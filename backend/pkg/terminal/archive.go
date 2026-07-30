package terminal

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrOutsideDir is a stored path that does not live under the recording
// directory. It is refused rather than read: the path comes out of a database
// row, and a row is not a thing to hand straight to the filesystem.
var ErrOutsideDir = errors.New("recording path is outside the recording directory")

// ErrMissing is a row whose recording is no longer on disk — a volume that was
// not mounted back, or a file somebody removed by hand.
var ErrMissing = errors.New("recording file is missing")

// Open decrypts and decompresses a recording for playback, confining it to dir
// first.
//
// The caller gets the asciinema stream itself, not the storage format. Doing
// both here rather than in the browser is what keeps the player a player: it
// reads lines, and neither the cipher nor the gzip reaches it — which also means
// a recording is never handed to a browser cache as a file that could be saved
// and replayed elsewhere without going through this authorization again.
//
// Whether a file is encrypted is read from the file, not from configuration:
// recordings written before a key was configured are plain gzip, and turning
// encryption on must not orphan them. The reverse — an encrypted file and no key
// — is ErrKeyRequired, because the recording still exists and the fix is to
// restore the key rather than to conclude the evidence is gone.
func Open(dir, path string, key []byte) (io.ReadCloser, error) {
	resolved, err := Resolve(dir, path)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrMissing
	}
	if err != nil {
		return nil, fmt.Errorf("open recording: %w", err)
	}

	// Buffered so the magic can be inspected and put back: which format this is
	// decides what wraps the file, and that has to be known before anything
	// consumes a byte of it.
	buffered := bufio.NewReader(file)
	head, err := buffered.Peek(len(magic))
	if err != nil && !errors.Is(err, io.EOF) {
		_ = file.Close()
		return nil, fmt.Errorf("read recording: %w", err)
	}

	var body io.Reader = buffered
	if string(head) == magic {
		if !Encrypted(key) {
			_ = file.Close()
			return nil, ErrKeyRequired
		}
		if _, err := buffered.Discard(len(magic)); err != nil {
			_ = file.Close()
			return nil, ErrTruncated
		}
		decrypted, err := newChunkedReader(buffered, key)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		body = decrypted
	}

	// gzip reads its header eagerly, which means the first chunk is decrypted
	// here: a wrong key or an altered file fails at Open rather than part way
	// through a replay. Those errors are passed through as themselves, because
	// "could not be decrypted" and "this file is not a recording" call for
	// different operator actions.
	gz, err := gzip.NewReader(body)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, ErrKeyMismatch) || errors.Is(err, ErrTruncated) {
			return nil, err
		}
		return nil, fmt.Errorf("read recording: %w", err)
	}
	return &castReader{gz: gz, file: file}, nil
}

// Remove deletes a recording, confining it to dir the same way Open does. A file
// that is already gone is not an error: the row is what the caller is removing,
// and it should not survive because its file did not.
func Remove(dir, path string) error {
	resolved, err := Resolve(dir, path)
	if err != nil {
		return err
	}
	if err := os.Remove(resolved); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove recording: %w", err)
	}
	return nil
}

// Resolve validates that a stored path names a recording inside dir and returns
// it cleaned. Containment is checked on the cleaned, separator-terminated
// prefix, so a sibling directory whose name merely starts with dir's does not
// pass.
func Resolve(dir, path string) (string, error) {
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(path) == "" {
		return "", ErrOutsideDir
	}
	root := filepath.Clean(dir)
	resolved := filepath.Clean(path)

	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", ErrOutsideDir
	}
	return resolved, nil
}

// castReader closes the gzip reader and the file underneath it together.
type castReader struct {
	gz   *gzip.Reader
	file *os.File
}

func (r *castReader) Read(p []byte) (int, error) { return r.gz.Read(p) }

func (r *castReader) Close() error {
	err := r.gz.Close()
	if closeErr := r.file.Close(); err == nil {
		err = closeErr
	}
	return err
}
