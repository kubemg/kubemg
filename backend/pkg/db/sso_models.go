package db

import (
	"slices"
	"strings"
	"time"
)

/*
 * Federated identity: who an external directory says someone is, and what that
 * buys them here.
 *
 * KubeMG's local accounts stay exactly as they were — an on-prem install with no
 * IdP must keep working — so federation is additive: a provider row says how to
 * talk to one directory, and mapping rows say what an external group is worth.
 * Nothing about the permission model changes; a federated login lands on the
 * same UserClusterAccess and UserGroup rows an administrator would have written
 * by hand, which is what keeps the audit trail and the permission matrix honest.
 *
 * The one thing that *is* new is provenance. A grant KubeMG derived from an IdP
 * group has to disappear when the person leaves that group, and a grant an
 * administrator wrote by hand must never disappear because a directory did not
 * mention it. That is what the Source column on the two grant tables carries,
 * and it is the reason the sync is a reconcile rather than an insert.
 */

// Identity federation protocols.
const (
	ProtocolOIDC = "oidc"
	ProtocolSAML = "saml"
	ProtocolLDAP = "ldap"
)

// SSOProtocols enumerates the assignable protocols.
var SSOProtocols = []string{ProtocolOIDC, ProtocolSAML, ProtocolLDAP}

// ValidSSOProtocol reports whether a protocol is one KubeMG speaks.
func ValidSSOProtocol(protocol string) bool { return slices.Contains(SSOProtocols, protocol) }

// Where an account's credentials live. A local account is the Phase 1 shape:
// a bcrypt hash in this database. A federated account has no usable password at
// all, so password sign-in is refused for it rather than merely failing.
const (
	AuthSourceLocal = "local"
)

// Provenance of a grant or a group membership. A row KubeMG derived from an IdP
// group is reconciled on every login; a row an administrator wrote is left
// alone. An empty value predates federation and therefore means local.
const (
	GrantSourceLocal = "local"
	GrantSourceSSO   = "sso"
)

// IsFederatedSource reports whether an auth source names an external directory.
func IsFederatedSource(source string) bool {
	return source != "" && source != AuthSourceLocal
}

