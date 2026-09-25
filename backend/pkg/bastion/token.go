package bastion

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
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

// installTicketPrefix marks an install download ticket. It is deliberately not
// agentTokenPrefix: an install URL must never be mistaken for, or accepted as,
// the tunnel credential, and a path segment that *does* start with the tunnel
// prefix is recognisably a URL from before the two were separated.
const installTicketPrefix = "kmgi_"

// NewInstallTicket mints an install download ticket and the SHA-256 it is
// stored under. The ticket is shown once, in the install URL; the hash is all
// the database keeps.
func NewInstallTicket() (ticket, hash string, err error) {
	buf := make([]byte, agentTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate install ticket: %w", err)
	}
	ticket = installTicketPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return ticket, HashInstallTicket(ticket), nil
}

// HashInstallTicket is the key a ticket is stored and redeemed under.
func HashInstallTicket(ticket string) string {
	sum := sha256.Sum256([]byte(ticket))
	return hex.EncodeToString(sum[:])
}

// LooksLikeAgentToken reports whether a string has the tunnel credential's
// shape. It checks the prefix only — it is how an install URL from before
// download tickets is recognised and refused by name, never how a credential is
// verified.
func LooksLikeAgentToken(value string) bool {
	return strings.HasPrefix(value, agentTokenPrefix)
}
