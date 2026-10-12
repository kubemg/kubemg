package api

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// failSignIns spends an account's free attempts and the one that starts the lock.
func failSignIns(t *testing.T, env *testEnv, username string) {
	t.Helper()
	for i := 0; i <= auth.AccountThrottle.Free; i++ {
		if code := signIn(t, env, username, "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("failure %d: expected %d, got %d", i+1, http.StatusUnauthorized, code)
		}
	}
}

// A locked account is refused before its password is checked — the right one
// included, or the lock would still answer every guess, only more slowly.
func TestLoginIsThrottledAfterRepeatedFailures(t *testing.T) {
	env := newTestEnv(t)
	env.store.addUser("devops", "right-password", db.RoleUser)

	failSignIns(t, env, "devops")

	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": "devops", "password": "right-password",
	})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected %d while locked, got %d (%s)", http.StatusTooManyRequests, rec.Code, rec.Body.String())
	}
	if after, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || after <= 0 {
		t.Fatalf("a throttled answer must say when to retry, Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	// A case variant is the same account, not a fresh budget.
	if code := signIn(t, env, "DevOps", "right-password"); code != http.StatusTooManyRequests {
		t.Fatalf("a case variant of a locked name should be locked too, got %d", code)
	}
}

// The throttle must not become the enumeration oracle the 401 was built not to
// be: a name nobody holds is counted and locked exactly like one somebody does.
func TestLoginThrottleTreatsAnUnknownNameLikeAKnownOne(t *testing.T) {
	env := newTestEnv(t)
	env.store.addUser("devops", "right-password", db.RoleUser)

	failSignIns(t, env, "devops")
	failSignIns(t, env, "nobody")

	known := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "devops", "password": "x"})
	unknown := env.do(t, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "nobody", "password": "x"})
	if known.Code != unknown.Code || known.Body.String() != unknown.Body.String() {
		t.Fatalf("known and unknown names answered differently: %d %s / %d %s",
			known.Code, known.Body.String(), unknown.Code, unknown.Body.String())
	}
}

func TestSuccessfulLoginClearsTheAccountCount(t *testing.T) {
	env := newTestEnv(t)
	env.store.addUser("devops", "right-password", db.RoleUser)

	for i := 0; i < auth.AccountThrottle.Free; i++ {
		signIn(t, env, "devops", "wrong")
	}
	if code := signIn(t, env, "devops", "right-password"); code != http.StatusOK {
		t.Fatalf("the right password inside the free attempts should sign in, got %d", code)
	}
	// The count started over, so the full free allowance is back.
	for i := 0; i < auth.AccountThrottle.Free; i++ {
		if code := signIn(t, env, "devops", "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("failure %d after a success: expected %d, got %d", i+1, http.StatusUnauthorized, code)
		}
	}
}

// The current-password field is the stolen-session case, so it is throttled
// like a sign-in rather than left as an oracle for the real password.
func TestChangePasswordIsThrottled(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "right-password", db.RoleUser)
	token := env.tokenFor(t, user)

	for i := 0; i <= auth.AccountThrottle.Free; i++ {
		rec := env.do(t, http.MethodPost, passwordPath, token, map[string]any{
			"current_password": "wrong", "new_password": "new-password-123",
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: expected %d, got %d", i+1, http.StatusUnauthorized, rec.Code)
		}
	}
	rec := env.do(t, http.MethodPost, passwordPath, token, map[string]any{
		"current_password": "right-password", "new_password": "new-password-123",
	})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected %d while locked, got %d (%s)", http.StatusTooManyRequests, rec.Code, rec.Body.String())
	}
	if code := signIn(t, env, "devops", "right-password"); code != http.StatusOK {
		t.Fatalf("the change-password lock must not lock the sign-in, got %d", code)
	}
}
