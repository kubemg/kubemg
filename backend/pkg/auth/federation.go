package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
)

/*
 * Federation: the part of sign-in that happens somewhere else.
 *
 * Three protocols, one outcome. An engine's whole job is to turn whatever its
 * directory said into a db.SSOIdentity — who this is, what they are called, and
 * which groups they are in — and to be wrong about none of it. What that
 * identity is *worth* is decided by the mapping rules in pkg/db, which have
 * never heard of a token, an assertion or a bind.
 *
 * Nothing here trusts the browser. An OIDC code is exchanged server to server
 * and its ID token verified against the issuer's keys; a SAML response is
 * verified against the IdP's signing certificate; an LDAP password is checked by
 * the directory itself. The state and nonce that tie a callback back to the
 * request that started it are minted here and held server-side.
 */

// Claim names KubeMG falls back to when a provider does not say which claim
// carries what. They are ordered by how specific they are: an identifier meant
// to be shown to a person beats one meant to be unique, because a username ends
// up in the audit trail and in Kubernetes impersonation headers.
var (
	usernameClaimCandidates = []string{
		"preferred_username", "username", "nickname", "email", "upn", "sub",
	}
	emailClaimCandidates  = []string{"email", "mail", "upn"}
	groupsClaimCandidates = []string{
		"groups", "roles", "memberOf",
		// What Active Directory Federation Services and Entra ID send.
		"http://schemas.xmlsoap.org/claims/Group",
		"http://schemas.microsoft.com/ws/2008/06/identity/claims/role",
	}
)

// SAML assertion attribute names for the same three things. SAML has no
// equivalent of OIDC's standard claim set, so the friendly names and the OASIS
// URNs both have to be tried.
var (
	samlUsernameCandidates = append([]string{
		"urn:oid:0.9.2342.19200300.100.1.1", // uid
		"uid", "username", "user.login", "NameID",
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name",
	}, usernameClaimCandidates...)
	samlEmailCandidates = append([]string{
		"urn:oid:0.9.2342.19200300.100.1.3", // mail
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress",
	}, emailClaimCandidates...)
	samlGroupCandidates = append([]string{
		"urn:oid:1.3.6.1.4.1.5923.1.5.1.1", // isMemberOf
		"Group", "Groups", "group",
	}, groupsClaimCandidates...)
)

// NewStateToken mints the opaque value that ties a callback to the request that
// started it — the OAuth state parameter, the OIDC nonce, and the SAML
// RelayState are all this.
func NewStateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// claimString reads a single string claim, trying the configured name first and
// then the well-known ones, so a provider left half-configured still works.
func claimString(claims map[string]any, configured string, candidates []string) string {
	for _, name := range claimOrder(configured, candidates) {
		if value := firstString(claims[name]); value != "" {
			return value
		}
	}
	return ""
}

// claimStrings reads a list claim. Directories disagree about the shape: a JSON
// array, a single string, or one comma- or semicolon-separated string are all in
// the field, and reading only the first shows a user with no groups at all
// rather than an error anyone would notice.
func claimStrings(claims map[string]any, configured string, candidates []string) []string {
	for _, name := range claimOrder(configured, candidates) {
		if values := toStrings(claims[name]); len(values) > 0 {
			return values
		}
	}
	return nil
}

// claimOrder puts the configured claim name first and then the fallbacks, with
// no name tried twice.
func claimOrder(configured string, candidates []string) []string {
	order := make([]string, 0, len(candidates)+1)
	if configured = strings.TrimSpace(configured); configured != "" {
		order = append(order, configured)
	}
	for _, candidate := range candidates {
		if !slices.Contains(order, candidate) {
			order = append(order, candidate)
		}
	}
	return order
}

func firstString(value any) string {
	values := toStrings(value)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// toStrings flattens whatever a claim turned out to be into a list of non-empty
// strings.
func toStrings(value any) []string {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return splitList(typed)
	case []string:
		out := []string{}
		for _, item := range typed {
			out = append(out, splitList(item)...)
		}
		return out
	case []any:
		out := []string{}
		for _, item := range typed {
			out = append(out, toStrings(item)...)
		}
		return out
	case float64:
		// A numeric subject is legal and Entra ID sends numeric object ids.
		return splitList(fmt.Sprintf("%.0f", typed))
	case bool:
		return nil
	default:
		return splitList(fmt.Sprint(typed))
	}
}

// splitList breaks a delimited claim into its parts. A group name may contain a
// space (and a distinguished name contains commas), so only the separators that
// cannot appear unescaped in an LDAP DN are treated as such.
func splitList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if !strings.ContainsAny(value, ";\n") {
		return []string{value}
	}
	out := []string{}
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == ';' || r == '\n' }) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// dedupe drops repeats while keeping the order the directory returned, which is
// the order an operator sees in their IdP.
func dedupe(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}
