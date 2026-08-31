package db

import (
	"slices"
	"strings"
	"time"
)

// User roles inside KubeMG itself (not Kubernetes RBAC). These are the values
// carried in the JWT and checked by the role middleware.
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// System roles are the administrative tier shown in the IAM UI. SuperAdmin and
// Admin both map onto the legacy RoleAdmin privilege; the distinction is that a
// SuperAdmin cannot be demoted or deleted by an Admin.
const (
	SystemRoleSuperAdmin = "superadmin"
	SystemRoleAdmin      = "admin"
	SystemRoleUser       = "user"
)

// Account types separate a person from a programmatic caller — a CI pipeline's
// release stage, a release bot, a controller somebody points at KubeMG.
const (
	AccountTypeUser    = "user"
	AccountTypeMachine = "machine"
)

// SystemRoles enumerates the assignable system roles.
var SystemRoles = []string{SystemRoleSuperAdmin, SystemRoleAdmin, SystemRoleUser}

// LegacyRoleFor maps a system role onto the coarse role the JWT and the role
// middleware understand.
func LegacyRoleFor(systemRole string) string {
	switch systemRole {
	case SystemRoleSuperAdmin, SystemRoleAdmin:
		return RoleAdmin
	default:
		return RoleUser
	}
}

// ValidSystemRole reports whether a system role is assignable.
func ValidSystemRole(systemRole string) bool {
	return slices.Contains(SystemRoles, systemRole)
}

// Cluster environments.
const (
	EnvProd    = "prod"
	EnvStaging = "staging"
	EnvDev     = "dev"
)

// Kubernetes roles granted through UserClusterAccess.
const (
	K8sRoleClusterAdmin = "cluster-admin"
	K8sRoleEdit         = "edit"
	K8sRoleView         = "view"
)

// Cluster connection states.
const (
	StatusPending   = "pending"
	StatusHealthy   = "healthy"
	StatusUnhealthy = "unhealthy"
)

// Cluster connection modes: how KubeMG reaches the target API server.
const (
	// ModeAgent routes every request down a reverse tunnel held open by an
	// in-cluster agent. Nothing inbound is opened and no cluster credential is
	// stored here.
	ModeAgent = "agent"
	// ModeDirect dials the API server from KubeMG using a stored service
	// account token. This is the Phase 1 path.
	ModeDirect = "direct"
)

// ConnectionModes enumerates the assignable connection modes.
var ConnectionModes = []string{ModeAgent, ModeDirect}

// ValidConnectionMode reports whether a connection mode is assignable.
func ValidConnectionMode(mode string) bool {
	return slices.Contains(ConnectionModes, mode)
}

