package db

import "testing"

func TestMatchGroupPattern(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		group   string
		want    bool
	}{
		{"exact", "platform", "platform", true},
		{"case insensitive", "Platform-Admins", "platform-admins", true},
		{"prefix", "platform-*", "platform-oncall", true},
		{"prefix does not match a different word", "platform-*", "payments-oncall", false},
		{"suffix", "*-admins", "payments-admins", true},
		{"infix", "*oncall*", "eu-oncall-rota", true},
		{"everything", "*", "anything at all", true},
		{"star matches nothing", "platform*", "platform", true},

		// A distinguished name is the shape LDAP and several SAML IdPs assert
		// groups in, and it contains the separators a path-style matcher treats
		// specially — which is exactly why this is not path.Match.
		{
			"distinguished name",
			"cn=platform-*,ou=groups,dc=example,dc=com",
			"CN=platform-admins,OU=Groups,DC=example,DC=com",
			true,
		},
		{"distinguished name across separators", "*dc=example*", "cn=eng,ou=groups,dc=example,dc=com", true},

		// A rule that matches nothing is safer than one that matches everything,
		// so an empty pattern is never a wildcard.
		{"empty pattern", "", "platform", false},
		{"empty group", "platform", "", false},

		{"multiple stars", "*-platform-*", "eu-platform-oncall", true},
		{"backtracking", "*ab*cd", "xxabxxabcd", true},
		{"no match after backtracking", "*ab*cd", "xxabxxcdx", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchGroupPattern(tc.pattern, tc.group); got != tc.want {
				t.Fatalf("MatchGroupPattern(%q, %q) = %v, want %v", tc.pattern, tc.group, got, tc.want)
			}
		})
	}
}

func TestProviderNormalizeFillsProtocolDefaults(t *testing.T) {
	oidc := SSOProviderConfig{Protocol: ProtocolOIDC, IssuerURL: "https://idp.example.com/"}
	oidc.Normalize()
	if oidc.IssuerURL != "https://idp.example.com" {
		t.Fatalf("issuer = %q, want the trailing slash trimmed", oidc.IssuerURL)
	}
	if oidc.UsernameClaim != "preferred_username" || oidc.GroupsClaim != "groups" {
		t.Fatalf("claims = %q/%q, want the OIDC defaults", oidc.UsernameClaim, oidc.GroupsClaim)
	}
	if oidc.Scopes == "" {
		t.Fatal("an OIDC provider with no scopes cannot return a username")
	}

	ldap := SSOProviderConfig{Protocol: ProtocolLDAP, LDAPUseTLS: true}
	ldap.Normalize()
	if ldap.LDAPPort != 636 {
		t.Fatalf("port = %d, want 636 for an implicit-TLS dial", ldap.LDAPPort)
	}
	if ldap.LDAPGroupAttribute != "memberOf" {
		t.Fatalf("group attribute = %q, want memberOf", ldap.LDAPGroupAttribute)
	}

	plain := SSOProviderConfig{Protocol: ProtocolLDAP}
	plain.Normalize()
	if plain.LDAPPort != 389 {
		t.Fatalf("port = %d, want 389 without TLS", plain.LDAPPort)
	}
}

func TestProviderNormalizeRefusesSuperAdminDefault(t *testing.T) {
	// The super admin tier exists to be the account an IdP outage cannot lock
	// you out of, so no directory may confer it.
	provider := SSOProviderConfig{Protocol: ProtocolOIDC, DefaultSystemRole: SystemRoleSuperAdmin}
	provider.Normalize()
	if provider.DefaultSystemRole != SystemRoleUser {
		t.Fatalf("default role = %q, want it capped at user", provider.DefaultSystemRole)
	}

	admin := SSOProviderConfig{Protocol: ProtocolOIDC, DefaultSystemRole: SystemRoleAdmin}
	admin.Normalize()
	if admin.DefaultSystemRole != SystemRoleAdmin {
		t.Fatalf("default role = %q, want admin to survive", admin.DefaultSystemRole)
	}
}

func TestFederatedSourceReadsLegacyRowsAsLocal(t *testing.T) {
	// A row written before federation existed has no auth source at all, and
	// reading it as federated would lock a local account out of its own login.
	if IsFederatedSource("") || IsFederatedSource(AuthSourceLocal) {
		t.Fatal("a blank or local auth source must not read as federated")
	}
	if !IsFederatedSource(ProtocolOIDC) {
		t.Fatal("a protocol auth source is federated")
	}
}
