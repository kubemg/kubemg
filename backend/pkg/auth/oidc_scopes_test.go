package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// discoveryServer serves an issuer whose discovery document advertises the
// given scopes, which is all CheckOIDC reads.
func discoveryServer(t *testing.T, scopes []string) string {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         srv.URL + "/token",
			"jwks_uri":               srv.URL + "/keys",
			"scopes_supported":       scopes,
		})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestCheckOIDCFailsAnOktaScopeTheServerDoesNotOffer(t *testing.T) {
	issuer := discoveryServer(t, []string{"openid", "profile", "email"})

	okta := &db.SSOProviderConfig{Protocol: db.ProtocolOIDC, Vendor: db.VendorOkta, IssuerURL: issuer, Scopes: "profile email groups"}
	if _, err := CheckOIDC(context.Background(), okta); err == nil || !strings.Contains(err.Error(), `"groups"`) {
		t.Fatalf("okta check err = %v, want a refusal naming groups", err)
	}

	generic := &db.SSOProviderConfig{Protocol: db.ProtocolOIDC, IssuerURL: issuer, Scopes: "profile email groups"}
	message, err := CheckOIDC(context.Background(), generic)
	if err != nil || !strings.Contains(message, `"groups"`) {
		t.Fatalf("generic check = %q, %v; want healthy with a note naming groups", message, err)
	}

	okta.Scopes = "profile email"
	if _, err := CheckOIDC(context.Background(), okta); err != nil {
		t.Fatalf("okta check with offered scopes: %v", err)
	}
}

func TestUnofferedScopesTrustsAnIssuerThatAdvertisesNothing(t *testing.T) {
	if got := unofferedScopes("profile groups", nil); got != nil {
		t.Fatalf("got %v, want nothing second-guessed", got)
	}
	if got := unofferedScopes("openid profile groups", []string{"profile"}); len(got) != 1 || got[0] != "groups" {
		t.Fatalf("got %v, want [groups]", got)
	}
}
