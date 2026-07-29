package db

import "time"

// TerminalSession is one recorded interactive session — a `kubectl exec`, an
// attach, or the console's own in-page terminal. The recording itself is a file
// on disk; this row is what makes it findable, and what an auditor reads before
// deciding whether to watch it.
//
// Fields are denormalised like AuditEvent's, for the same reason: a recording
// has to still say who ran what where after the account or the cluster it names
// has been deleted.
type TerminalSession struct {
	ID uint `gorm:"primaryKey" json:"id"`

	// SessionID ties the recording to the audit records the same session wrote.
	// It is the correlation key rather than an audit row id because the trail is
	// persisted asynchronously in batches — the row this session will occupy has
	// no id yet at the moment the session opens, and waiting for one would put a
	// database write in front of a shell.
	SessionID string `gorm:"size:64;uniqueIndex;not null" json:"session_id"`

	UserID    uint   `gorm:"index" json:"user_id"`
	Username  string `gorm:"size:120;index" json:"username"`
	ClusterID uint   `gorm:"index" json:"cluster_id"`
	Cluster   string `gorm:"size:120" json:"cluster"`

	Namespace     string `gorm:"size:190;index" json:"namespace,omitempty"`
	PodName       string `gorm:"size:253;index" json:"pod_name,omitempty"`
	ContainerName string `gorm:"size:253" json:"container_name,omitempty"`
	// Shell is the argv the caller asked the container to run, joined for
	// display. An attach has none — it joins whatever PID 1 is already running.
	Shell string `gorm:"size:255" json:"shell,omitempty"`
	// Verb is "exec" or "attach", matching the audit trail's own naming.
	Verb string `gorm:"size:20" json:"verb,omitempty"`

	StartedAt time.Time `gorm:"index:idx_terminal_started,sort:desc;not null" json:"started_at"`
	// EndedAt is nil while the session is still open, which is what lets the
	// list show a shell somebody is running right now.
	EndedAt         *time.Time `json:"ended_at,omitempty"`
	DurationSeconds int64      `json:"duration_seconds"`
	// ByteCount is how much of the session was recorded, before compression.
	ByteCount int64 `json:"byte_count"`
	// Truncated marks a session that outgrew the per-recording cap. The
	// recording is still playable; it just stops before the session did.
	Truncated bool `json:"truncated"`

	// StoragePath is where the .cast.gz lives. It is validated against the
	// configured recording directory on the way out, never trusted on its own.
	StoragePath string `gorm:"type:text" json:"-"`

	// Error explains a recording that ended badly — a broken tunnel, or a disk
	// that stopped accepting writes half way through.
	Error string `gorm:"type:text" json:"error,omitempty"`
}

// TableName pins the recording table name.
func (TerminalSession) TableName() string { return "terminal_sessions" }

// IsOpen reports whether the session is still running.
func (s TerminalSession) IsOpen() bool { return s.EndedAt == nil }