// User is a local KubeMG account.
type User struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Username string `gorm:"size:120;uniqueIndex;not null" json:"username"`
	// Email is optional; local accounts are keyed by username.
	Email        string `gorm:"size:190" json:"email,omitempty"`
	PasswordHash string `gorm:"size:120;not null" json:"-"`
	// Role is the coarse privilege carried in the JWT. It is derived from
	// SystemRole and kept in sync by Normalize.
	Role string `gorm:"size:20;not null;default:user" json:"role"`
	// SystemRole is the administrative tier surfaced in the IAM UI.
	SystemRole string `gorm:"size:20;not null;default:user" json:"system_role"`
	// IsActive gates sign-in without destroying the account or its grants.
	IsActive bool `gorm:"not null;default:true" json:"is_active"`

	// AccountType says whether this row is a person or a machine. A machine
	// account is a User row on purpose rather than a principal of its own: every
	// grant, every namespace scope, the permissions matrix, the audit trail and
	// the proxy's own impersonation are keyed on a user id, and a second kind of
	// principal would mean teaching all of them a second shape for no gain — a
	// Jenkins job needs exactly the access model a developer needs.
	//
	// Only two things differ, and both are enforced here rather than left to a
	// handler. It never authenticates with a password — it holds no hash and
	// login refuses it — and it can never be an administrator: Normalize pins its
	// system role to SystemRoleUser, so a row edited by hand cannot smuggle admin
	// onto a credential that lives in a CI secret store.
	AccountType string `gorm:"size:20;not null;default:user" json:"account_type"`

	// CanViewRecordings lets an administrator replay *other people's* terminal
	// recordings. It is a capability of its own rather than part of the admin
	// role because a recording is the most invasive thing this product stores —
	// it holds everything that crossed a production shell — and "may administer
	// KubeMG" is not the same claim as "may watch what a colleague typed". An
	// auditor asks who could see those files, and the honest answer has to be a
	// short list rather than "every admin".
	//
	// It only ever *widens* what an admin sees. Everyone may replay their own
	// sessions without it, and it grants a non-admin nothing: reading the fleet's
	// recordings needs both. A super admin has it implicitly, because the account
	// that can grant it can already take it.
	CanViewRecordings bool `gorm:"not null;default:false" json:"can_view_recordings"`

	// CanRevealSecrets lets an account read one Secret value through the
	// console. It is the CanViewRecordings shape for the same reason: a Secret's
	// value is the one thing this product has always refused to put in a
	// response, and "may administer KubeMG" is not the same claim as "may read
	// the database password". An auditor asks who could, and the answer has to
	// be a short list.
	//
	// Two differences from the recording capability, both deliberate. It does
	// **not** require the admin role, because the object belongs to the cluster
	// rather than to KubeMG: the impersonated read is still answered by the
	// cluster's own RBAC, so a developer who may `kubectl get secret` in their
	// namespace is exactly who this is for, and requiring admin as well would
	// leave them dropping to a terminal — which is the untraced reveal this
	// exists to replace. And it is a capability rather than a role because a
	// role is coarse: granting edit on a namespace should not silently grant
	// "read every credential in it through a web page".
	//
	// A super admin holds it implicitly, because the account that may grant it
	// can grant it to itself; pretending otherwise would be theatre.
	CanRevealSecrets bool `gorm:"not null;default:false" json:"can_reveal_secrets"`

	// AuthSource is where this account's credentials live: AuthSourceLocal for a
	// bcrypt hash in this database, or a federation protocol for an account an
	// identity provider vouches for. A federated account has no usable password,
	// so password sign-in is refused for it rather than merely failing.
	AuthSource string `gorm:"size:20;not null;default:local" json:"auth_source"`
	// SSOProviderID is the provider that vouches for a federated account.
	SSOProviderID uint `gorm:"column:sso_provider_id;index" json:"sso_provider_id,omitempty"`
	// ExternalID is the directory's own stable identifier — an OIDC subject, a
	// SAML NameID, an LDAP DN. It is what an account is matched on, because a
	// username is a display detail a directory is entitled to change.
	ExternalID string `gorm:"size:255;index" json:"external_id,omitempty"`

	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// IsFederated reports whether this account is authenticated by an external
// directory rather than by a password stored here.
func (u User) IsFederated() bool { return IsFederatedSource(u.AuthSource) }

// Normalize fills in a missing system role from the legacy role and re-derives
// the legacy role, so the two columns can never drift apart.
func (u *User) Normalize() {
	if u.SystemRole == "" {
		u.SystemRole = u.Role
	}
	if !ValidSystemRole(u.SystemRole) {
		u.SystemRole = SystemRoleUser
	}
	// A machine identity is never an administrator. This is here rather than in
	// the handler that creates one because the credential outlives the request
	// that made it: a token in a pipeline's secret store is replayed for months,
	// and "which row said admin" is not a question anybody re-asks.
	if u.IsMachine() {
		u.SystemRole = SystemRoleUser
	}
	u.Role = LegacyRoleFor(u.SystemRole)
}

// IsMachine reports whether this account is a programmatic caller rather than a
// person. A row written before machine accounts existed carries an empty type
// and is a person, which is what the default reads as.
func (u User) IsMachine() bool { return u.AccountType == AccountTypeMachine }

