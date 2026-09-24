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
// refuse ciphertext by name instead of handing it to a cluster as a token. The
// version is where a key-rotation scheme goes when one is needed: a v2 can name
// its key without every v1 value having to be rewritten first.
package secretbox

import (
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
}

// New builds a Box from a raw key. An empty key returns a nil Box, which is
// the documented no-encryption state.
func New(key []byte) (*Box, error) {
	if len(key) == 0 {
		return nil, nil
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Parse is DecodeKey followed by New.
func Parse(raw string) (*Box, error) {
	key, err := DecodeKey(raw)
	if err != nil {
		return nil, err
	}
	return New(key)
}

// Enabled reports whether values sealed now are encrypted.
func (b *Box) Enabled() bool { return b != nil }

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
	if !IsSealed(stored) {
		return stored, nil
	}
	if b == nil {
		return "", ErrKeyRequired
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(stored, Prefix))
	if err != nil {
		return "", ErrKeyMismatch
	}
	size := b.aead.NonceSize()
	if len(raw) < size+b.aead.Overhead() {
		return "", ErrKeyMismatch
	}
	plain, err := b.aead.Open(nil, raw[:size], raw[size:], []byte(Prefix))
	if err != nil {
		return "", ErrKeyMismatch
	}
	return string(plain), nil
}
