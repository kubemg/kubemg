package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

func TestLoginReturnsToken(t *testing.T) {
	env := newTestEnv(t)
	env.store.addUser("devops", "s3cret", db.RoleUser)

	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": "devops",
		"password": "s3cret",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[loginResponse](t, rec)
	if body.Token == "" {
		t.Fatal("expected a non-empty token")
	}
	if body.ExpiresAt.IsZero() {
		t.Fatal("expected a token expiry")
	}
	if body.User.Username != "devops" || body.User.Role != db.RoleUser {
		t.Fatalf("unexpected user payload: %+v", body.User)
	}

	claims, err := env.jwt.Parse(body.Token)
	if err != nil {
		t.Fatalf("issued token did not verify: %v", err)
	}
	if claims.Username != "devops" {
		t.Fatalf("expected username claim \"devops\", got %q", claims.Username)
	}
}

func TestLoginNeverLeaksPasswordHash(t *testing.T) {
	env := newTestEnv(t)
	env.store.addUser("devops", "s3cret", db.RoleUser)

	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": "devops",
		"password": "s3cret",
	})

	if got := rec.Body.String(); strings.Contains(got, "password_hash") || strings.Contains(got, "$2a$") {
		t.Fatalf("login response leaked password material: %s", got)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	env := newTestEnv(t)
	env.store.addUser("devops", "s3cret", db.RoleUser)

	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": "devops",
		"password": "wrong",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestLoginRejectsUnknownUser(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": "ghost",
		"password": "whatever",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestLoginRequiresCredentials(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "devops"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestMeReturnsAuthenticatedUser(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "s3cret", db.RoleUser)

	rec := env.do(t, http.MethodGet, "/api/v1/auth/me", env.tokenFor(t, user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[userResponse](t, rec)
	if body.ID != user.ID || body.Username != "devops" {
		t.Fatalf("unexpected user payload: %+v", body)
	}
}

func TestMeRequiresToken(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, http.MethodGet, "/api/v1/auth/me", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestMeRejectsTokenSignedWithOtherSecret(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "s3cret", db.RoleUser)

	forged, _, err := auth.NewManager("attacker-secret", time.Hour).
		Generate(user.ID, user.Username, db.RoleAdmin)
	if err != nil {
		t.Fatalf("forge token: %v", err)
	}

	rec := env.do(t, http.MethodGet, "/api/v1/auth/me", forged, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestMeRejectsDeletedUser(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "s3cret", db.RoleUser)
	token := env.tokenFor(t, user)
	delete(env.store.users, user.ID)

	rec := env.do(t, http.MethodGet, "/api/v1/auth/me", token, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}