// SSOProviderConfig is one configured identity provider. The three protocols
// share a row rather than a table each because they answer the same question —
// who is this and what groups are they in — and differ only in how they are
// asked; the fields not relevant to a protocol are simply empty.
//
// Every secret on it is treated the way a cluster's service account token is:
// stored, never serialized. A caller that needs to know whether one is set reads
// the rendered has_* flags instead.
type SSOProviderConfig struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Name is what the login button says, so it is the operator's own words.
	Name     string `gorm:"size:120;uniqueIndex;not null" json:"name"`
	Protocol string `gorm:"size:20;not null" json:"protocol"`
	Enabled  bool   `gorm:"not null;default:true" json:"enabled"`

	// OIDC. The issuer is discovered rather than configured field by field: an
	// operator pastes the issuer URL and KubeMG reads its well-known document.
	IssuerURL    string `gorm:"size:512" json:"issuer_url,omitempty"`
	ClientID     string `gorm:"size:255" json:"client_id,omitempty"`
	ClientSecret string `gorm:"type:text" json:"-"`
	// Scopes is the space-separated set requested on top of "openid". Directories
	// disagree about where groups live, so this is configurable.
	Scopes string `gorm:"size:255" json:"scopes,omitempty"`

	// SAML. Either the metadata URL — fetched and re-fetched — or a pasted
	// document, since plenty of IdPs hand an operator an XML file and no URL.
	SAMLMetadataURL string `gorm:"column:saml_metadata_url;size:512" json:"saml_metadata_url,omitempty"`
	SAMLMetadataXML string `gorm:"column:saml_metadata_xml;type:text" json:"-"`
	// SAMLEntityID is what KubeMG calls itself to this IdP. Empty means the
	// generated default, which is the SP metadata URL.
	SAMLEntityID string `gorm:"column:saml_entity_id;size:512" json:"saml_entity_id,omitempty"`

	// LDAP.
	LDAPHost string `gorm:"column:ldap_host;size:255" json:"ldap_host,omitempty"`
	LDAPPort int    `gorm:"column:ldap_port" json:"ldap_port,omitempty"`
	// LDAPUseTLS dials ldaps://; LDAPStartTLS upgrades a plain connection. Both
	// off is a cleartext bind, which is refused unless the host is loopback.
	LDAPUseTLS   bool `gorm:"column:ldap_use_tls;not null;default:true" json:"ldap_use_tls"`
	LDAPStartTLS bool `gorm:"column:ldap_start_tls;not null;default:false" json:"ldap_start_tls"`
	// LDAPSkipVerify exists for a directory with an internal certificate nobody
	// has exported yet. It is a warning in the UI, not a default.
	LDAPSkipVerify bool `gorm:"column:ldap_skip_verify;not null;default:false" json:"ldap_skip_verify"`
	// The service account KubeMG searches as. A directory that allows anonymous
	// search can leave it empty.
	LDAPBindDN       string `gorm:"column:ldap_bind_dn;size:512" json:"ldap_bind_dn,omitempty"`
	LDAPBindPassword string `gorm:"column:ldap_bind_password;type:text" json:"-"`
	LDAPBaseDN       string `gorm:"column:ldap_base_dn;size:512" json:"ldap_base_dn,omitempty"`
	// LDAPUserFilter locates the account being signed in. "%s" is replaced with
	// the escaped username; a filter without it is ANDed with the username
	// attribute instead.
	LDAPUserFilter     string `gorm:"column:ldap_user_filter;size:512" json:"ldap_user_filter,omitempty"`
	LDAPUserAttribute  string `gorm:"column:ldap_user_attribute;size:120" json:"ldap_user_attribute,omitempty"`
	LDAPEmailAttribute string `gorm:"column:ldap_email_attribute;size:120" json:"ldap_email_attribute,omitempty"`
	// LDAPGroupAttribute is the attribute on the user entry listing its groups —
	// memberOf on Active Directory and on most modern OpenLDAP deployments.
	LDAPGroupAttribute string `gorm:"column:ldap_group_attribute;size:120" json:"ldap_group_attribute,omitempty"`
	// LDAPGroupFilter is the fallback for a directory that keeps membership on
	// the group rather than on the user: "%s" is the user's DN.
	LDAPGroupFilter string `gorm:"column:ldap_group_filter;size:512" json:"ldap_group_filter,omitempty"`
	// LDAPGroupBaseDN scopes that search; empty falls back to LDAPBaseDN.
	LDAPGroupBaseDN string `gorm:"column:ldap_group_base_dn;size:512" json:"ldap_group_base_dn,omitempty"`
	// LDAPGroupNameAttribute is what a matched group entry is called for mapping
	// purposes — cn for a readable name, or empty to match on the whole DN.
	LDAPGroupNameAttribute string `gorm:"column:ldap_group_name_attribute;size:120" json:"ldap_group_name_attribute,omitempty"`

	// Claim mapping, shared by OIDC and SAML: which claim or assertion attribute
	// carries the username, the email and the group list.
	UsernameClaim string `gorm:"size:190" json:"username_claim,omitempty"`
	EmailClaim    string `gorm:"size:190" json:"email_claim,omitempty"`
	GroupsClaim   string `gorm:"size:190" json:"groups_claim,omitempty"`

	// AllowJIT provisions an account on first successful sign-in. With it off, a
	// directory can authenticate someone KubeMG has never heard of and they are
	// still refused — which is what an install that pre-creates its accounts
	// wants.
	AllowJIT bool `gorm:"column:allow_jit;not null;default:true" json:"allow_jit"`
	// DefaultSystemRole is what a provisioned account starts as. It is validated
	// down to user/admin: a super admin is created by hand, never by a directory.
	DefaultSystemRole string `gorm:"size:20;not null;default:user" json:"default_system_role"`

	LastStatus    string     `gorm:"size:20;not null;default:pending" json:"last_status"`
	LastMessage   string     `gorm:"type:text" json:"last_message,omitempty"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the provider table name.
func (SSOProviderConfig) TableName() string { return "sso_providers" }

// HasClientSecret reports whether an OIDC secret is stored.
func (p SSOProviderConfig) HasClientSecret() bool { return strings.TrimSpace(p.ClientSecret) != "" }

// HasBindPassword reports whether an LDAP bind password is stored.
func (p SSOProviderConfig) HasBindPassword() bool {
	return strings.TrimSpace(p.LDAPBindPassword) != ""
}

// Interactive reports whether signing in with this provider means leaving the
// browser for the IdP. LDAP is the one that does not: it takes a username and a
// password on KubeMG's own form.
func (p SSOProviderConfig) Interactive() bool { return p.Protocol != ProtocolLDAP }

// Normalize fills in the defaults each protocol needs to be usable, so an
// operator who leaves the optional half of the form empty still gets a working
// provider rather than a subtly broken one.
func (p *SSOProviderConfig) Normalize() {
	p.Name = strings.TrimSpace(p.Name)
	p.Protocol = strings.ToLower(strings.TrimSpace(p.Protocol))

	// A directory must never be able to mint a super admin: that tier exists to
	// be the account an IdP outage cannot lock you out of.
	if p.DefaultSystemRole != SystemRoleAdmin {
		p.DefaultSystemRole = SystemRoleUser
	}

	switch p.Protocol {
	case ProtocolOIDC:
		p.IssuerURL = strings.TrimRight(strings.TrimSpace(p.IssuerURL), "/")
		if strings.TrimSpace(p.Scopes) == "" {
			// profile and email are what the username and email claims come
			// from; groups is what most directories call the group claim, and a
			// provider that does not know it ignores it rather than failing.
			p.Scopes = "profile email groups"
		}
		p.UsernameClaim = defaultClaim(p.UsernameClaim, "preferred_username")
		p.EmailClaim = defaultClaim(p.EmailClaim, "email")
		p.GroupsClaim = defaultClaim(p.GroupsClaim, "groups")
	case ProtocolSAML:
		p.SAMLMetadataURL = strings.TrimSpace(p.SAMLMetadataURL)
		p.SAMLEntityID = strings.TrimSpace(p.SAMLEntityID)
		// SAML has no universal claim names, so the defaults are the two
		// vocabularies actually in the field: the friendly names most IdPs send
		// and the long OASIS URNs Active Directory Federation Services sends.
		// The resolver tries the configured name first and then those.
		p.UsernameClaim = defaultClaim(p.UsernameClaim, "")
		p.EmailClaim = defaultClaim(p.EmailClaim, "")
		p.GroupsClaim = defaultClaim(p.GroupsClaim, "")
	case ProtocolLDAP:
		p.LDAPHost = strings.TrimSpace(p.LDAPHost)
		if p.LDAPPort <= 0 || p.LDAPPort > 65535 {
			p.LDAPPort = DefaultLDAPPort(p.LDAPUseTLS)
		}
		p.LDAPUserAttribute = defaultClaim(p.LDAPUserAttribute, "uid")
		p.LDAPEmailAttribute = defaultClaim(p.LDAPEmailAttribute, "mail")
		p.LDAPGroupAttribute = defaultClaim(p.LDAPGroupAttribute, "memberOf")
		p.LDAPUserFilter = strings.TrimSpace(p.LDAPUserFilter)
		p.LDAPGroupFilter = strings.TrimSpace(p.LDAPGroupFilter)
		p.LDAPGroupBaseDN = strings.TrimSpace(p.LDAPGroupBaseDN)
		p.LDAPGroupNameAttribute = strings.TrimSpace(p.LDAPGroupNameAttribute)
	}

	if p.LastStatus == "" {
		p.LastStatus = StatusPending
	}
}

// DefaultLDAPPort is 636 for an implicit-TLS dial and 389 otherwise.
func DefaultLDAPPort(useTLS bool) int {
	if useTLS {
		return 636
	}
	return 389
}

func defaultClaim(value, fallback string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return fallback
}

// SSOGroupMapping is what one external group is worth here.
//
// A rule can do either of two things, or both: put the person in a local group,
// which then carries whatever that group has been granted; and grant a
// Kubernetes role directly across every cluster in an environment, which is the
// shape "everyone in ops-oncall gets edit on staging" actually takes. The second
// is the reason EnvironmentFilter exists — a rule written per cluster would have
// to be rewritten every time a cluster is registered, which is exactly when
// nobody remembers to.
type SSOGroupMapping struct {
	ID         uint `gorm:"primaryKey" json:"id"`
	ProviderID uint `gorm:"index;not null" json:"provider_id"`

	// ExternalGroupPattern matches the group names the IdP asserted, case
	// insensitively, with "*" standing for any run of characters. "*" on its own
	// matches everyone the provider authenticates.
	ExternalGroupPattern string `gorm:"size:512;not null" json:"external_group_pattern"`

	// TargetGroupID is the local group members are put into. Zero means the rule
	// only grants a Kubernetes role.
	TargetGroupID uint `gorm:"index" json:"target_group_id,omitempty"`

	// TargetK8sRole is granted on every cluster the environment filter selects.
	// Empty means the rule only confers local group membership.
	TargetK8sRole string `gorm:"column:target_k8s_role;size:60" json:"target_k8s_role,omitempty"`
	// EnvironmentFilter narrows that grant to one environment. Empty means every
	// registered cluster, which is deliberately spelled out in the UI.
	EnvironmentFilter string `gorm:"size:20" json:"environment_filter,omitempty"`
	// Namespaces scopes the derived grant the same way a hand-written one is
	// scoped. Empty is cluster-wide.
	Namespaces string `gorm:"type:text" json:"-"`

	// TargetSystemRole elevates the account itself — how an IdP group becomes
	// KubeMG administrators. Empty leaves the provider's default in place, and
	// superadmin is refused here as it is everywhere else.
	TargetSystemRole string `gorm:"size:20" json:"target_system_role,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the mapping table name.
