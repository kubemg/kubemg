package db

import "time"

// Device-code states. A request starts pending, an approval or a denial in
// the console settles it, and the CLI's next poll spends it.
const (
	CLIDevicePending  = "pending"
	CLIDeviceApproved = "approved"
	CLIDeviceDenied   = "denied"
)

// CLIDeviceCode is one `kubemg login` waiting for somebody to approve it in the
// browser (RFC 8628's device authorization grant).
//
// The device code is what the CLI polls with and only its SHA-256 is kept; the
// user code is what a person types or follows, and it is stored as typed
// because it is short, single-use, bound to a ten-minute window and worthless
// without a signed-in session to approve it with. A row rather than process
// memory for the ws_tickets reason: the CLI's poll and the browser's approval
// need not reach the same replica.
type CLIDeviceCode struct {
	DeviceCodeHash string `gorm:"primaryKey;size:64"`
	UserCode       string `gorm:"size:16;uniqueIndex;not null"`
	ClientName     string `gorm:"size:64"`
	ClientHost     string `gorm:"size:128"`
	ClientOS       string `gorm:"size:64"`
	ClientVersion  string `gorm:"size:64"`
	// SourceIP is where `kubemg login` ran from, as the server saw it. It is
	// what the approval page shows beside the request, because a request that
	// did not come from you is the device flow's one real attack.
	SourceIP string `gorm:"size:64;index"`
	Status   string `gorm:"size:16;not null"`
	// UserID is set by the approval: the session the CLI receives belongs to
	// whoever approved, never to anyone the request named.
	UserID       uint
	LastPolledAt *time.Time
	ExpiresAt    time.Time `gorm:"index;not null"`
	CreatedAt    time.Time
}

// TableName pins the table name.
func (CLIDeviceCode) TableName() string { return "cli_device_codes" }

// CLISession is one signed-in `kubemg` CLI: the register row for the
// credential a laptop holds instead of a kubeconfig file.
//
// The CLI holds a refresh token; only its SHA-256 is stored, so the session is
// revocable on its next call the way a machine token is and a stateless one
// could not be. Every short-lived proxy token minted under it carries the
// session's ID as its `jti`, so the published revocation snapshot that already
// withdraws kubeconfigs withdraws a whole session too — the gateway needs no
// second question.
type CLISession struct {
	ID          string `gorm:"primaryKey;size:64" json:"id"`
	UserID      uint   `gorm:"index;not null" json:"user_id"`
	Username    string `gorm:"size:190" json:"username"`
	RefreshHash string `gorm:"size:64;uniqueIndex;not null" json:"-"`

	ClientName    string `gorm:"size:64" json:"client_name,omitempty"`
	ClientHost    string `gorm:"size:128" json:"client_host,omitempty"`
	ClientOS      string `gorm:"size:64" json:"client_os,omitempty"`
	ClientVersion string `gorm:"size:64" json:"client_version,omitempty"`
	SourceIP      string `gorm:"size:64" json:"source_ip,omitempty"`

	ExpiresAt     time.Time  `gorm:"index" json:"expires_at"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	RevokedBy     uint       `json:"revoked_by,omitempty"`
	RevokedByName string     `gorm:"size:190" json:"revoked_by_username,omitempty"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
	CreatedAt     time.Time  `gorm:"index" json:"created_at"`
}

// TableName pins the table name.
func (CLISession) TableName() string { return "cli_sessions" }

// Live reports whether the session can still mint credentials.
func (s CLISession) Live(now time.Time) bool {
	return s.RevokedAt == nil && now.Before(s.ExpiresAt)
}

// Status is the row's own reading, in the kubeconfig register's words.
func (s CLISession) Status(now time.Time) string {
	switch {
	case s.RevokedAt != nil:
		return "revoked"
	case !now.Before(s.ExpiresAt):
		return "expired"
	}
	return "active"
}

// CLISessionFilter narrows a read of the sessions.
type CLISessionFilter struct {
	UserID     uint
	ActiveOnly bool
	Now        time.Time
	Limit      int
	Offset     int
}
