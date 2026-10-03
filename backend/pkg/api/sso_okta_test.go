package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// Okta is OIDC or SAML like any other directory; what the vendor adds is the
// refusals Okta's own URLs make recognisable before anybody is sent there.

func TestOktaProviderIsStoredWithItsVendorAndServerScopes(t *testing.T) {
	env := newTestEnv(t)
	token := env.tokenFor(t, env.store.addUser("admin", "pw", db.RoleAdmin))

	for _, tc := range []struct {
		name, issuer, scopes string
	}{
		{"Okta org", "https://acme.okta.com/", "profile email groups"},
		{"Okta custom", "https://acme.okta.com/oauth2/default", "profile email"},
	} {
		res := env.do(t, http.MethodPost, "/api/v1/admin/sso/providers", token, map[string]any{
			"name": tc.name, "protocol": "oidc", "vendor": "okta",
			"issuer_url": tc.issuer, "client_id": "0oa1",
		})
		if res.Code != http.StatusCreated {
			t.Fatalf("%s: status = %d, want 201: %s", tc.name, res.Code, res.Body.String())
		}
		var created ssoProviderResponse
		if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if created.Vendor != db.VendorOkta {
			t.Fatalf("%s: vendor = %q, want okta", tc.name, created.Vendor)
		}
		// A custom authorization server refuses an undeclared "groups" scope
		// outright, so it is not asked for unless the operator types it.
		if created.Scopes != tc.scopes {
			t.Fatalf("%s: scopes = %q, want %q", tc.name, created.Scopes, tc.scopes)
		}
	}
}

func TestOktaRefusesWhatCannotBeAnIssuer(t *testing.T) {
	env := newTestEnv(t)
	token := env.tokenFor(t, env.store.addUser("admin", "pw", db.RoleAdmin))

	for _, tc := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"protocol": "oidc", "issuer_url": "https://acme-admin.okta.com"}, "https://acme.okta.com"},
		{map[string]any{"protocol": "oidc", "issuer_url": "https://acme.okta.com/oauth2/v1/authorize"}, "not an Okta issuer"},
		{map[string]any{"protocol": "oidc", "issuer_url": "https://acme.okta.com/oauth2/default/v1/token"}, "/oauth2/{server id}"},
		{map[string]any{"protocol": "oidc", "issuer_url": "http://acme.okta.com"}, "https"},
		{map[string]any{"protocol": "saml", "saml_metadata_url": "https://acme-admin.oktapreview.com/app/x/sso/saml/metadata"}, "acme.oktapreview.com"},
		{map[string]any{"protocol": "ldap", "ldap_host": "acme.ldap.okta.com", "ldap_base_dn": "dc=acme"}, "generic LDAP"},
	} {
		body := map[string]any{"name": "Okta", "vendor": "okta", "client_id": "0oa1"}
		for k, v := range tc.body {
			body[k] = v
		}
		res := env.do(t, http.MethodPost, "/api/v1/admin/sso/providers", token, body)
		if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), tc.want) {
			t.Fatalf("%v: status = %d body = %s, want 400 naming %q", tc.body, res.Code, res.Body.String(), tc.want)
		}
	}

	// A custom URL domain is Okta too: only the admin console is refused.
	res := env.do(t, http.MethodPost, "/api/v1/admin/sso/providers", token, map[string]any{
		"name": "Okta", "protocol": "oidc", "vendor": "okta",
		"issuer_url": "https://login.acme.com/oauth2/aus1", "client_id": "0oa1",
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("custom domain: status = %d, want 201: %s", res.Code, res.Body.String())
	}

	// An unknown vendor is not stored as a label nobody reads.
	res = env.do(t, http.MethodPost, "/api/v1/admin/sso/providers", token, map[string]any{
		"name": "Other", "protocol": "oidc", "vendor": "auth0",
		"issuer_url": "https://x.example.com", "client_id": "c",
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("unknown vendor: status = %d, want 400", res.Code)
	}
}
