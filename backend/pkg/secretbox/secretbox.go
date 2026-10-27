// Package secretbox encrypts the credentials KubeMG keeps in its database.
//
// The database is the crown jewel of an install: it holds the key that signs
// every session and every agent-mode kubeconfig, the tunnel credential of every
// agent, direct-mode ServiceAccount tokens, and the passwords KubeMG presents to
// identity providers, datasources, chart repositories and alarm destinations. A
// dump of it — a backup copied somewhere it should not be, a read replica, a
// support bundle — used to be all of those in the clear.
//
// Each value that has to be *read back* is sealed with AES-256-GCM under a key
// that lives in the process's environment (KUBEMG_SECRET_KEY), never in the
// database beside the ciphertext. A value that only ever has to be *verified*
// is hashed instead, and does not come through here.
//
// A sealed value is text, so it fits the columns it replaces:
//
//	enc:v1:<base64(nonce || ciphertext || tag)>
//
// The prefix makes an encrypted value recognisable — which is what lets the
// boot migration skip what it has already done, and lets a server with no key
// refuse ciphertext by name instead of handing it to a cluster as a token.
//
// Moving to a new key is a rotation, not a format change. A Box can carry the
// key it replaces (KUBEMG_SECRET_KEY_PREVIOUS) as an opener only: it seals under
// the current key, opens under either, and says which one answered, so the boot
// pass re-seals every value the old key opened. No key id is written into the
// value — a v1 value under either key is still a v1 value, so a rotation needs
// no rewrite of the prefix and a rollback to the release before it reads what
// was sealed. Trying the previous key costs one extra GCM open, and only while a
// rotation is configured.
package secretbox

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// KeySize is the AES-256 key length.
const KeySize = 32

// Prefix marks a sealed value. Anything without it is plaintext.
const Prefix = "enc:v1:"

// ErrKeyRequired is a sealed value read by a server with no key configured.
var ErrKeyRequired = errors.New("value is encrypted and KUBEMG_SECRET_KEY is not set")

// ErrKeyMismatch is a sealed value that will not authenticate: the wrong key,
// or a value that was altered. AEAD cannot tell the two apart, and neither
// answer changes what an operator does next.
var ErrKeyMismatch = errors.New("value could not be decrypted with KUBEMG_SECRET_KEY (wrong key, or the value was altered)")

// ErrPreviousWithoutKey is a previous key with no current one. A rotation moves
// values to a new key; it is never a way to take encryption off.
var ErrPreviousWithoutKey = errors.New("KUBEMG_SECRET_KEY_PREVIOUS is set but KUBEMG_SECRET_KEY is not: " +
	"a rotation moves the stored credentials to a new key, it does not remove encryption")

// ErrPreviousIsCurrent is a rotation to the key already in use: an operator who
// set it believes the key was changed, and it was not.
var ErrPreviousIsCurrent = errors.New("KUBEMG_SECRET_KEY_PREVIOUS is the same key as KUBEMG_SECRET_KEY, " +
	"so nothing would be rotated: set KUBEMG_SECRET_KEY to the new key")

// DecodeKey reads a 32-byte key written as hex or base64. Empty input is no key
// (nil, nil). Anything else — including a passphrase, which would need a KDF
// and a stored salt — is refused.
func DecodeKey(raw string) ([]byte, error) {
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
	return nil, fmt.Errorf("key must be %d bytes as hex or base64 (generate one with: openssl rand -base64 %d)",
		KeySize, KeySize)
}

// Box seals and opens values. A nil *Box is valid and means "no key": Seal
// passes plaintext through and Open refuses ciphertext.
type Box struct {
	aead cipher.AEAD
	// previous is the key being rotated away from. It opens, it never seals.
	previous cipher.AEAD
}

// New builds a Box from a raw key. An empty key returns a nil Box, which is
// the documented no-encryption state.
func New(key []byte) (*Box, error) {
	if len(key) == 0 {
		return nil, nil
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// NewRotating builds a Box that seals under key and also opens what previous
// sealed. An empty previous is New(key). A previous with no key, or the same
// key twice, is refused rather than read as "no rotation".
func NewRotating(key, previous []byte) (*Box, error) {
	if len(previous) == 0 {
		return New(key)
	}
	if len(key) == 0 {
		return nil, ErrPreviousWithoutKey
	}
	if bytes.Equal(key, previous) {
		return nil, ErrPreviousIsCurrent
	}
	box, err := New(key)
	if err != nil {
		return nil, err
	}
	if box.previous, err = newAEAD(previous); err != nil {
		return nil, fmt.Errorf("previous %w", err)
	}
	return box, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Parse is DecodeKey followed by New.
func Parse(raw string) (*Box, error) {
	return ParseRotating(raw, "")
}

// ParseRotating is DecodeKey on both keys followed by NewRotating.
func ParseRotating(raw, rawPrevious string) (*Box, error) {
	key, err := DecodeKey(raw)
	if err != nil {
		return nil, err
	}
	previous, err := DecodeKey(rawPrevious)
	if err != nil {
		return nil, fmt.Errorf("previous %w", err)
	}
	return NewRotating(key, previous)
}

// Enabled reports whether values sealed now are encrypted.
func (b *Box) Enabled() bool { return b != nil }

// Rotating reports whether a previous key is configured.
func (b *Box) Rotating() bool { return b != nil && b.previous != nil }

// IsSealed reports whether a stored value carries the encrypted prefix.
func IsSealed(stored string) bool { return strings.HasPrefix(stored, Prefix) }

// Seal encrypts a value. The empty string stays empty — "no credential" must
// read back as no credential, not as the encryption of nothing — and with no
// key the value is returned as given.
func (b *Box) Seal(plaintext string) (string, error) {
	if plaintext == "" || b == nil {
		return plaintext, nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secretbox nonce: %w", err)
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), []byte(Prefix))
	return Prefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a stored value. A value without the prefix is plaintext written
// before a key was configured and is returned as is; a sealed value with no key,
// or one that fails to authenticate, is an error — never returned as if it were
// the credential.
func (b *Box) Open(stored string) (string, error) {
	plain, _, err := b.OpenRotated(stored)
	return plain, err
}

// OpenRotated is Open that also reports whether the value was sealed under the
// previous key — a value the boot pass has still to re-seal.
func (b *Box) OpenRotated(stored string) (plain string, underPrevious bool, err error) {
	if !IsSealed(stored) {
		return stored, false, nil
	}
	if b == nil {
		return "", false, ErrKeyRequired
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(stored, Prefix))
	if err != nil {
		return "", false, ErrKeyMismatch
	}
	if opened, ok := openWith(b.aead, raw); ok {
		return opened, false, nil
	}
	if b.previous != nil {
		if opened, ok := openWith(b.previous, raw); ok {
			return opened, true, nil
		}
	}
	return "", false, ErrKeyMismatch
}

func openWith(aead cipher.AEAD, raw []byte) (string, bool) {
	size := aead.NonceSize()
	if len(raw) < size+aead.Overhead() {
		return "", false
	}
	plain, err := aead.Open(nil, raw[:size], raw[size:], []byte(Prefix))
	if err != nil {
		return "", false
	}
	return string(plain), true
}
