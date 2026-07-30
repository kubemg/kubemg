package terminal

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// testKey is a deterministic 32-byte key; the tests care about the construction,
// not about where the bytes came from.
func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = byte(i * 7)
	}
	return key
}

// encryptedRecorder is a recorder writing encrypted recordings.
func encryptedRecorder(t *testing.T, sessions Sessions, key []byte) (*Recorder, string) {
	t.Helper()
	dir := t.TempDir()
	recorder, err := NewRecorder(Options{Dir: dir, Sessions: sessions, Key: key})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	return recorder, dir
}

func TestParseKeyAcceptsHexAndBase64AndRefusesTheRest(t *testing.T) {
	raw := make([]byte, KeySize)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("random key: %v", err)
	}

	for name, encoded := range map[string]string{
		"hex":              hex.EncodeToString(raw),
		"base64":           base64.StdEncoding.EncodeToString(raw),
		"base64 unpadded":  base64.RawStdEncoding.EncodeToString(raw),
		"base64 url":       base64.URLEncoding.EncodeToString(raw),
		"surrounded by ws": "  " + base64.StdEncoding.EncodeToString(raw) + "\n",
	} {
		key, err := ParseKey(encoded)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(key, raw) {
			t.Fatalf("%s: decoded to the wrong key", name)
		}
	}

	// Empty is "no encryption", which is a configuration, not an error.
	if key, err := ParseKey("   "); err != nil || key != nil {
		t.Fatalf("an empty key must read as unset, got %v / %v", key, err)
	}

	// A passphrase is refused rather than stretched: a KDF would need a stored
	// salt, and it would invite twenty bits of entropy protecting the most
	// sensitive file on the volume.
	for _, bad := range []string{
		"correct horse battery staple",
		hex.EncodeToString(raw[:16]),
		base64.StdEncoding.EncodeToString(raw[:31]),
	} {
		if _, err := ParseKey(bad); err == nil {
			t.Fatalf("%q must be refused as a recording key", bad)
		}
	}
}

func TestEncryptedRecordingRoundTrips(t *testing.T) {
	sessions := newFakeSessions()
	key := testKey(t)
	recorder, dir := encryptedRecorder(t, sessions, key)

	if !recorder.Encrypting() {
		t.Fatal("a recorder with a key must report that it encrypts")
	}

	sink := recorder.Begin(context.Background(), meta())
	// More than one chunk's worth, so the framing is exercised rather than a
	// single seal that happens to fit.
	sink.Output([]byte(strings.Repeat("secret output ", 12_000)))
	sink.Input([]byte("hunter2\r"))
	sink.Close(nil)

	stored := sessions.created[0]
	if !stored.Encrypted {
		t.Fatal("the row must record that the file was written encrypted")
	}

	// Nothing legible on disk: this is the whole point of the exercise.
	onDisk, err := os.ReadFile(stored.StoragePath)
	if err != nil {
		t.Fatalf("read the raw file: %v", err)
	}
	if !bytes.HasPrefix(onDisk, []byte(magic)) {
		t.Fatalf("an encrypted recording must be identifiable as one, got %q", onDisk[:8])
	}
	for _, leaked := range []string{"secret output", "hunter2"} {
		if bytes.Contains(onDisk, []byte(leaked)) {
			t.Fatalf("%q is readable in the stored file", leaked)
		}
	}

	reader, err := Open(dir, stored.StoragePath, key)
	if err != nil {
		t.Fatalf("open recording: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read recording: %v", err)
	}
	if !bytes.Contains(body, []byte("secret output")) || !bytes.Contains(body, []byte("hunter2")) {
		t.Fatal("the decrypted stream is not the session that was recorded")
	}
}

func TestEncryptedRecordingRefusesTheWrongKeyAndNoKey(t *testing.T) {
	sessions := newFakeSessions()
	key := testKey(t)
	recorder, dir := encryptedRecorder(t, sessions, key)

	sink := recorder.Begin(context.Background(), meta())
	sink.Output([]byte("hello"))
	sink.Close(nil)
	path := sessions.created[0].StoragePath

	other := testKey(t)
	other[0]++
	if _, err := Open(dir, path, other); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("the wrong key must be refused, got %v", err)
	}

	// The evidence still exists; what is missing is the key, and saying which
	// tells an operator to restore it rather than conclude the recording is gone.
	if _, err := Open(dir, path, nil); !errors.Is(err, ErrKeyRequired) {
		t.Fatalf("an encrypted recording with no key must say so, got %v", err)
	}
}

