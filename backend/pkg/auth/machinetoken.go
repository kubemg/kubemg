package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// MachineTokenPrefix marks a machine account's credential. It is on the wire
// for two reasons: this process can tell in one comparison which of the two
// credential shapes it was handed, without attempting a JWT parse on something
// that was never a JWT — and a secret scanner, a log filter or an operator
// reading a CI config can recognise a KubeMG credential for what it is.
//
// It is deliberately distinct from the agent registration token's "kmg_"
// (see bastion.NewAgentToken). The two never meet in one check — an agent token
// authenticates the tunnel handshake, outside this middleware entirely — but
// they do meet in a support conversation, and two credentials that look alike
// and are revoked in different places is how the wrong one gets revoked.
const MachineTokenPrefix = "kmgm_"

// machineTokenBytes is the entropy behind the secret. 256 bits is what makes a
// plain hash the right thing to store: there is nothing here to guess, so a
// password KDF would defend nothing and would put its work factor in front of
// every proxied call.
const machineTokenBytes = 32

// machineTokenHintLength is how much of the secret is kept in the clear. It is
// long enough to match a row against a value in a CI secret store and far too
// short to narrow the secret — the whole point of a hint is that revoking the
// right credential must not require guessing.
const machineTokenHintLength = 8

// NewMachineToken mints a credential, returning the secret the caller is handed
// once, the hash that is stored in its place, and the hint that identifies the
// row afterwards.
func NewMachineToken() (secret, hash, hint string, err error) {
	buf := make([]byte, machineTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("generate service token: %w", err)
	}
	secret = MachineTokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return secret, HashMachineToken(secret), machineTokenHint(secret), nil
}

// HashMachineToken renders the stored form of a secret.
func HashMachineToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// EqualMachineTokenHash compares two stored hashes without an early return.
// The lookup itself is by hash and therefore already constant in the secret,
// but a verifier that compares afterwards should not reintroduce a timing
// difference it just avoided.
func EqualMachineTokenHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// IsMachineToken reports whether a presented bearer token is a programmatic
// credential rather than a session JWT.
func IsMachineToken(token string) bool {
	return strings.HasPrefix(token, MachineTokenPrefix)
}

func machineTokenHint(secret string) string {
	if len(secret) <= len(MachineTokenPrefix)+machineTokenHintLength {
		return secret
	}
	return secret[:len(MachineTokenPrefix)+machineTokenHintLength]
}

// MachineTokenVerifier resolves an opaque programmatic credential into the same
// claims a session token would have carried. It is an interface here so that
// pkg/auth keeps knowing nothing about storage: the verifier is a database read
// plus a liveness check, and it belongs beside the store.
//
// The claims it returns are expected to carry ScopeProxy and the token's own
// cluster, so that everything downstream — the proxy route confinement in
// RequireAuth, the grant re-read on every call, the audit record — treats a
// pipeline exactly as it treats an issued kubeconfig.
type MachineTokenVerifier interface {
	VerifyMachineToken(ctx context.Context, secret string) (*Claims, error)
}
