package db

import "time"

// The kinds of second factor an account can hold.
const (
	// MFAKindTOTP is an authenticator-app factor (RFC 6238).
	MFAKindTOTP = "totp"
	// MFAKindWebAuthn is a security key or a passkey (WebAuthn Level 3).
	MFAKindWebAuthn = "webauthn"
)

// MFA challenge purposes. A verify challenge stands between a right password
// and a session; an enroll challenge is what an account that must have a
// factor and has none holds instead of a session — it opens the enrolment
// routes and nothing else.
//
// A webauthn row is one security-key ceremony rather than a sign-in: the
// challenge the key signs, minted when the browser asks for it and spent by the
// first answer, right or wrong — WebAuthn's challenge is single-use, unlike a
// sign-in's.
const (
	MFAChallengeVerify   = "verify"
	MFAChallengeEnroll   = "enroll"
	MFAChallengeWebAuthn = "webauthn"
)

// MFAFactor is one second factor an account holds.
//
// A factor starts unconfirmed: the secret is shown, the person types a code the
// app produced, and only then is ConfirmedAt set. An unconfirmed factor counts
// for nothing — sign-in ignores it — and is replaced by the account's next
// enrolment or pruned after fifteen minutes, so a QR code shown and abandoned
// does not linger as a usable secret.
type MFAFactor struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	UserID uint   `gorm:"index;not null" json:"-"`
	Kind   string `gorm:"size:16;not null" json:"kind"`
	// Name is the person's own label — "work phone" — so a list of two factors
	// says which one to remove.
	Name string `gorm:"size:64" json:"name"`
	// Secret is the TOTP shared secret. It is read back on every sign-in, which
	// is what puts it under serializer:secret: a database dump must not be a
	// copy of everybody's authenticator.
	//
	// For a security key it is the credential record as JSON — a public key,
	// which is not secret, sealed anyway because it costs nothing — and while
	// the enrolment is pending, the registration ceremony's state.
	Secret string `gorm:"type:text;serializer:secret" json:"-"`
	// LastStep is the last TOTP time step accepted, or for a security key the
	// last signature counter. A value at or before it is refused, and it is
	// advanced by a conditional update so two replicas presented the same code
	// cannot both accept it.
	LastStep int64 `gorm:"not null;default:0" json:"-"`
	// RPID is the host a security key was registered for — the public URL's at
	// the time. A key answers only to that host, so one registered before the
	// public URL changed is still listed but no longer offered at sign-in.
	RPID        string     `gorm:"column:rp_id;size:253" json:"rp_id,omitempty"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// TableName pins the table name.
func (MFAFactor) TableName() string { return "user_mfa_factors" }

// MFARecoveryCode is one single-use recovery code, stored as its SHA-256.
type MFARecoveryCode struct {
	ID        uint       `gorm:"primaryKey"`
	UserID    uint       `gorm:"index;not null"`
	CodeHash  string     `gorm:"size:64;not null"`
	UsedAt    *time.Time
	CreatedAt time.Time
}

// TableName pins the table name.
func (MFARecoveryCode) TableName() string { return "user_mfa_recovery_codes" }

// MFAChallenge is the half-finished sign-in between a right password and a
// session. It is a row rather than process memory for the ws_tickets reason:
// the password and the code are two requests, and behind a load balancer they
// need not reach the same replica. Only the SHA-256 of the challenge is kept.
type MFAChallenge struct {
	TokenHash string    `gorm:"primaryKey;size:64"`
	UserID    uint      `gorm:"not null"`
	Purpose   string    `gorm:"size:16;not null"`
	ExpiresAt time.Time `gorm:"index;not null"`
	// Ceremony is a webauthn row's ceremony state: the challenge the key must
	// sign and the credentials it may sign with. Empty for every other purpose.
	Ceremony string `gorm:"type:text"`
}

// TableName pins the table name.
func (MFAChallenge) TableName() string { return "mfa_challenges" }

// MFAState is what one account's second factor looks like from outside: what it
// holds and how many recovery codes are left. It never carries a secret.
type MFAState struct {
	Factors                []MFAFactor `json:"factors"`
	RecoveryCodesRemaining int64       `json:"recovery_codes_remaining"`
}

// Enrolled reports whether the account has a confirmed factor.
func (s MFAState) Enrolled() bool { return len(s.Factors) > 0 }

// MFAApplies reports whether KubeMG's own second factor governs an account.
//
// A local account, obviously. An LDAP account too: a directory bind checks a
// password and nothing else, so it is exactly as exposed as a local one. An
// OIDC or SAML account is not — its second factor is the identity provider's,
// and asking again here is friction that protects nothing. A machine account
// holds no password at all.
func (u User) MFAApplies() bool {
	if u.IsMachine() {
		return false
	}
	return !u.IsFederated() || u.AuthSource == ProtocolLDAP
}