func TestAlteredRecordingDoesNotDecrypt(t *testing.T) {
	sessions := newFakeSessions()
	key := testKey(t)
	recorder, dir := encryptedRecorder(t, sessions, key)

	sink := recorder.Begin(context.Background(), meta())
	sink.Output([]byte("the session as it happened"))
	sink.Close(nil)
	path := sessions.created[0].StoragePath

	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recording: %v", err)
	}
	// One bit inside the ciphertext. An audit artefact that can be edited in
	// place is not one.
	altered := append([]byte(nil), stored...)
	altered[len(altered)-1] ^= 0x01
	if err := os.WriteFile(path, altered, 0o600); err != nil {
		t.Fatalf("rewrite recording: %v", err)
	}

	if _, err := Open(dir, path, key); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("an altered recording must not decrypt, got %v", err)
	}
}

func TestTruncatedRecordingIsReportedRatherThanShortened(t *testing.T) {
	sessions := newFakeSessions()
	key := testKey(t)
	recorder, dir := encryptedRecorder(t, sessions, key)

	sink := recorder.Begin(context.Background(), meta())
	sink.Output([]byte(strings.Repeat("output ", 20_000)))
	sink.Close(nil)
	path := sessions.created[0].StoragePath

	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read recording: %v", err)
	}
	// Cut the tail off, which is what a half-written file or a trimmed one looks
	// like. The remaining chunks are authentic, so the only thing that catches
	// this is the missing end-of-stream marker.
	if err := os.WriteFile(path, stored[:len(stored)/2], 0o600); err != nil {
		t.Fatalf("truncate recording: %v", err)
	}

	reader, err := Open(dir, path, key)
	if err != nil {
		if !errors.Is(err, ErrTruncated) && !errors.Is(err, ErrKeyMismatch) {
			t.Fatalf("expected a truncation to be reported, got %v", err)
		}
		return
	}
	defer reader.Close()
	if _, err := io.ReadAll(reader); !errors.Is(err, ErrTruncated) {
		t.Fatalf("a recording that stops early must not read as a complete one, got %v", err)
	}
}

func TestPlainRecordingsStillReadAfterAKeyIsConfigured(t *testing.T) {
	sessions := newFakeSessions()
	// Written with no key, as every recording made before encryption existed was.
	recorder, dir := newRecorder(t, sessions, 0)

	sink := recorder.Begin(context.Background(), meta())
	sink.Output([]byte("older recording"))
	sink.Close(nil)

	if sessions.created[0].Encrypted {
		t.Fatal("a recording written without a key is not encrypted")
	}

	// Turning encryption on must not orphan what is already on the volume: the
	// format is read from the file, not from configuration.
	reader, err := Open(dir, sessions.created[0].StoragePath, testKey(t))
	if err != nil {
		t.Fatalf("open a plain recording with a key configured: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read recording: %v", err)
	}
	if !bytes.Contains(body, []byte("older recording")) {
		t.Fatalf("the older recording did not come back: %q", body)
	}
}

func TestOmitInputKeepsOutputAndDropsKeystrokes(t *testing.T) {
	sessions := newFakeSessions()
	dir := t.TempDir()
	recorder, err := NewRecorder(Options{Dir: dir, Sessions: sessions, OmitInput: true})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	if recorder.RecordingInput() {
		t.Fatal("a recorder configured to omit input must report that it does")
	}

	sink := recorder.Begin(context.Background(), meta())
	sink.Output([]byte("uid=0(root)"))
	// What a prompt deliberately does not echo is exactly what this drops.
	sink.Input([]byte("s3cr3t-token\r"))
	sink.Close(nil)

	stored := sessions.created[0]
	if stored.InputRecorded {
		t.Fatal("the row must say keystrokes were not collected")
	}

	reader, err := Open(dir, stored.StoragePath, nil)
	if err != nil {
		t.Fatalf("open recording: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read recording: %v", err)
	}
	if !bytes.Contains(body, []byte("uid=0(root)")) {
		t.Fatal("output must still be recorded")
	}
	if bytes.Contains(body, []byte("s3cr3t-token")) {
		t.Fatal("a keystroke reached a recording that is not collecting them")
	}
	if bytes.Contains(body, []byte(`"i"`)) {
		t.Fatal("no input frame should have been written at all")
	}
}

func TestRecorderRefusesAKeyOfTheWrongLength(t *testing.T) {
	// Refused at construction rather than at the first session: an operator who
	// set a key believes the files are encrypted, and a server that quietly
	// disagreed would be worse than one that will not start recording.
	if _, err := NewRecorder(Options{
		Dir: t.TempDir(), Sessions: newFakeSessions(), Key: []byte("too short"),
	}); err == nil {
		t.Fatal("a key of the wrong length must be refused")
	}
}
