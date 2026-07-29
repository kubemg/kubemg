// Package terminal records interactive container sessions so they can be
// replayed later.
//
// The format is asciinema v2, which is a JSON header line followed by one JSON
// array per event: [offset, code, data]. It is chosen because it is the format
// of the tooling operators already have — an exported recording plays in
// asciinema itself, not only in KubeMG's own player — and because it is a text
// stream, so it compresses to a fraction of its size and needs no index to play
// from the start.
//
// A recording is a *tee* of a session the proxy is already carrying. Nothing
// here can refuse a session or slow one down: a disk that stops accepting writes
// ends the recording and leaves the shell running, because a gateway that
// dropped a production shell over a full volume would be a worse product than
// one with a gap in its recordings and a line in the log saying so.
package terminal

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// Recording defaults.
const (
	// DefaultMaxBytes caps one recording. A shell that prints a gigabyte is
	// almost always a `cat` of something large, and holding the first 32 MiB of
	// it answers "what did they do" while a truncation flag says the rest was
	// dropped.
	DefaultMaxBytes = 32 << 20

	// FileExtension is what a recording is named, so an operator who finds one
	// on disk knows what to open it with.
	FileExtension = ".cast.gz"

	// defaultCols and defaultRows are the geometry a recording starts at. The
	// real size arrives as the client's first resize message, a moment later, and
	// is recorded as a resize event the way any later one is.
	defaultCols = 80
	defaultRows = 24

	// asciinemaVersion is the format version written into the header.
	asciinemaVersion = 2
)

// Sessions is the persistence a recorder needs: a row when a session opens, and
// the same row closed out when it ends.
type Sessions interface {
	CreateTerminalSession(ctx context.Context, session *db.TerminalSession) error
	FinishTerminalSession(ctx context.Context, sessionID string, result db.TerminalSessionResult) error
}

// Options wires a recorder.
type Options struct {
	// Dir is where recordings are written. It is created if it does not exist.
	Dir string
	// Sessions indexes the recordings. Required: a recording nothing references
	// is a file nobody will ever find.
	Sessions Sessions
	// MaxBytes caps one recording. Zero takes DefaultMaxBytes.
	MaxBytes int64
	Logger   *slog.Logger
}

// Recorder writes asciinema recordings into a directory and indexes them in the
// database. It satisfies bastion.SessionRecorder.
type Recorder struct {
	dir      string
	sessions Sessions
	maxBytes int64
	logger   *slog.Logger
}

// NewRecorder prepares the recording directory. It fails rather than silently
// not recording: recording is either configured and working or explicitly off,
// and the difference has to be visible at boot rather than discovered by an
// auditor looking for a session that was never written.
func NewRecorder(opts Options) (*Recorder, error) {
	dir := filepath.Clean(opts.Dir)
	if dir == "" || dir == "." {
		return nil, fmt.Errorf("terminal recording: no directory configured")
	}
	if opts.Sessions == nil {
		return nil, fmt.Errorf("terminal recording: no session index configured")
	}
	// 0o700: a recording holds everything that crossed a production shell,
	// keystrokes included. Nothing but this process has any business reading it.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("terminal recording: prepare %s: %w", dir, err)
	}

	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Recorder{dir: dir, sessions: opts.Sessions, maxBytes: maxBytes, logger: logger}, nil
}

// Dir is where this recorder writes, which is also the directory the HTTP layer
// confines a stored path to before reading it back.
func (r *Recorder) Dir() string { return r.dir }

// Begin opens a recording. A failure to open one is logged and returns nil, so
// the session proceeds unrecorded rather than not at all.
func (r *Recorder) Begin(ctx context.Context, meta bastion.SessionMeta) bastion.SessionSink {
	started := meta.At
	if started.IsZero() {
		started = time.Now().UTC()
	}

	path := r.pathFor(meta, started)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		r.logger.Error("could not prepare a terminal recording directory",
			slog.String("session_id", meta.SessionID),
			slog.String("error", err.Error()))
		return nil
	}

	// O_EXCL: a session id collision would otherwise overwrite an existing
	// recording, and an audit artefact that can be replaced is not one.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		r.logger.Error("could not open a terminal recording",
			slog.String("session_id", meta.SessionID),
			slog.String("path", path),
			slog.String("error", err.Error()))
		return nil
	}

	recording := &cast{
		recorder: r,
		meta:     meta,
		started:  started,
		file:     file,
		gz:       gzip.NewWriter(file),
		cols:     defaultCols,
		rows:     defaultRows,
	}
	if err := recording.writeHeader(); err != nil {
		recording.abandon(err)
		return nil
	}

	session := &db.TerminalSession{
		SessionID:     meta.SessionID,
		UserID:        meta.UserID,
		Username:      meta.Username,
		ClusterID:     meta.ClusterID,
		Cluster:       meta.Cluster,
		Namespace:     meta.Namespace,
		PodName:       meta.Pod,
		ContainerName: meta.Container,
		Shell:         meta.Shell,
		Verb:          meta.Verb,
		StartedAt:     started,
		StoragePath:   path,
	}
	if err := r.sessions.CreateTerminalSession(ctx, session); err != nil {
		// Without a row the recording is unreachable, so there is no point
		// writing it. Take the file back out rather than leaving an orphan.
		recording.abandon(err)
		return nil
	}
	return recording
}

