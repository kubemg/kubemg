package api

import (
	"net/http"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The footer is on every page for everybody, so the version is readable by any
// signed-in caller — not only an administrator.
func TestServerVersionIsReadableByAnyone(t *testing.T) {
	env := newTestEnvWith(t, func(opts *Options) { opts.Version = "0.7.0" })
	user := env.store.addUser("dev", "pw", db.RoleUser)

	rec := env.do(t, http.MethodGet, "/api/v1/version", env.tokenFor(t, user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	body := decode[versionResponse](t, rec)
	if body.Version != "0.7.0" {
		t.Fatalf("expected the stamped version, got %q", body.Version)
	}
	if body.DocsURL != docsURL {
		t.Fatalf("expected the manual's address, got %q", body.DocsURL)
	}
}

// An unstamped build says so rather than reporting a number nobody set.
func TestServerVersionUnstamped(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)

	rec := env.do(t, http.MethodGet, "/api/v1/version", env.tokenFor(t, user), nil)
	if got := decode[versionResponse](t, rec).Version; got != unknownVersion {
		t.Fatalf("expected %q, got %q", unknownVersion, got)
	}
}

// The exact release is the first thing a scanner wants in order to match a
// published advisory against an install, so it is behind a session.
func TestServerVersionNeedsAuth(t *testing.T) {
	env := newTestEnv(t)
	if rec := env.do(t, http.MethodGet, "/api/v1/version", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}
