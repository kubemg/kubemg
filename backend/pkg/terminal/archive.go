package terminal

import (
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

// Open decompresses a recording for playback, confining it to dir first.
//
// The caller gets the asciinema stream itself, not the gzip. Decompressing here
// rather than in the browser is what keeps the player a player: it reads lines,
// and nothing about the storage format reaches it.
func Open(dir, path string) (io.ReadCloser, error) {
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

	gz, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
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
