package api

import (
	"net/http"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

const passwordPath = "/api/v1/auth/password"

// signIn is the only proof that matters here: a rotation is done when the new
// password opens a session and the old one no longer does.
func signIn(t *testing.T, env *testEnv, username, password string) int {
	t.Helper()
	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": username,
		"password": password,
	})
	return rec.Code
}

func TestChangingYourOwnPassword(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "old-password", db.RoleUser)

	rec := env.do(t, http.MethodPost, passwordPath, env.tokenFor(t, user), map[string]any{
		"current_password": "old-password",
		"new_password":     "new-password",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if body := decode[changePasswordResponse](t, rec); !body.Changed {
		t.Fatalf("expected the rotation to be reported: %+v", body)
	}

	if code := signIn(t, env, "devops", "new-password"); code != http.StatusOK {
		t.Fatalf("the new password does not sign in: %d", code)
	}
	if code := signIn(t, env, "devops", "old-password"); code != http.StatusUnauthorized {
		t.Fatalf("the old password still signs in: %d", code)
	}
}

// The stored credential must be a hash of the new password, not the password.
func TestChangingAPasswordStoresAHash(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "old-password", db.RoleUser)

	env.do(t, http.MethodPost, passwordPath, env.tokenFor(t, user), map[string]any{
		"current_password": "old-password",
		"new_password":     "new-password",
	})

	stored := env.store.users[user.ID].PasswordHash
	if stored == "new-password" {
		t.Fatal("the new password was stored in the clear")
	}
	if !auth.CheckPassword(stored, "new-password") {
		t.Fatal("the stored hash does not verify the new password")
	}
}

// A live session is not enough. This is the whole reason the current password is
// required: a stolen token must not be able to lock the owner out.
func TestChangingAPasswordNeedsTheCurrentOne(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "old-password", db.RoleUser)

	rec := env.do(t, http.MethodPost, passwordPath, env.tokenFor(t, user), map[string]any{
		"current_password": "not-the-password",
		"new_password":     "new-password",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusUnauthorized, rec.Code, rec.Body.String())
	}
	if code := signIn(t, env, "devops", "old-password"); code != http.StatusOK {
		t.Fatalf("a refused rotation changed the password anyway: %d", code)
	}
}

func TestChangingAPasswordNeedsASession(t *testing.T) {
	env := newTestEnv(t)
	env.store.addUser("devops", "old-password", db.RoleUser)

	rec := env.do(t, http.MethodPost, passwordPath, "", map[string]any{
		"current_password": "old-password",
		"new_password":     "new-password",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

// One policy, not two: the rule is the one an account is created under.
func TestChangingAPasswordEnforcesTheCreationPolicy(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "old-password", db.RoleUser)

	rec := env.do(t, http.MethodPost, passwordPath, env.tokenFor(t, user), map[string]any{
		"current_password": "old-password",
		"new_password":     "short",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

// A rotation that rotates nothing is refused rather than reported as done.
func TestChangingAPasswordRefusesTheSameOne(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "old-password", db.RoleUser)

	rec := env.do(t, http.MethodPost, passwordPath, env.tokenFor(t, user), map[string]any{
		"current_password": "old-password",
		"new_password":     "old-password",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

// A federated account is told where its password actually lives, rather than
// being offered a form that cannot work.
func TestAFederatedAccountIsToldItHasNoPasswordHere(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("sso-user", "irrelevant", db.RoleUser)
	user.AuthSource = db.ProtocolOIDC

	rec := env.do(t, http.MethodPost, passwordPath, env.tokenFor(t, user), map[string]any{
		"current_password": "irrelevant",
		"new_password":     "new-password",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

// A machine account holds no password by construction; its credential is a
// stored token with a revoke of its own.
func TestAMachineAccountHasNoPasswordToChange(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("ci-bot", "irrelevant", db.RoleUser)
	user.AccountType = db.AccountTypeMachine

	rec := env.do(t, http.MethodPost, passwordPath, env.tokenFor(t, user), map[string]any{
		"current_password": "irrelevant",
		"new_password":     "new-password",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

// A disabled account never reaches an authenticated route at all.
func TestADisabledAccountCannotChangeItsPassword(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "old-password", db.RoleUser)
	token := env.tokenFor(t, user)
	env.store.users[user.ID].IsActive = false

	rec := env.do(t, http.MethodPost, passwordPath, token, map[string]any{
		"current_password": "old-password",
		"new_password":     "new-password",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

// The rotation is in the trail, and the password is in no part of it.
func TestChangingAPasswordIsAudited(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	user := env.store.addUser("devops", "old-password", db.RoleUser)

	env.do(t, http.MethodPost, passwordPath, env.tokenFor(t, user), map[string]any{
		"current_password": "old-password",
		"new_password":     "new-password",
	})

	events := auditor.all()
	if len(events) != 1 || events[0].Verb != verbPasswordChange {
		t.Fatalf("expected one %s record, got %+v", verbPasswordChange, events)
	}
	if events[0].Username != "devops" || events[0].UserID != user.ID {
		t.Fatalf("the record does not name who rotated: %+v", events[0])
	}
}

// Rotating because you think it leaked should be able to take the kubeconfigs
// with it — offered, not silent.
func TestChangingAPasswordCanTakeTheKubeconfigsWithIt(t *testing.T) {
	auditor := &recordingAuditor{}
	env := newTestEnvWith(t, func(o *Options) { o.Auditor = auditor })
	user := env.store.addUser("devops", "old-password", db.RoleUser)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")
	grantView(env.store, user.ID, cluster.ID)

	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, user), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("generate failed: %d (%s)", rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodPost, passwordPath, env.tokenFor(t, user), map[string]any{
		"current_password":   "old-password",
		"new_password":       "new-password",
		"revoke_kubeconfigs": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[changePasswordResponse](t, rec)
	if body.Credentials == nil || body.Credentials.Revoked != 1 {
		t.Fatalf("expected one revoked credential, got %+v", body.Credentials)
	}
	if env.store.issuances[0].RevokedAt == nil {
		t.Fatal("the register row was not marked revoked")
	}
	// The gateway must refuse the withdrawn credential from here, not from the
	// next refresh tick.
	if !env.issued.Snapshot().Revoked(env.store.issuances[0].TokenID) {
		t.Fatal("the revocation was not published to the gateway")
	}

	verbs := map[string]int{}
	for _, event := range auditor.all() {
		verbs[event.Verb]++
	}
	if verbs[verbPasswordChange] != 1 || verbs[verbKubeconfigRevoke] != 1 {
		t.Fatalf("expected both acts in the trail, got %+v", verbs)
	}
}

// Not asking for it leaves them alone: somebody rotating on a schedule does not
// want every laptop in the team to stop working.
func TestChangingAPasswordLeavesKubeconfigsAloneByDefault(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "old-password", db.RoleUser)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_token")
	grantView(env.store, user.ID, cluster.ID)

	env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, user), nil)
	rec := env.do(t, http.MethodPost, passwordPath, env.tokenFor(t, user), map[string]any{
		"current_password": "old-password",
		"new_password":     "new-password",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if body := decode[changePasswordResponse](t, rec); body.Credentials != nil {
		t.Fatalf("a rotation that was not asked to revoke reported a revoke: %+v", body.Credentials)
	}
	if env.store.issuances[0].RevokedAt != nil {
		t.Fatal("the credential was revoked without being asked for")
	}
}
