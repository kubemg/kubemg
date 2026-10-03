package api

import (
	"net/http"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

const profilePath = "/api/v1/auth/me"

func TestEditingYourOwnEmail(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	user := env.store.addUser("devops", "password", db.RoleUser)

	rec := env.do(t, http.MethodPatch, profilePath, env.tokenFor(t, user), map[string]any{
		"email": "  devops@example.com ",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if body := decode[userResponse](t, rec); body.Email != "devops@example.com" {
		t.Fatalf("expected the trimmed address back, got %q", body.Email)
	}
	if stored := env.store.users[user.ID].Email; stored != "devops@example.com" {
		t.Fatalf("expected the address stored, got %q", stored)
	}

	events := auditor.all()
	if len(events) != 1 || events[0].Verb != verbProfileUpdate || events[0].UserID != user.ID {
		t.Fatalf("expected one profile-update record for the caller, got %+v", events)
	}
}

// An address is optional, so clearing it is an edit like any other.
func TestClearingYourOwnEmail(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "password", db.RoleUser)
	user.Email = "devops@example.com"

	rec := env.do(t, http.MethodPatch, profilePath, env.tokenFor(t, user), map[string]any{"email": ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if stored := env.store.users[user.ID].Email; stored != "" {
		t.Fatalf("expected the address cleared, got %q", stored)
	}
}

func TestAnInvalidEmailIsRefused(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "password", db.RoleUser)

	rec := env.do(t, http.MethodPatch, profilePath, env.tokenFor(t, user), map[string]any{
		"email": "not-an-address",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	if stored := env.store.users[user.ID].Email; stored != "" {
		t.Fatalf("a refused edit was stored anyway: %q", stored)
	}
}

// The username is the impersonated identity and every audit record's name, so
// a body carrying one changes nothing — the field is not part of the request.
func TestTheUsernameIsNotSelfServed(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "password", db.RoleUser)

	env.do(t, http.MethodPatch, profilePath, env.tokenFor(t, user), map[string]any{
		"username":    "someone-else",
		"system_role": db.SystemRoleSuperAdmin,
	})
	stored := env.store.users[user.ID]
	if stored.Username != "devops" || stored.SystemRole == db.SystemRoleSuperAdmin {
		t.Fatalf("a self-service edit reached a field it must not: %+v", stored)
	}
}

// An edit that changes nothing is answered, not written.
func TestAnUnchangedEmailIsNotRecorded(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	user := env.store.addUser("devops", "password", db.RoleUser)
	user.Email = "devops@example.com"

	rec := env.do(t, http.MethodPatch, profilePath, env.tokenFor(t, user), map[string]any{
		"email": "devops@example.com",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if events := auditor.all(); len(events) != 0 {
		t.Fatalf("an edit that changed nothing was recorded: %+v", events)
	}
}

// A federated account's details belong to its directory, which writes them back
// at every sign-in — an edit here would be silently undone.
func TestAFederatedAccountCannotEditItsProfile(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("sso-user", "irrelevant", db.RoleUser)
	user.AuthSource = db.ProtocolOIDC

	rec := env.do(t, http.MethodPatch, profilePath, env.tokenFor(t, user), map[string]any{
		"email": "sso-user@example.com",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

func TestAMachineAccountCannotEditItsProfile(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("jenkins", "irrelevant", db.RoleUser)
	user.AccountType = db.AccountTypeMachine

	rec := env.do(t, http.MethodPatch, profilePath, env.tokenFor(t, user), map[string]any{
		"email": "jenkins@example.com",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

func TestEditingAProfileNeedsASession(t *testing.T) {
	env := newTestEnv(t)
	env.store.addUser("devops", "password", db.RoleUser)

	rec := env.do(t, http.MethodPatch, profilePath, "", map[string]any{"email": "a@example.com"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}
