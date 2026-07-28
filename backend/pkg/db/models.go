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
	u.Role = LegacyRoleFor(u.SystemRole)
}

// IsAdmin reports whether the user holds the KubeMG admin privilege.
func (u User) IsAdmin() bool {
	return u.Role == RoleAdmin || u.SystemRole == SystemRoleAdmin || u.SystemRole == SystemRoleSuperAdmin
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

	Error string `gorm:"type:text" json:"error,omitempty"`
}

// TableName pins the audit table name.
func (AuditEvent) TableName() string { return "audit_events" }

// ImpersonatedGroupList splits the stored group list.
func (e AuditEvent) ImpersonatedGroupList() []string { return splitNamespaces(e.ImpersonatedGroups) }

// UserClusterAccess maps a KubeMG user onto a cluster with a Kubernetes role
// and an optional namespace scope.
type UserClusterAccess struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	UserID     uint   `gorm:"uniqueIndex:idx_user_cluster;not null" json:"user_id"`
	ClusterID  uint   `gorm:"uniqueIndex:idx_user_cluster;not null" json:"cluster_id"`
	K8sRole    string `gorm:"column:k8s_role;size:60;not null;default:view" json:"k8s_role"`
	Namespaces string `gorm:"type:text" json:"-"`
	// Source records who wrote this grant, and is what makes federated access
	// revocable: a grant derived from an IdP group is withdrawn on the next
	// login that no longer carries it, while one an administrator wrote by hand
	// survives untouched. Empty predates federation and means local.
	Source string `gorm:"size:20;not null;default:local" json:"source,omitempty"`
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
func MergeAccess(a, b UserClusterAccess) UserClusterAccess {
	out := a
	if k8sRoleRank(b.K8sRole) > k8sRoleRank(a.K8sRole) {
		out.K8sRole = b.K8sRole
	}
	if a.Namespaces == "" || b.Namespaces == "" {
		out.Namespaces = ""
		return out
	}
	out.Namespaces = JoinNamespaces(append(a.NamespaceList(), b.NamespaceList()...))
	return out
}
