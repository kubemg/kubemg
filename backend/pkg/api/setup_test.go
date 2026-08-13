package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The sign-in page asks this before anybody has a session, so it has to answer
// without one — and it has to answer nothing else. A server describing its own
// configuration to an unauthenticated caller would be reconnaissance.
func TestSetupStateIsPublicAndSaysOnlyWhetherSetupIsNeeded(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, http.MethodGet, "/api/v1/setup/state", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !decode[setupStateResponse](t, rec).Required {
		t.Fatal("a database with no completion stamp needs setup")
	}

	if payload := decode[map[string]any](t, rec); len(payload) != 1 {
		t.Fatalf("the public setup state must carry one field, got %v", payload)
	}
}

// An install that predates the wizard is stamped complete at boot, and the same
// stamp is what a finished wizard writes. Either way it is the only signal.
func TestSetupStateFollowsTheCompletionStamp(t *testing.T) {
	env := newTestEnv(t)
	env.store.settings[db.SettingSetupCompletedAt] = "2026-01-01T00:00:00Z"

	rec := env.do(t, http.MethodGet, "/api/v1/setup/state", "", nil)
	if decode[setupStateResponse](t, rec).Required {
		t.Fatal("a stamped install must never be sent back through setup")
	}
}

func TestSetupPreflightIsAdminOnly(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)

	rec := env.do(t, http.MethodGet, "/api/v1/setup/preflight", env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

// Preflight's whole job is to disclose what the wizard cannot write. A server
// with no TLS cannot serve kubectl at all, which is the one state worth calling
// blocked rather than merely warning about.
func TestSetupPreflightReportsWhatTheWizardCannotFix(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodGet, "/api/v1/setup/preflight", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[setupPreflightResponse](t, rec)
	tls, ok := checkByKey(body.Checks, "tls")
	if !ok {
		t.Fatalf("expected a tls check, got %v", body.Checks)
	}
	if tls.Severity != checkBlocked {
		t.Fatalf("a bastion without TLS cannot serve kubectl; expected %q, got %q",
			checkBlocked, tls.Severity)
	}
	if !strings.Contains(tls.Fix, "KUBEMG_TLS_ENABLED") {
		t.Fatalf("a check the operator has to fix outside the console must say what to set, got %q", tls.Fix)
	}

	// Recording is off in the default test stack, and that is a gap in the audit
	// trail worth saying out loud rather than a fault.
	recording, ok := checkByKey(body.Checks, "recording")
	if !ok || recording.Severity != checkWarn {
		t.Fatalf("expected a recording warning, got %v", body.Checks)
	}
}

// A self-signed certificate is a decision, not a fault: agents connect fine
// because it is pinned into their install package, and that pinning is exactly
// why replacing it later is expensive.
func TestSetupPreflightSeparatesSelfSignedFromSupplied(t *testing.T) {
	selfSigned := newTestEnvWith(t, func(o *Options) {
		o.Deployment = Deployment{TLSEnabled: true, TLSSelfSigned: true, TLSCertFile: "/etc/kubemg/tls/tls.crt"}
	})
	admin := selfSigned.store.addUser("admin", "pw", db.RoleAdmin)
	body := decode[setupPreflightResponse](t,
		selfSigned.do(t, http.MethodGet, "/api/v1/setup/preflight", selfSigned.tokenFor(t, admin), nil))
	if check, ok := checkByKey(body.Checks, "tls"); !ok || check.Severity != checkWarn {
		t.Fatalf("expected a self-signed warning, got %v", body.Checks)
	}

	supplied := newTestEnvWith(t, func(o *Options) {
		o.Deployment = Deployment{TLSEnabled: true}
	})
	admin = supplied.store.addUser("admin", "pw", db.RoleAdmin)
	body = decode[setupPreflightResponse](t,
		supplied.do(t, http.MethodGet, "/api/v1/setup/preflight", supplied.tokenFor(t, admin), nil))
	if check, ok := checkByKey(body.Checks, "tls"); !ok || check.Severity != checkOK {
		t.Fatalf("a real certificate is not something to warn about, got %v", body.Checks)
	}
}

// The refusal that makes "setup complete" mean something. Everything else the
// wizard collects is a preference; this is the difference between a bastion that
// has been set up and one that only looks like it.
func TestSetupCannotFinishWhileTheSeededPasswordStillWorks(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	env.store.settings[db.SettingBootstrapAdminID] = strconv.FormatUint(uint64(admin.ID), 10)

	rec := env.do(t, http.MethodPost, "/api/v1/setup/complete", token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
	if !decode[setupStateResponse](t,
		env.do(t, http.MethodGet, "/api/v1/setup/state", "", nil)).Required {
		t.Fatal("a refused completion must not have stamped anything")
	}

	// Changing that password is what opens the gate, and it opens it from the
	// ordinary user endpoint rather than from a second path of its own.
	changed := env.do(t, http.MethodPut, "/api/v1/users/"+strconv.FormatUint(uint64(admin.ID), 10),
		token, map[string]any{"password": "a-real-password"})
	if changed.Code != http.StatusOK {
		t.Fatalf("expected the password change to succeed, got %d (%s)", changed.Code, changed.Body.String())
	}

	rec = env.do(t, http.MethodPost, "/api/v1/setup/complete", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if decode[setupStateResponse](t,
		env.do(t, http.MethodGet, "/api/v1/setup/state", "", nil)).Required {
		t.Fatal("setup should be finished")
	}
}

// A marker naming an account nobody can sign in as is not a password still in
// force, and it must not be able to wedge setup shut.
func TestSetupIgnoresAMarkerForAnAccountThatIsGone(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	env.store.settings[db.SettingBootstrapAdminID] = "9999"

	rec := env.do(t, http.MethodPost, "/api/v1/setup/complete", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
}

// Finishing twice is a double-submit, not a failure, and it must not move the
// timestamp that records when setup actually finished.
func TestCompletingSetupTwiceIsHarmless(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	if rec := env.do(t, http.MethodPost, "/api/v1/setup/complete", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	first := env.store.settings[db.SettingSetupCompletedAt]

	if rec := env.do(t, http.MethodPost, "/api/v1/setup/complete", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if env.store.settings[db.SettingSetupCompletedAt] != first {
		t.Fatal("the stamp records when setup finished, so a repeat must not move it")
	}
}

func checkByKey(checks []setupCheck, key string) (setupCheck, bool) {
	for _, check := range checks {
		if check.Key == key {
			return check, true
		}
	}
	return setupCheck{}, false
}
