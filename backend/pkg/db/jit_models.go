package db

import (
	"slices"
	"time"
)

/*
 * Just-in-time elevated access.
 *
 * Everywhere else in KubeMG a grant is a standing fact: somebody was given
 * `edit` on staging and holds it until an administrator takes it away. That is
 * the right shape for the access people use every day and the wrong shape for
 * the access they need twice a quarter — a production `cluster-admin` that
 * exists permanently is a permanent risk for an occasional need, and the
 * pressure to hand it out is exactly what makes standing privilege spread.
 *
 * A JIT request is the other shape: a role, a cluster, a reason, and a clock.
 * Three properties are load-bearing and each one is a decision the schema has to
 * carry rather than something a handler can be trusted to remember:
 *
 *   - The **reason is mandatory and stored**. The value of this workflow is not
 *     the approval click, it is that six months later the trail says why someone
 *     held cluster-admin in prod for an hour.
 *   - The elevation is a **grant of its own**, never an edit of the requester's
 *     standing one. `user_cluster_access` carries a source and an expiry, so an
 *     elevation is a second row that outranks the first while it lives and
 *     vanishes on its own — expiry needs no restore step, and there is no window
 *     in which a user has lost the access they permanently hold.
 *   - Everything is **denormalised**: requester and cluster names are copied in.
 *     A request is a record of a decision, and it has to read correctly after
 *     the account or the cluster it names is gone.
 */

// JIT request statuses.
//
// The set is wider than the transitions this build makes, and deliberately so:
// `approved` and `active` are one event here, because KubeMG activates the grant
// in the same transaction that records the approval. Keeping both means a row
// written by an older or a future build — one that queues an approval for a
// window starting later — still reads as something, and JitStatusApproved is
// treated as live everywhere JitStatusActive is.
const (
	// JitStatusPending is waiting for a decision. It is the only status an
	// approval or a rejection may act on.
	JitStatusPending = "pending"
	// JitStatusApproved is decided but not yet carrying a grant. See above.
	JitStatusApproved = "approved"
	// JitStatusActive is decided and carrying a live grant, until ExpiresAt.
	JitStatusActive = "active"
	// JitStatusRejected is refused by an approver. Terminal.
	JitStatusRejected = "rejected"
	// JitStatusExpired ran its window out. Terminal, and written by the
	// background sweeper rather than by anybody's request.
	JitStatusExpired = "expired"
	// JitStatusRevoked was withdrawn before its window ended, by an
	// administrator or by the requester handing it back. Terminal.
	JitStatusRevoked = "revoked"
)

// JitStatuses enumerates every stored status.
var JitStatuses = []string{
	JitStatusPending,
	JitStatusApproved,
	JitStatusActive,
	JitStatusRejected,
	JitStatusExpired,
	JitStatusRevoked,
}

// JitLiveStatuses are the statuses that mean a grant exists right now. Both are
// listed for the reason given above the status block.
var JitLiveStatuses = []string{JitStatusApproved, JitStatusActive}

// ValidJitStatus reports whether a status is one this build knows.
func ValidJitStatus(status string) bool { return slices.Contains(JitStatuses, status) }

// JIT duration bounds, in minutes.
//
// The floor exists because a window shorter than the work is a second request;
// the ceiling because past a day this is not just-in-time access, it is access,
// and it belongs in the permissions matrix where it is reviewed.
const (
	MinJitDurationMinutes = 5
	MaxJitDurationMinutes = 24 * 60
)

// JitDurationChoices are the windows the console offers. They are stored here
// rather than in the UI so the API can report them: a client that offers a
// duration the server would refuse is a form that fails on submit.
var JitDurationChoices = []int{30, 60, 120, 240, 480}

// JitRequest is one request for time-bound elevated access.
//
// The primary key is a UUID string rather than a sequence, because a request id
// travels: it goes into a Slack message, a signed approval callback and an audit
// record. A guessable id in any of those is an invitation to try the next number
// along.
type JitRequest struct {
	ID string `gorm:"primaryKey;size:36" json:"id"`

	RequesterID uint `gorm:"index;not null" json:"requester_id"`
	// RequesterUsername is copied in so a request still reads after the account
	// is deleted — see the note on denormalisation above.
	RequesterUsername string `gorm:"size:120" json:"requester_username"`

	ClusterID   uint   `gorm:"index;not null" json:"cluster_id"`
	ClusterName string `gorm:"size:120" json:"cluster_name"`

	// RequestedRole is the Kubernetes role being asked for: one of the three in
	// K8sRole*. It is what the activated grant carries, and what the cluster's own
	// RBAC binds through the kubemg: groups.
	RequestedRole string `gorm:"size:60;not null" json:"requested_role"`
	// Namespaces narrows the elevation, comma-separated. Empty means every
	// namespace the role allows, which is the honest default for cluster-admin and
	// the one worth thinking twice about for anything else.
	Namespaces string `gorm:"type:text" json:"-"`

	DurationMinutes int `gorm:"not null" json:"duration_minutes"`
	// Reason is why. Mandatory at the API, and the field that makes this workflow
	// worth having at all.
	Reason string `gorm:"type:text;not null" json:"reason"`

	Status string `gorm:"size:16;not null;index" json:"status"`

	ApproverID       uint       `json:"approver_id,omitempty"`
	ApproverUsername string     `gorm:"size:120" json:"approver_username,omitempty"`
	ApproverComment  string     `gorm:"type:text" json:"approver_comment,omitempty"`
	ApprovedAt       *time.Time `json:"approved_at,omitempty"`
	// ExpiresAt is when the grant stops. It is set at approval rather than at
	// creation: the window an approver grants starts when they grant it, not when
	// somebody asked.
	ExpiresAt *time.Time `gorm:"index" json:"expires_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the request table name.
func (JitRequest) TableName() string { return "jit_requests" }

// NamespaceList splits the stored namespace scope.
func (r JitRequest) NamespaceList() []string { return splitNamespaces(r.Namespaces) }

// Live reports whether this request is carrying a grant right now: a live status
// and a window that has not run out. The status alone is not enough — the sweeper
// runs on a timer, so between the expiry and the next pass the row still says
// active while the resolver has already stopped honouring it.
func (r JitRequest) Live(now time.Time) bool {
	if !slices.Contains(JitLiveStatuses, r.Status) {
		return false
	}
	return r.ExpiresAt == nil || r.ExpiresAt.After(now)
}

// RemainingSeconds is how much of the window is left, zero for anything not
// live. It is computed here rather than in the browser because the countdown has
// to agree with the server that will refuse the call.
func (r JitRequest) RemainingSeconds(now time.Time) int64 {
	if !r.Live(now) || r.ExpiresAt == nil {
		return 0
	}
	remaining := r.ExpiresAt.Sub(now).Seconds()
	if remaining < 0 {
		return 0
	}
	return int64(remaining)
}

// Decided reports whether this request has had its decision made, whatever that
// decision was.
func (r JitRequest) Decided() bool { return r.Status != JitStatusPending }

// JitRequestFilter narrows a listing. A zero value is every request, which is
// what an administrator's inbox asks for.
type JitRequestFilter struct {
	// Statuses keeps only these statuses. Empty means all of them.
	Statuses []string
	// RequesterID narrows to one account. It is what the API sets — not asks —
	// for a non-admin caller: the audit trail's rule applies here too, since a
	// pending request carries somebody's stated reason for needing production.
	RequesterID uint
	ClusterID   uint
	// Limit bounds the page. Zero takes the store's default.
	Limit int
}
