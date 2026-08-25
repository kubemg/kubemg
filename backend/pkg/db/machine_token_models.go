package db

import "time"

// MachineToken is the long-lived credential a machine account authenticates
// with: one row per issued token, bound to one cluster.
//
// It exists because the other credential in this product cannot do this job. A
// generated kubeconfig carries a proxy-scoped JWT, which is fine for a file on a
// laptop that expires within the day — but a JWT is stateless, so a credential a
// pipeline holds for months could not be withdrawn before its own expiry, and
// "disable the whole account" is the wrong-sized lever when one build agent's
// secret store leaks. A stored token is a row: revoking it is a write, and it
// takes effect on the next call.
//
// Only the hash is kept. The secret is 256 bits of CSPRNG output, so there is
// nothing to guess and a password KDF would only put its work factor in front of
// every proxied call; SHA-256 is the right primitive for a high-entropy secret
// looked up on the hot path.
type MachineToken struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// UserID is the machine account this token speaks for. It is what the whole
	// access model keys on: the grant, the namespace scope and the impersonated
	// identity the target cluster sees are all this account's, not the token's.
	UserID uint `gorm:"index;not null" json:"user_id"`
	// ClusterID is the one cluster this token may address. It is carried in the
	// claims the verifier builds, so the same rule that confines a generated
	// kubeconfig to one cluster's proxy route confines this.
	ClusterID uint `gorm:"index;not null" json:"cluster_id"`
	// Name is what an operator wrote on it — "jenkins release", "argo sync" — so
	// a list of live credentials is readable as a list of systems.
	Name string `gorm:"size:120;not null" json:"name"`
	// Namespace is the kubeconfig's default context namespace. It is a
	// convenience, never a boundary: the boundary is the account's grant.
	Namespace string `gorm:"size:190" json:"namespace,omitempty"`

	TokenHash string `gorm:"size:64;uniqueIndex;not null" json:"-"`
	// Hint is the token's own opening characters. A CI system holds the secret
	// and this console holds a row; without something in common, deciding which
	// row to revoke means guessing.
	Hint string `gorm:"size:24;not null" json:"hint"`

	// ExpiresAt nil is a token with no expiry. That is allowed deliberately — a
	// release pipeline that stops working at 3am on a quarter boundary is an
	// outage nobody scheduled — and it is why LastUsedAt exists: what replaces
	// the expiry as a control is being able to see which credentials are still
	// being used and which were abandoned.
	ExpiresAt *time.Time `gorm:"index" json:"expires_at,omitempty"`
	// RevokedAt closes a token without deleting the row, because "what existed
	// and when did it stop" is the question an auditor asks about a credential.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	// LastUsedAt is written at most once per touchInterval rather than per call:
	// this is the hot path, and knowing a token was used in the last few minutes
	// is worth as much as knowing the second.
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`

	CreatedBy uint      `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// Revoked reports whether an administrator has withdrawn this token.
func (t MachineToken) Revoked() bool { return t.RevokedAt != nil }

// Expired reports whether the token's own window has run out. A token with no
// expiry never does.
func (t MachineToken) Expired(now time.Time) bool {
	return t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)
}

// Usable reports whether this token still authenticates. It says nothing about
// what it may reach — that is the account's grant, re-read on every call.
func (t MachineToken) Usable(now time.Time) bool {
	return !t.Revoked() && !t.Expired(now)
}
