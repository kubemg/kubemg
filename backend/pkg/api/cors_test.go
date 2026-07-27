package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const devOrigin = "http://localhost:5173"

// preflight replays what a browser sends before an authenticated cross-origin
// request. gin's cors.Default() fails this, which makes the whole SPA unusable.
func preflight(t *testing.T, router http.Handler, origin, method string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/clusters", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", method)
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestPreflightAllowsAuthorizationHeader(t *testing.T) {
	env := newTestEnv(t)

	rec := preflight(t, env.router, devOrigin, http.MethodGet)
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("expected the preflight to succeed, got %d", rec.Code)
	}

	allowHeaders := strings.ToLower(rec.Header().Get("Access-Control-Allow-Headers"))
	if !strings.Contains(allowHeaders, "authorization") {
		t.Fatalf("browser could not send its bearer token: allow-headers = %q", allowHeaders)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != devOrigin {
		t.Fatalf("expected the dev origin to be allowed, got %q", got)
	}
}

func TestPreflightAllowsClusterDeletion(t *testing.T) {
	env := newTestEnv(t)

	rec := preflight(t, env.router, devOrigin, http.MethodDelete)
	allowMethods := rec.Header().Get("Access-Control-Allow-Methods")
	if !strings.Contains(allowMethods, http.MethodDelete) {
		t.Fatalf("expected DELETE to be allowed, got %q", allowMethods)
	}
}

func TestCORSRejectsUnknownOrigin(t *testing.T) {
	env := newTestEnv(t)

	rec := preflight(t, env.router, "https://evil.example.com", http.MethodGet)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no allow-origin for an unlisted origin, got %q", got)
	}
}

func TestCORSHonoursConfiguredOrigins(t *testing.T) {
	store := newFakeStore()
	router := NewRouter(Options{
		Store:          store,
		JWT:            authManagerForTest(),
		AllowedOrigins: []string{"https://kubemg.internal"},
	})

	rec := preflight(t, router, "https://kubemg.internal", http.MethodGet)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://kubemg.internal" {
		t.Fatalf("expected the configured origin to be allowed, got %q", got)
	}

	rec = preflight(t, router, devOrigin, http.MethodGet)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected the default origin to be replaced, got %q", got)
	}
}

func TestCORSWildcardOrigin(t *testing.T) {
	store := newFakeStore()
	router := NewRouter(Options{
		Store:          store,
		JWT:            authManagerForTest(),
		AllowedOrigins: []string{"*"},
	})

	rec := preflight(t, router, "https://anywhere.example.com", http.MethodGet)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("expected a wildcard allow-origin, got %q", got)
	}
}

// An authenticated GET must carry the allow-origin header, or the browser
// discards the response even though the server answered it.
func TestAuthenticatedRequestCarriesCORSHeader(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", "admin")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil)
	req.Header.Set("Origin", devOrigin)
	req.Header.Set("Authorization", "Bearer "+env.tokenFor(t, admin))

	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != devOrigin {
		t.Fatalf("expected allow-origin %q, got %q", devOrigin, got)
	}
}