// pathFor lays recordings out by cluster and day. A flat directory of a hundred
// thousand files is one an operator cannot work with, and the two things anyone
// narrows by when looking on disk are which cluster and when.
func (r *Recorder) pathFor(meta bastion.SessionMeta, started time.Time) string {
	cluster := strconv.FormatUint(uint64(meta.ClusterID), 10)
	day := started.UTC().Format("2006-01-02")
	name := meta.SessionID
	if name == "" {
		name = strconv.FormatInt(started.UTC().UnixNano(), 10)
	}
	return filepath.Join(r.dir, "cluster-"+cluster, day, name+FileExtension)
}

// cast is one open recording. Both directions of a live session write to it, so
// every method takes the lock.
type cast struct {
	recorder *Recorder
	meta     bastion.SessionMeta
	started  time.Time

	mu        sync.Mutex
	file      *os.File
	gz        *gzip.Writer
	cols      int
	rows      int
	bytes     int64
	truncated bool
	// broken holds the first write failure. Once set, the recording stops
	// accepting frames; the session itself carries on.
	broken error
	closed bool
}

// Output records what the container printed.
func (c *cast) Output(data []byte) { c.event("o", data) }

// Input records what the operator typed.
func (c *cast) Input(data []byte) { c.event("i", data) }

// Resize records a geometry change.
func (c *cast) Resize(cols, rows int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cols == c.cols && rows == c.rows {
		return
	}
	c.cols, c.rows = cols, rows
	c.writeFrame("r", []byte(strconv.Itoa(cols)+"x"+strconv.Itoa(rows)))
}

// event appends one output or input frame.
func (c *cast) event(code string, data []byte) {
	if len(data) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.bytes >= c.recorder.maxBytes {
		if !c.truncated {
			c.truncated = true
			c.writeFrame("o", []byte("\r\n[kubemg] recording truncated: "+
				"this session exceeded the per-recording limit\r\n"))
		}
		return
	}
	c.bytes += int64(len(data))
	c.writeFrame(code, data)
}

// writeFrame appends one event line. The caller holds the lock.
//
// Frames are written as they arrive rather than buffered by the recorder: gzip
// already buffers, and a session that ends badly should still play up to the
// point it stopped.
func (c *cast) writeFrame(code string, data []byte) {
	if c.broken != nil || c.closed {
		return
	}
	// json.Marshal on a []any keeps the array shape asciinema requires, and
	// escapes the payload — including replacing invalid UTF-8, which terminal
	// output regularly contains.
	line, err := json.Marshal([]any{time.Since(c.started).Seconds(), code, string(data)})
	if err != nil {
		c.fail(err)
		return
	}
	if _, err := c.gz.Write(append(line, '\n')); err != nil {
		c.fail(err)
	}
}

// writeHeader writes the asciinema header line.
func (c *cast) writeHeader() error {
	header := map[string]any{
		"version":   asciinemaVersion,
		"width":     c.cols,
		"height":    c.rows,
		"timestamp": c.started.Unix(),
		// What the recording is of, so a file found on disk explains itself.
		"title": c.meta.Verb + " " + c.meta.Namespace + "/" + c.meta.Pod,
		"env":   map[string]string{"TERM": "xterm-256color", "SHELL": c.meta.Shell},
	}
	line, err := json.Marshal(header)
	if err != nil {
		return err
	}
	if _, err := c.gz.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

// fail records the first write error and gives up on this recording. The caller
// holds the lock.
func (c *cast) fail(err error) {
	if c.broken != nil {
		return
	}
	c.broken = err
	c.recorder.logger.Error("terminal recording stopped early",
		slog.String("session_id", c.meta.SessionID),
		slog.String("error", err.Error()))
}

// Close finishes the recording and closes out its row.
func (c *cast) Close(cause error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true

	// Flush before recording the size: an unflushed gzip tail is an unplayable
	// file, and the row must not claim a recording that cannot be read.
	if err := c.gz.Close(); err != nil && c.broken == nil {
		c.broken = err
	}
	if err := c.file.Close(); err != nil && c.broken == nil {
		c.broken = err
	}

	result := db.TerminalSessionResult{
		EndedAt:   time.Now().UTC(),
		Duration:  time.Since(c.started),
		ByteCount: c.bytes,
		Truncated: c.truncated,
	}
	switch {
	case c.broken != nil:
		result.Error = "recording failed: " + c.broken.Error()
	case cause != nil:
		result.Error = cause.Error()
	}
	sessionID := c.meta.SessionID
	c.mu.Unlock()

	// A detached context: the session has already ended, and the row describing
	// how it ended has to be written even when the request that carried it is
	// gone.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.recorder.sessions.FinishTerminalSession(ctx, sessionID, result); err != nil {
		c.recorder.logger.Error("could not close out a terminal recording",
			slog.String("session_id", sessionID),
			slog.String("error", err.Error()))
	}
}

// abandon discards a recording that never became one, taking the file with it so
// the directory holds no unreferenced recordings.
func (c *cast) abandon(cause error) {
	c.closed = true
	_ = c.gz.Close()
	name := c.file.Name()
	_ = c.file.Close()
	_ = os.Remove(name)

	c.recorder.logger.Error("could not start a terminal recording; the session is not being recorded",
		slog.String("session_id", c.meta.SessionID),
		slog.String("error", cause.Error()))
}
