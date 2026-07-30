package terminal

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// fakeSessions stands in for the terminal_sessions table.
type fakeSessions struct {
	mu       sync.Mutex
	created  []db.TerminalSession
	finished map[string]db.TerminalSessionResult
	createErr error
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{finished: map[string]db.TerminalSessionResult{}}
}

func (f *fakeSessions) CreateTerminalSession(_ context.Context, session *db.TerminalSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, *session)
	return nil
}

func (f *fakeSessions) FinishTerminalSession(
	_ context.Context, sessionID string, result db.TerminalSessionResult,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished[sessionID] = result
	return nil
}

func newRecorder(t *testing.T, sessions Sessions, maxBytes int64) (*Recorder, string) {
	t.Helper()
	dir := t.TempDir()
	recorder, err := NewRecorder(Options{Dir: dir, Sessions: sessions, MaxBytes: maxBytes})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	return recorder, dir
}

func meta() bastion.SessionMeta {
	return bastion.SessionMeta{
		SessionID: "abc123",
		UserID:    7,
		Username:  "devops",
		ClusterID: 3,
		Cluster:   "edge-us",
		Namespace: "team-a",
		Pod:       "api-0",
		Container: "api",
		Shell:     "/bin/sh",
		Verb:      "exec",
		At:        time.Now().UTC(),
		TTY:       true,
	}
}

// frames reads a recording back as its header and its event lines.
func frames(t *testing.T, path string) (map[string]any, [][]any) {
	t.Helper()

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open recording: %v", err)
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gunzip recording: %v", err)
	}
	defer gz.Close()

	scanner := bufio.NewScanner(gz)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)

	var header map[string]any
	events := [][]any{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if header == nil {
			if err := json.Unmarshal([]byte(line), &header); err != nil {
				t.Fatalf("header %q: %v", line, err)
			}
			continue
		}
		var event []any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("event %q: %v", line, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan recording: %v", err)
	}
	return header, events
}

func TestRecorderWritesAnAsciinemaCast(t *testing.T) {
	sessions := newFakeSessions()
	recorder, dir := newRecorder(t, sessions, 0)

	sink := recorder.Begin(context.Background(), meta())
	if sink == nil {
		t.Fatal("expected a recording to start")
	}
	sink.Resize(120, 40)
	sink.Output([]byte("$ "))
	sink.Input([]byte("id\r"))
	sink.Output([]byte("uid=0(root)\r\n"))
	sink.Close(nil)

	if len(sessions.created) != 1 {
		t.Fatalf("expected one indexed session, got %d", len(sessions.created))
	}
	created := sessions.created[0]
	if created.SessionID != "abc123" || created.PodName != "api-0" || created.Username != "devops" {
		t.Fatalf("the row does not describe the session: %+v", created)
	}
	if !strings.HasPrefix(created.StoragePath, dir) {
		t.Fatalf("recording %q is not under %q", created.StoragePath, dir)
	}
	if !strings.HasSuffix(created.StoragePath, FileExtension) {
		t.Fatalf("recording %q is not named as one", created.StoragePath)
	}

	header, events := frames(t, created.StoragePath)
	if header["version"] != float64(asciinemaVersion) {
		t.Fatalf("expected an asciinema v2 header, got %v", header["version"])
	}

	codes := []string{}
	for _, event := range events {
		if len(event) != 3 {
			t.Fatalf("an event must be [offset, code, data], got %v", event)
		}
		if _, ok := event[0].(float64); !ok {
			t.Fatalf("the offset must be a number, got %v", event[0])
		}
		codes = append(codes, event[1].(string))
	}
	// Output, input and resize all land, and in the order they happened.
	if strings.Join(codes, "") != "roio" {
		t.Fatalf("expected resize, output, input, output; got %v", codes)
	}
	if events[1][2].(string) != "$ " || events[2][2].(string) != "id\r" {
		t.Fatalf("payloads did not survive: %v", events)
	}

	result, ok := sessions.finished["abc123"]
	if !ok {
		t.Fatal("the session was never closed out")
	}
	// "$ " + "id\r" + "uid=0(root)\r\n" — the resize is not payload.
	if result.ByteCount != 18 {
		t.Fatalf("expected 18 recorded bytes, got %d", result.ByteCount)
	}
	if result.Truncated || result.Error != "" {
		t.Fatalf("a clean session must not be marked truncated or failed: %+v", result)
	}
}

