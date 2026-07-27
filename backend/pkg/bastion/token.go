package bastion

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
)

// agentTokenBytes is the entropy behind a registration token. The token is the
// agent's only credential and it lives in a Kubernetes Secret for the life of
// the installation, so it is sized to be brute-force proof rather than short.
const agentTokenBytes = 32

// agentTokenPrefix makes a leaked token identifiable in a log or a bug report
// without having to guess what it is.
const agentTokenPrefix = "kmg_"

// NewAgentToken mints a cluster registration token.
func NewAgentToken() (string, error) {
	buf := make([]byte, agentTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate agent token: %w", err)
	}
	return agentTokenPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// SameToken compares two tokens without leaking their length relationship
// through timing.
func SameToken(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