// IsAdmin reports whether the user holds the KubeMG admin privilege.
func (u User) IsAdmin() bool {
	return u.Role == RoleAdmin || u.SystemRole == SystemRoleAdmin || u.SystemRole == SystemRoleSuperAdmin
}

// MayViewAllRecordings reports whether this account may replay other people's
// terminal recordings. Own sessions are always readable and are not decided
// here.
func (u User) MayViewAllRecordings() bool {
	if !u.IsAdmin() {
		return false
	}
	return u.IsSuperAdmin() || u.CanViewRecordings
}

// MayRevealSecrets reports whether this account may read a Secret's value
// through the console. Unlike MayViewAllRecordings this does not require the
// admin role: the cluster's own RBAC is the other half of the answer, and it is
// the half that knows which secrets this identity may read at all.
func (u User) MayRevealSecrets() bool {
	return u.IsSuperAdmin() || u.CanRevealSecrets
}

// IsSuperAdmin reports whether the user is protected from administrative edits
// by other admins.
func (u User) IsSuperAdmin() bool { return u.SystemRole == SystemRoleSuperAdmin }

// Group is a local collection of users that cluster access can be granted to.
type Group struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:120;uniqueIndex;not null" json:"name"`
	Description string    `gorm:"type:text" json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// UserGroup maps a user into a local group.
type UserGroup struct {
	UserID  uint `gorm:"primaryKey" json:"user_id"`
	GroupID uint `gorm:"primaryKey" json:"group_id"`
	// Source records who wrote this membership: an administrator, or the
	// federation sync deriving it from an IdP group. Only the derived ones are
	// reconciled away when the directory stops asserting that group, so a
	// hand-written membership is never lost to someone else's login. Empty
	// predates federation and therefore means local.
	Source    string    `gorm:"size:20;not null;default:local" json:"source,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the membership table name.
func (UserGroup) TableName() string { return "user_groups" }

// GroupClusterAccess maps a local group onto a cluster with a Kubernetes role
// and an optional namespace scope. Members inherit it.
type GroupClusterAccess struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	GroupID    uint   `gorm:"uniqueIndex:idx_group_cluster;not null" json:"group_id"`
	ClusterID  uint   `gorm:"uniqueIndex:idx_group_cluster;not null" json:"cluster_id"`
	K8sRole    string `gorm:"column:k8s_role;size:60;not null;default:view" json:"k8s_role"`
	Namespaces string `gorm:"type:text" json:"-"`
}

// TableName pins the join table name; GORM would otherwise pluralize it to
// "group_cluster_accesses".
func (GroupClusterAccess) TableName() string { return "group_cluster_access" }

// NamespaceList splits the stored namespace scope into a slice.
func (a GroupClusterAccess) NamespaceList() []string { return splitNamespaces(a.Namespaces) }

