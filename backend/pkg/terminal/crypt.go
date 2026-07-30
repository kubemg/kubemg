package terminal

// Encryption at rest for recordings.
//
// A recording is the most sensitive artefact this product writes. It holds
// everything a production shell printed and — unless input recording is turned
// off — everything that was typed into one, which includes the passwords a
// prompt never echoes: `mysql -p`, `vault login`, a token pasted into an
// interactive tool. File permissions (0600 in a 0700 directory) protect it from
// other processes on the host and from nothing else: a volume snapshot, a
// misconfigured backup, a debug container mounting the PVC, or root on the node
// all read it in the clear. So the file is encrypted with a key that lives in
// the process's environment rather than beside it on the disk.
//
// The construction is a chunked AEAD stream (the shape age and TLS use), not a
// single AES-GCM seal over the whole file:
//
//   - A recording is written incrementally over what may be hours, and sealing
//     one blob would mean holding the whole session in memory and losing
//     everything if the process died mid-session.
//   - Playback streams; a reader must be able to authenticate and hand over the
//     first chunk without having the last.
//
// Each chunk carries its own nonce derived from a per-file random prefix and a
// counter, and its sequence number and end-of-stream flag are authenticated as
// additional data. That is what makes reordering, dropping, duplicating or
// truncating chunks a decryption failure rather than a shorter recording: an
// audit artefact that can be silently trimmed is not one.
//
// Compress-then-encrypt is deliberate. The compression-oracle attacks that make
// that ordering dangerous (CRIME and its relatives) need an attacker who can
// inject chosen plaintext into the stream *and* observe its compressed size
// repeatedly; a file written once to disk offers neither. Encrypting first would
// mean storing incompressible data, and a recording that no longer compresses is
// roughly ten times its size on a volume an operator has to size in advance.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// KeySize is the AES-256 key length a recording is encrypted with.
const KeySize = 32

// The container format.
const (
	// magic identifies an encrypted recording. A file that does not start with
	// it is read as a plain gzip cast, which is what recordings written before
	// a key was configured are — turning encryption on must not orphan them.
	magic = "KMGCAST1"

	// noncePrefixSize is the random half of every chunk's nonce; the other four
	// bytes are the chunk counter, making a 12-byte GCM nonce. Random per file
	// rather than per chunk, so nonce reuse under one key would need a repeat of
	// the prefix *and* the same counter.
	noncePrefixSize = 8

	// chunkPlaintext is how much cleartext one chunk carries. 64 KiB keeps the
	// per-chunk overhead (16-byte tag plus a 4-byte header) under 0.04% while
	// staying small enough that a reader's buffer is not a consideration.
	chunkPlaintext = 64 << 10

	// finalBit marks the last chunk in the length header. It is authenticated as
	// additional data, so it cannot be flipped to make a truncated file look
	// complete.
	finalBit = uint32(1) << 31

	// maxChunkCipher bounds what a reader will allocate for one chunk, so a
	// corrupt or hostile length header cannot ask for an arbitrary allocation.
	maxChunkCipher = chunkPlaintext + 64
)

// ErrKeyRequired is an encrypted recording read by a server with no key
// configured. It is a distinct error because the fix is an operator action —
// restore KUBEMG_SESSION_RECORDING_KEY — and not something the caller did wrong.
var ErrKeyRequired = errors.New("recording is encrypted and no recording key is configured")

// ErrKeyMismatch is an encrypted recording that will not authenticate: the wrong
// key, or a file that has been altered. The two are deliberately one error —
// AEAD cannot tell them apart, and neither answer changes what an operator does
// next.
var ErrKeyMismatch = errors.New("recording could not be decrypted with the configured key")

// ErrTruncated is a recording whose stream ends without its final chunk. The
// bytes that are there are authentic; what is missing cannot be recovered, and
// silently treating it as a complete recording would misrepresent evidence.
var ErrTruncated = errors.New("recording is truncated")

// ParseKey reads a recording key from configuration. It accepts 32 raw bytes
// written as hex or base64 and refuses anything else — including a passphrase,
// which would need a KDF and a stored salt, and would invite a key with a few
// bits of entropy protecting the most sensitive file on the volume.
func ParseKey(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}

	if decoded, err := hex.DecodeString(trimmed); err == nil && len(decoded) == KeySize {
		return decoded, nil
	}
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		if decoded, err := encoding.DecodeString(trimmed); err == nil && len(decoded) == KeySize {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf(
		"recording key must be %d bytes as hex or base64 (generate one with: openssl rand -base64 %d)",
		KeySize, KeySize)
}

// Encrypted reports whether a recording written now would be encrypted, which is
// the same question as whether a key is configured.
func Encrypted(key []byte) bool { return len(key) == KeySize }

// newChunkedWriter wraps w so that everything written to it is encrypted in
// place. Close seals the final chunk and must be called: without it the file
// reads as truncated, which is exactly what an interrupted write should look
// like.
func newChunkedWriter(w io.Writer, key []byte) (io.WriteCloser, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}

	prefix := make([]byte, noncePrefixSize)
	if _, err := rand.Read(prefix); err != nil {
		return nil, fmt.Errorf("recording key stream: %w", err)
	}
	if _, err := w.Write([]byte(magic)); err != nil {
		return nil, err
	}
	if _, err := w.Write(prefix); err != nil {
		return nil, err
	}
	return &chunkWriter{
		w:      w,
		gcm:    gcm,
		prefix: prefix,
		buf:    make([]byte, 0, chunkPlaintext),
	}, nil
}

