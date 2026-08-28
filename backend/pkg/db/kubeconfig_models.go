package db

import "time"

// KubeconfigIssuance is one row per kubeconfig this console has handed out.
//
// It exists because generating a kubeconfig was the one act in this product
// that gave somebody a credential and wrote nothing down. A machine token has
// had a row since it existed — revoking one is a write — while the *human*
// credential, the file that leaves the building on a laptop, could only be
// stopped by disabling the account or revoking the grant, which are the levers
// nobody pulls for the case that actually happens: the person still works here
// and still needs their console.
//
// The register is the half that has to exist first. Revocation is what everyone
// notices missing, but you cannot withdraw what you never recorded, and "who
// holds access to production right now, and since when" is a question a row
// answers and an audit record only half answers.
type KubeconfigIssuance struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// TokenID is the credential's own identity. In agent mode it is the `jti` in
	// the proxy-scoped JWT, which is what makes a revocation something the token
	// cannot argue with: the gateway matches this against a published set.
	//
	// A direct-mode row carries one too, even though nothing in the issued file
	// mentions it. That is deliberate rather than tidy-minded: the column is the
	// row's identity everywhere else in this feature, and a nullable one would
	// make every read branch on a mode it does not otherwise care about. What a
	// direct-mode row cannot do is be revoked — see RevocableHere.
	TokenID string `gorm:"size:64;uniqueIndex;not null" json:"token_id"`

	// UserID is who holds the credential; Username is kept beside it so a row
	// still reads after the account is deleted, the audit table's rule.
	UserID   uint   `gorm:"index;not null" json:"user_id"`
	Username string `gorm:"size:190" json:"username"`

	ClusterID   uint   `gorm:"index;not null" json:"cluster_id"`
	ClusterName string `gorm:"size:190" json:"cluster_name"`
	// ConnectionMode is what the file actually is — ModeAgent means a KubeMG
	// token pointed at KubeMG's proxy, ModeDirect means a service account token
	// the cluster itself minted. The two are revocable by completely different
	// means, and the register has to say which it is holding.
	ConnectionMode string `gorm:"size:16;not null" json:"connection_mode"`

	Namespace string `gorm:"size:190" json:"namespace,omitempty"`
	K8sRole   string `gorm:"size:32" json:"k8s_role,omitempty"`
	// ServiceAccount is the in-cluster identity a direct-mode token authenticates
	// as. It is the one thing that makes the direct-mode disclosure actionable:
	// deleting that account is the only lever that withdraws the token.
	ServiceAccount string `gorm:"size:190" json:"service_account,omitempty"`

	// Purpose says what the credential was made for. It is empty for the ordinary
	// case — somebody downloaded a kubeconfig — and KubeconfigPurposeShell for the
	// one that is never downloaded: the credential written into a browser shell's
	// pod. The register would otherwise show a row per shell with nothing to
	// distinguish it from a file on a laptop, and the two are revoked for very
	// different reasons.
	Purpose string `gorm:"size:16" json:"purpose,omitempty"`

	// IssuedBy is whoever asked for the file, which is not always whoever holds
	// it — an administrator generating a kubeconfig for somebody else is exactly
	// the row an auditor is looking for.
	IssuedBy         uint   `json:"issued_by"`
	IssuedByUsername string `gorm:"size:190" json:"issued_by_username,omitempty"`

	ExpiresAt time.Time `gorm:"index" json:"expires_at"`
	// RevokedAt closes a credential without deleting the row: "what existed and
	// when did it stop" is the question, and a deleted row answers neither half.
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
	RevokedBy       uint       `json:"revoked_by,omitempty"`
	RevokedByName   string     `gorm:"size:190" json:"revoked_by_username,omitempty"`
	// LastUsedAt is written off the request's own path at most once every few
	// minutes — see pkg/credentials. This read would otherwise sit in front of
	// every proxied call.
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`

	CreatedAt time.Time `gorm:"index" json:"created_at"`
}

// KubeconfigPurposeShell marks the credential seeded into a browser shell pod.
const KubeconfigPurposeShell = "shell"

// Revoked reports whether this credential has been withdrawn.
func (k KubeconfigIssuance) Revoked() bool { return k.RevokedAt != nil }

// Expired reports whether the credential's own window has run out.
func (k KubeconfigIssuance) Expired(now time.Time) bool {
	return !k.ExpiresAt.IsZero() && !now.Before(k.ExpiresAt)
}

// Status is the row's own reading, so a list does not make every client
// re-derive it from two nullable columns and a clock.
func (k KubeconfigIssuance) Status(now time.Time) string {
	switch {
	case k.Revoked():
		return "revoked"
	case k.Expired(now):
		return "expired"
	default:
		return "active"
	}
}

// RevocableHere reports whether revoking this row actually stops the credential.
//
// Only agent mode can answer yes. In direct mode the token is the cluster's,
// minted through TokenRequest, and it stays valid until it expires however this
// console feels about it — the one real lever is deleting the per-user service
// account on the cluster, which is all-or-nothing for that user on that cluster
// because every one of their direct-mode kubeconfigs is bound to the same
// account. Saying so is not a caveat to bury: an administrator who believes a
// revoke landed when it did not is in a worse position than one who knows the
// token has four hours left to run.
func (k KubeconfigIssuance) RevocableHere() bool { return k.ConnectionMode == ModeAgent }