// Cluster is a registered target Kubernetes cluster.
type Cluster struct {
	ID          uint   `gorm:"primaryKey" json:"id"`
	Name        string `gorm:"size:120;uniqueIndex;not null" json:"name"`
	Environment string `gorm:"size:20;not null;default:dev" json:"environment"`
	// ShortName is what the rail's chip says. It is stored rather than derived
	// because a derived abbreviation cannot be built into muscle memory: the
	// rail truncates `prod-eu-west-1` and `prod-eu-west-2` to the same three
	// letters, and at eleven clusters that is a row of guesses. Empty means "no
	// operator has chosen one", which the console renders by falling back to the
	// same derivation it always used — so a fleet registered before this field
	// existed looks exactly as it did.
	//
	// It is deliberately **not** unique. Two clusters sharing a chip is an
	// operator's mistake to see and fix, and a uniqueness constraint here would
	// refuse a registration over a label, which is the wrong thing to fail on.
	ShortName string `gorm:"size:4" json:"short_name,omitempty"`
	// Description is free text an operator writes at registration time, so the
	// fleet list can say what a cluster is for.
	Description         string `gorm:"type:text" json:"description,omitempty"`
	APIURL              string `gorm:"column:api_url;size:255" json:"api_url"`
	CACertData          string `gorm:"column:ca_cert_data;type:text" json:"-"`
	ServiceAccountToken string `gorm:"column:service_account_token;type:text" json:"-"`
	Status              string `gorm:"size:20;not null;default:pending" json:"status"`
	// StatusMessage explains a non-healthy status, so operators do not have to
	// guess why a cluster is unreachable.
	StatusMessage string `gorm:"type:text" json:"status_message,omitempty"`
	// KubernetesVersion is what the API server reported at the last check.
	KubernetesVersion string     `gorm:"size:40" json:"kubernetes_version,omitempty"`
	LastCheckedAt     *time.Time `json:"last_checked_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`

	// ConnectionMode is how KubeMG reaches this cluster: ModeAgent or ModeDirect.
	ConnectionMode string `gorm:"size:20;not null;default:direct" json:"connection_mode"`
	// AgentToken is the registration secret the in-cluster agent presents when
	// it dials the bastion. It is the only credential the agent ever holds, so
	// it is treated like the service account token: stored, never serialized.
	AgentToken string `gorm:"column:agent_token;size:120;index" json:"-"`
	// AgentVersion is what the agent reported in its last handshake.
	AgentVersion string `gorm:"size:40" json:"agent_version,omitempty"`
	// AgentConnectedAt is when the current tunnel was established. It is cleared
	// on disconnect, so a nil value means "no agent is attached right now".
	AgentConnectedAt *time.Time `json:"agent_connected_at,omitempty"`
}

// UsesAgent reports whether this cluster is reached through a tunnel rather
// than by dialling its API server.
func (c Cluster) UsesAgent() bool { return c.ConnectionMode == ModeAgent }

// MaxShortNameLen is how many characters the rail's chip can hold at the size
// it is drawn. Four is the honest ceiling: `MDE1` fits a 40px square in mono at
// 10.5px, a fifth character does not, and a chip that overflows is the defect
// this field exists to fix rather than one to introduce.
const MaxShortNameLen = 4