type chunkWriter struct {
	w      io.Writer
	gcm    cipher.AEAD
	prefix []byte
	buf    []byte
	// counter is the sequence number of the next chunk, authenticated with it.
	counter uint32
	closed  bool
	// broken holds the first write failure. Once set every later call fails the
	// same way rather than writing chunks around a gap.
	broken error
}

func (c *chunkWriter) Write(p []byte) (int, error) {
	if c.broken != nil {
		return 0, c.broken
	}
	if c.closed {
		return 0, errors.New("write to a closed recording")
	}

	written := 0
	for len(p) > 0 {
		room := chunkPlaintext - len(c.buf)
		take := min(room, len(p))
		c.buf = append(c.buf, p[:take]...)
		p = p[take:]
		written += take

		if len(c.buf) == chunkPlaintext {
			if err := c.flush(false); err != nil {
				return written, err
			}
		}
	}
	return written, nil
}

func (c *chunkWriter) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	if c.broken != nil {
		return c.broken
	}
	// The final chunk is written even when it is empty: it is what says the
	// stream ended on purpose.
	return c.flush(true)
}

// flush seals the buffered chunk. The header is written before the ciphertext so
// a reader knows how much to read, and carries the end-of-stream flag in its
// high bit.
func (c *chunkWriter) flush(final bool) error {
	sealed := c.gcm.Seal(nil, c.nonce(), c.buf, additionalData(c.counter, final))

	header := uint32(len(sealed))
	if final {
		header |= finalBit
	}
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], header)

	if _, err := c.w.Write(head[:]); err != nil {
		c.broken = err
		return err
	}
	if _, err := c.w.Write(sealed); err != nil {
		c.broken = err
		return err
	}
	c.buf = c.buf[:0]
	c.counter++
	return nil
}

func (c *chunkWriter) nonce() []byte {
	nonce := make([]byte, 0, len(c.prefix)+4)
	nonce = append(nonce, c.prefix...)
	return binary.BigEndian.AppendUint32(nonce, c.counter)
}

// newChunkedReader decrypts a stream written by newChunkedWriter. The magic has
// already been consumed by the caller, which is how it decided to call this at
// all; the nonce prefix is read here.
func newChunkedReader(r io.Reader, key []byte) (io.Reader, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	prefix := make([]byte, noncePrefixSize)
	if _, err := io.ReadFull(r, prefix); err != nil {
		return nil, ErrTruncated
	}
	return &chunkReader{r: r, gcm: gcm, prefix: prefix}, nil
}

type chunkReader struct {
	r      io.Reader
	gcm    cipher.AEAD
	prefix []byte

	plain   []byte
	counter uint32
	done    bool
	err     error
}

func (c *chunkReader) Read(p []byte) (int, error) {
	for len(c.plain) == 0 {
		if c.err != nil {
			return 0, c.err
		}
		if c.done {
			return 0, io.EOF
		}
		if err := c.next(); err != nil {
			c.err = err
			return 0, err
		}
	}
	n := copy(p, c.plain)
	c.plain = c.plain[n:]
	return n, nil
}

// next reads and authenticates one chunk.
func (c *chunkReader) next() error {
	var head [4]byte
	if _, err := io.ReadFull(c.r, head[:]); err != nil {
		// A stream that stops between chunks stopped without saying it was
		// finished, which is what truncation looks like.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return ErrTruncated
		}
		return err
	}

	header := binary.BigEndian.Uint32(head[:])
	final := header&finalBit != 0
	length := header & ^finalBit
	if length > maxChunkCipher {
		return ErrKeyMismatch
	}

	sealed := make([]byte, length)
	if _, err := io.ReadFull(c.r, sealed); err != nil {
		return ErrTruncated
	}

	nonce := append(append(make([]byte, 0, len(c.prefix)+4), c.prefix...), 0, 0, 0, 0)
	binary.BigEndian.PutUint32(nonce[len(c.prefix):], c.counter)

	plain, err := c.gcm.Open(nil, nonce, sealed, additionalData(c.counter, final))
	if err != nil {
		// Wrong key, altered ciphertext, or chunks that have been reordered or
		// dropped — the sequence number and the final flag are authenticated, so
		// all of those land here rather than producing plausible output.
		return ErrKeyMismatch
	}

	c.plain = plain
	c.counter++
	c.done = final
	return nil
}

// additionalData binds a chunk to its position in the stream and to whether it
// ends it.
func additionalData(counter uint32, final bool) []byte {
	data := binary.BigEndian.AppendUint32(nil, counter)
	if final {
		return append(data, 1)
	}
	return append(data, 0)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if !Encrypted(key) {
		return nil, fmt.Errorf("recording key must be %d bytes", KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("recording cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("recording cipher: %w", err)
	}
	return gcm, nil
}