func TestRecorderCapsOneRecording(t *testing.T) {
	sessions := newFakeSessions()
	recorder, _ := newRecorder(t, sessions, 16)

	sink := recorder.Begin(context.Background(), meta())
	if sink == nil {
		t.Fatal("expected a recording to start")
	}
	sink.Output([]byte(strings.Repeat("x", 20)))
	// Everything past the cap is dropped rather than growing the file forever.
	sink.Output([]byte(strings.Repeat("y", 4096)))
	sink.Close(nil)

	result := sessions.finished["abc123"]
	if !result.Truncated {
		t.Fatal("a recording past its cap must say so")
	}

	_, events := frames(t, sessions.created[0].StoragePath)
	joined := ""
	for _, event := range events {
		joined += event[2].(string)
	}
	if strings.Contains(joined, "yyyy") {
		t.Fatal("output past the cap was recorded anyway")
	}
	if !strings.Contains(joined, "truncated") {
		t.Fatal("the replay should say where it stops")
	}
}

func TestRecorderCarriesTheCauseThatEndedTheSession(t *testing.T) {
	sessions := newFakeSessions()
	recorder, _ := newRecorder(t, sessions, 0)

	sink := recorder.Begin(context.Background(), meta())
	sink.Output([]byte("hello"))
	sink.Close(errors.New("the agent disconnected"))

	if got := sessions.finished["abc123"].Error; got != "the agent disconnected" {
		t.Fatalf("expected the cause on the row, got %q", got)
	}
}

func TestRecorderLeavesNoFileWhenTheSessionCannotBeIndexed(t *testing.T) {
	sessions := newFakeSessions()
	sessions.createErr = errors.New("database is down")
	recorder, dir := newRecorder(t, sessions, 0)

	if sink := recorder.Begin(context.Background(), meta()); sink != nil {
		t.Fatal("a session that cannot be indexed must not be recorded")
	}

	// A recording nothing references is a file no retention pass will ever
	// reach, so it must not be left behind.
	var found []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if len(found) != 0 {
		t.Fatalf("expected no recordings on disk, found %v", found)
	}
}

func TestRecorderNeedsADirectoryAndAnIndex(t *testing.T) {
	if _, err := NewRecorder(Options{Dir: "", Sessions: newFakeSessions()}); err == nil {
		t.Fatal("a recorder with nowhere to write must be refused")
	}
	if _, err := NewRecorder(Options{Dir: t.TempDir()}); err == nil {
		t.Fatal("a recorder with no session index must be refused")
	}
}

func TestOpenDecompressesAndConfines(t *testing.T) {
	sessions := newFakeSessions()
	recorder, dir := newRecorder(t, sessions, 0)

	sink := recorder.Begin(context.Background(), meta())
	sink.Output([]byte("visible"))
	sink.Close(nil)

	reader, err := Open(dir, sessions.created[0].StoragePath, nil)
	if err != nil {
		t.Fatalf("open recording: %v", err)
	}
	defer reader.Close()

	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read recording: %v", err)
	}
	if !strings.Contains(string(body), "visible") {
		t.Fatalf("the decompressed stream is missing its output: %q", body)
	}

	// A path out of a database row is not a filesystem instruction.
	if _, err := Open(dir, filepath.Join(dir, "..", "etc", "passwd"), nil); !errors.Is(err, ErrOutsideDir) {
		t.Fatalf("expected the escape to be refused, got %v", err)
	}
	// A sibling whose name merely starts with the directory's must not pass.
	if _, err := Open(dir, dir+"-elsewhere/x.cast.gz", nil); !errors.Is(err, ErrOutsideDir) {
		t.Fatalf("expected a sibling directory to be refused, got %v", err)
	}
	if _, err := Open(dir, filepath.Join(dir, "missing.cast.gz"), nil); !errors.Is(err, ErrMissing) {
		t.Fatalf("expected a missing recording to be reported as such, got %v", err)
	}
}

func TestRemoveTakesTheRecordingAndToleratesAMissingOne(t *testing.T) {
	sessions := newFakeSessions()
	recorder, dir := newRecorder(t, sessions, 0)

	sink := recorder.Begin(context.Background(), meta())
	sink.Close(nil)
	path := sessions.created[0].StoragePath

	if err := Remove(dir, path); err != nil {
		t.Fatalf("remove recording: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the recording is still on disk: %v", err)
	}
	// The row is what a caller is removing; it must not survive because its file
	// is already gone.
	if err := Remove(dir, path); err != nil {
		t.Fatalf("removing a missing recording should be fine, got %v", err)
	}
}
