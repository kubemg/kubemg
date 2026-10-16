package db

import "time"

// MFAKindTOTP is an authenticator-app factor (RFC 6238).
const MFAKindTOTP = "totp"

// MFA challenge purposes. A verify challenge stands between a right password
// and a session; an enroll challenge is what an account that must have a
// factor and has none holds instead of a session — it opens the enrolment
// routes and nothing else.
const (
	MFAChallengeVerify = "verify"
	MFAChallengeEnroll = "enroll"
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
	Secret string `gorm:"type:text;serializer:secret" json:"-"`
	// LastStep is the last TOTP time step accepted. A code at or before it is
	// refused, and it is advanced by a conditional update so two replicas
	// presented the same code cannot both accept it.
	LastStep    int64      `gorm:"not null;default:0" json:"-"`
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
