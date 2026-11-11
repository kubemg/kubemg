package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// A caller with no session sends a bounded body. The same sign-in that
// succeeds small is refused once its padding passes the bound, so the
// handler never reads a body of the caller's choosing.
func TestAnAnonymousRouteReadsABoundedBody(t *testing.T) {
	env := newTestEnv(t)
	env.store.addUser("root", "pw", db.RoleAdmin)

	small := map[string]string{"username": "root", "password": "pw", "pad": "x"}
	if rec := env.do(t, http.MethodPost, "/api/v1/auth/login", "", small); rec.Code != http.StatusOK {
		t.Fatalf("a small sign-in: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	large := map[string]string{"username": "root", "password": "pw", "pad": strings.Repeat("x", int(maxAnonymousBody))}
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/cli/device", "/api/v1/auth/mfa/verify"} {
		rec := env.do(t, http.MethodPost, path, "", large)
		if rec.Code == http.StatusOK || rec.Code >= http.StatusInternalServerError {
			t.Errorf("%s with a body past the bound: want a 4xx refusal, got %d", path, rec.Code)
		}
	}
	// A bearer header does not lift the bound: it is attached by route, not
	// read from the request.
	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", "not-a-token", large)
	if rec.Code == http.StatusOK {
		t.Fatal("an Authorization header lifted the bound")
	}
}