// NormalizeShortName folds a chip label to the one shape the rail can draw:
// upper case, letters and digits only, at most MaxShortNameLen of them.
//
// It normalizes rather than refuses, because every input this rejects is one a
// person meant something reasonable by — `eu-1` is `EU1`, and refusing it over
// a hyphen would be pedantry about a label. The one thing it cannot do is
// invent characters, so an input with nothing usable in it comes back empty,
// and empty means "no chip was chosen" rather than "a blank chip was".
func NormalizeShortName(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(raw)) {
		if b.Len() == MaxShortNameLen {
			break
		}
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// AuditEvent is one proxied Kubernetes API call, persisted. The bastion also
// emits these as structured logs; the table is what makes them queryable, and
// what an auditor asking "what did this person do in prod last Tuesday" needs.
//
// Fields are denormalised on purpose: an audit record must still read correctly
// after the user or the cluster it names has been deleted.
type AuditEvent struct {
	ID uint      `gorm:"primaryKey" json:"id"`
	At time.Time `gorm:"index:idx_audit_at,sort:desc;not null" json:"at"`

	UserID    uint   `gorm:"index" json:"user_id"`
	Username  string `gorm:"size:120;index" json:"username"`
	ClusterID uint   `gorm:"index" json:"cluster_id"`
	Cluster   string `gorm:"size:120" json:"cluster"`

	// Verb is the Kubernetes verb ("get", "list", "watch", "create", …), which
	// is what an auditor filters on; Method keeps the raw HTTP truth.
	Verb      string `gorm:"size:20;index" json:"verb"`
	Method    string `gorm:"size:10" json:"method"`
	Path      string `gorm:"type:text" json:"path"`
	Namespace string `gorm:"size:190;index" json:"namespace,omitempty"`
	Resource  string `gorm:"size:120" json:"resource,omitempty"`

	// The identity KubeMG asserted to the API server. This is the crux of the
	// record: it ties a KubeMG account to the Kubernetes subject that acted.
	ImpersonatedUser   string `gorm:"size:120" json:"impersonated_user,omitempty"`
	ImpersonatedGroups string `gorm:"type:text" json:"impersonated_groups,omitempty"`

	Status     int   `gorm:"index" json:"status"`
	DurationMS int64 `gorm:"column:duration_ms" json:"duration_ms"`

	// Streaming calls are recorded twice, at PhaseOpen and PhaseClose, so a
	// session that runs for an hour is visible while it is still running.
	Streaming bool   `gorm:"index" json:"streaming"`
	Phase     string `gorm:"size:10" json:"phase,omitempty"`
	BytesOut  int64  `json:"bytes_out,omitempty"`
	BytesIn   int64  `json:"bytes_in,omitempty"`
	// SessionID identifies one interactive session across both of its records,
	// and is what a terminal recording of that session is filed under. Empty on
	// everything that is not a session, and on sessions recorded before it
	// existed.
	SessionID string `gorm:"size:64;index" json:"session_id,omitempty"`

	// GuardrailPolicy names the safety policy this call matched and
	// GuardrailAction says what the match did. They are indexed together with
	// nothing else because the question they answer is one query — "what has this
	// rule caught?" — asked of a rule running in warn before it is armed.
	GuardrailPolicy string `gorm:"size:120;index" json:"guardrail_policy,omitempty"`
	GuardrailAction string `gorm:"size:16" json:"guardrail_action,omitempty"`

	Error string `gorm:"type:text" json:"error,omitempty"`

	// Where the call came from, as the server saw it: the client address
	// resolved through whatever proxy headers are trusted, and the client's own
	// claim about what it is. "From where" is the second question in an access
	// review after "who", and it is the one part of a record that cannot be
	// filled in later — a call already made has no address to go back for. Both
	// are empty on a record with no caller (the JIT expirer, the alarm poller)
	// and on every row written before these columns existed, which is a real
	// state and reads as "not recorded" rather than as an unknown address.
	SourceAddr string `gorm:"size:64;index" json:"source_addr,omitempty"`
	UserAgent  string `gorm:"type:text" json:"user_agent,omitempty"`

	// Diff is the field-level structural diff of a manifest write (an `update`
	// row's before/after, from pkg/objdiff), stored as its own JSON encoding —
	// this table has no JSON column type of its own to reach for without a new
	// dependency, so it is a string like Path and ImpersonatedGroups already
	// are. It is empty on every row that is not a successful manifest write
	// with the "record manifest diffs" setting on: gating that is entirely the
	// write path's job (pkg/api/resources_object.go and
	// bastion.Proxy.Call), not this table's, so that the one rule — never
	// stored for a refused write, never stored for a redacted kind, only
	// stored when the setting is on — lives in exactly one place. Because it
	// rides on this row rather than a table of its own, PruneAuditEvents'
	// existing `DELETE ... WHERE at < ?` prunes it for free; a diff outliving
	// the write it describes would be the retention window lying about what it
	// retains.
	Diff string `gorm:"type:text" json:"-"`
}

// TableName pins the audit table name.
func (AuditEvent) TableName() string { return "audit_events" }

// ImpersonatedGroupList splits the stored group list.
func (e AuditEvent) ImpersonatedGroupList() []string { return splitNamespaces(e.ImpersonatedGroups) }

// UserClusterAccess maps a KubeMG user onto a cluster with a Kubernetes role
// and an optional namespace scope.
//
// A user may hold more than one row per cluster, one per provenance: the standing
// grant an administrator wrote, the one a directory group derives, and a
// time-bound elevation a JIT approval activated. AccessForUser merges them into
// the most permissive live grant, which is what lets an elevation expire without
// anybody having to put the previous role back — the row it outranked never
// changed.
type UserClusterAccess struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// The uniqueness is per (user, cluster, source) rather than per (user,
	// cluster). Two rows of the *same* provenance would be two answers to one
	// question, which is what the constraint is for; rows of different provenance
	// are three different facts about the same person.
	UserID     uint   `gorm:"uniqueIndex:idx_user_cluster_source,priority:1;not null" json:"user_id"`
	ClusterID  uint   `gorm:"uniqueIndex:idx_user_cluster_source,priority:2;not null" json:"cluster_id"`
	K8sRole    string `gorm:"column:k8s_role;size:60;not null;default:view" json:"k8s_role"`
	Namespaces string `gorm:"type:text" json:"-"`
	// Source records who wrote this grant, and is what makes federated access
	// revocable: a grant derived from an IdP group is withdrawn on the next
	// login that no longer carries it, while one an administrator wrote by hand
	// survives untouched. Empty predates federation and means local.
	Source string `gorm:"size:20;not null;default:local;uniqueIndex:idx_user_cluster_source,priority:3" json:"source,omitempty"`
	// ExpiresAt is when this grant stops counting. Nil is a standing grant, which
	// is every row an administrator or a directory writes; a time is what a JIT
	// elevation carries.
	//
	// It is enforced by the *resolver* rather than by the sweeper that eventually
	// deletes the row — AccessForUser ignores an expired row — so a window that
	// has run out is closed the moment it runs out, whether or not any background
	// pass has run since. A sweeper that fell behind must never mean access that
	// outlived its approval.
	ExpiresAt *time.Time `gorm:"index" json:"expires_at,omitempty"`
}