func (SSOGroupMapping) TableName() string { return "sso_group_mappings" }

// NamespaceList splits the stored namespace scope.
func (m SSOGroupMapping) NamespaceList() []string { return splitNamespaces(m.Namespaces) }

// Matches reports whether an asserted group name satisfies this rule.
func (m SSOGroupMapping) Matches(externalGroup string) bool {
	return MatchGroupPattern(m.ExternalGroupPattern, externalGroup)
}

// MatchGroupPattern matches an asserted group against a rule's pattern, case
// insensitively, with "*" standing for any run of characters.
//
// It is deliberately not a regular expression and deliberately not path.Match:
// group names arrive as LDAP distinguished names as often as as bare words, and
// both a regex metacharacter and path.Match's refusal to let "*" cross a
// separator would make the obvious pattern quietly match nothing.
func MatchGroupPattern(pattern, group string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	group = strings.ToLower(strings.TrimSpace(group))
	if pattern == "" || group == "" {
		return false
	}
	if !strings.Contains(pattern, "*") {
		return pattern == group
	}

	// Two-pointer glob: walk both strings, and on a "*" remember where to
	// backtrack to rather than recursing, so a pattern of many stars against a
	// long DN stays linear.
	var (
		p, g       int
		star       = -1
		groupMatch int
	)
	for g < len(group) {
		switch {
		case p < len(pattern) && (pattern[p] == group[g]):
			p++
			g++
		case p < len(pattern) && pattern[p] == '*':
			star = p
			groupMatch = g
			p++
		case star >= 0:
			p = star + 1
			groupMatch++
			g = groupMatch
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