// TableName pins the join table name; GORM would otherwise pluralize it to
// "user_cluster_accesses".
func (UserClusterAccess) TableName() string { return "user_cluster_access" }

// NamespaceList splits the stored namespace scope into a slice. An empty value
// means "all namespaces the Kubernetes role allows".
func (a UserClusterAccess) NamespaceList() []string { return splitNamespaces(a.Namespaces) }

func splitNamespaces(stored string) []string {
	if strings.TrimSpace(stored) == "" {
		return []string{}
	}
	parts := strings.Split(stored, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// JoinNamespaces renders a namespace slice for storage.
func JoinNamespaces(namespaces []string) string {
	out := make([]string, 0, len(namespaces))
	for _, n := range namespaces {
		if n = strings.TrimSpace(n); n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return strings.Join(out, ",")
}

// k8sRoleRank orders Kubernetes roles by privilege so overlapping grants can be
// merged deterministically.
func k8sRoleRank(role string) int {
	switch role {
	case K8sRoleClusterAdmin:
		return 3
	case K8sRoleEdit:
		return 2
	case K8sRoleView:
		return 1
	default:
		return 0
	}
}

// MergeAccess combines two grants for the same cluster into the more permissive
// one: the stronger Kubernetes role wins, and namespace scopes are unioned
// (an unscoped grant means "all namespaces" and therefore absorbs the other).
//
// The merged expiry is the *weakest* of the two, because that is what the merged
// capability is: a standing `view` merged with an elevation to `cluster-admin`
// until 15:00 is access that does not stop at 15:00, it is access that stops
// being cluster-admin then. Only callers holding both rows can say when each part
// ends, which is why the JIT surface reads the requests rather than this.
func MergeAccess(a, b UserClusterAccess) UserClusterAccess {
	out := a
	if k8sRoleRank(b.K8sRole) > k8sRoleRank(a.K8sRole) {
		out.K8sRole = b.K8sRole
	}
	switch {
	case a.ExpiresAt == nil || b.ExpiresAt == nil:
		out.ExpiresAt = nil
	case b.ExpiresAt.After(*a.ExpiresAt):
		out.ExpiresAt = b.ExpiresAt
	}
	if a.Namespaces == "" || b.Namespaces == "" {
		out.Namespaces = ""
		return out
	}
	out.Namespaces = JoinNamespaces(append(a.NamespaceList(), b.NamespaceList()...))
	return out
}
