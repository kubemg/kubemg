package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func TestSettingsReadBackEnvironmentDefaults(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodGet, "/api/v1/settings", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[settingsResponse](t, rec)
	if body.Effective.PublicURL != "https://kubemg.example.com" {
		t.Fatalf("expected the configured public URL, got %q", body.Effective.PublicURL)
	}
	if body.Overrides.PublicURL != "" {
		t.Fatalf("expected no stored override, got %q", body.Overrides.PublicURL)
	}
	if body.Defaults.PublicURL != "https://kubemg.example.com" {
		t.Fatalf("expected the default to be reported, got %q", body.Defaults.PublicURL)
	}
}

// The stored audit diff is a new class of retained data — a manifest body can
// carry values as sensitive as a Secret's without the object being one — so
// unlike the other switches on this page it must default off rather than on,
// and stay off until an admin opts in.
func TestRecordManifestDiffsDefaultsOff(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	body := decode[settingsResponse](t,
		env.do(t, http.MethodGet, "/api/v1/settings", token, nil))
	if body.Effective.RecordManifestDiffs {
		t.Fatal("recording manifest diffs must default off")
	}
	if body.Defaults.RecordManifestDiffs {
		t.Fatal("there is no environment default that could turn this on")
	}

	rec := env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{
		"record_manifest_diffs": true,
	})
	body = decode[settingsResponse](t, rec)
	if !body.Effective.RecordManifestDiffs {
		t.Fatal("expected the switch to turn the setting on")
	}

	rec = env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{
		"record_manifest_diffs": false,
	})
	body = decode[settingsResponse](t, rec)
	if body.Effective.RecordManifestDiffs {
		t.Fatal("expected the switch to turn the setting back off")
	}
}

func TestSettingsAreAdminOnly(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "pw", db.RoleUser)

	rec := env.do(t, http.MethodGet, "/api/v1/settings", env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

// The whole point of the setting: the install command an operator pastes into a
// cluster has to carry the address that cluster can reach.
func TestStoredPublicURLRewritesTheInstallCommand(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]string{
		"public_url": "https://bastion.corp.example/",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if got := decode[settingsResponse](t, rec).Effective.PublicURL; got != "https://bastion.corp.example" {
		t.Fatalf("expected the trailing slash stripped, got %q", got)
	}

	created := env.do(t, http.MethodPost, "/api/v1/clusters", token, agentClusterPayload())
	if created.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusCreated, created.Code, created.Body.String())
	}
	cluster := decode[clusterResponse](t, created)

	install := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID)+"/kustomize", token, nil)
	if install.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, install.Code, install.Body.String())
	}

	body := decode[agentInstallResponse](t, install)
	if body.BastionURL != "https://bastion.corp.example" {
		t.Fatalf("expected the stored URL in the manifest, got %q", body.BastionURL)
	}
	if !strings.Contains(body.ApplyCommand, "https://bastion.corp.example/install/") {
		t.Fatalf("expected the stored URL in the apply command, got %q", body.ApplyCommand)
	}
	if !strings.Contains(body.Manifest, "https://bastion.corp.example") {
		t.Fatalf("expected the stored URL inside the rendered manifest")
	}
}

func TestClearedSettingFallsBackToTheDefault(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]string{
		"agent_image": "registry.internal/kubemg-agent:9.9.9",
	})
	rec := env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]string{"agent_image": ""})

	body := decode[settingsResponse](t, rec)
	if body.Effective.AgentImage != "ghcr.io/kubemg/kubemg-agent:test" {
		t.Fatalf("expected the configured default back, got %q", body.Effective.AgentImage)
	}
}

func TestInvalidPublicURLIsRefused(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPut, "/api/v1/settings", env.tokenFor(t, admin), map[string]string{
		"public_url": "kubemg.example.com",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestLoopbackPublicURLIsAcceptedWithAWarning(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPut, "/api/v1/settings", env.tokenFor(t, admin), map[string]string{
		"public_url": "http://localhost:8080",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if len(decode[settingsResponse](t, rec).Warnings) == 0 {
		t.Fatalf("expected a warning about the loopback address")
	}
}

func TestAuditVerbSelectionRoundTrips(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	// No selection means every verb, which is the default and the only value that
	// cannot lose information.
	body := decode[settingsResponse](t,
		env.do(t, http.MethodGet, "/api/v1/settings", token, nil))
	if body.Effective.AuditVerbsSelected {
		t.Fatal("a fresh install has no verb selection")
	}

	rec := env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{
		"audit_verbs": []string{"delete", "create", "exec"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	body = decode[settingsResponse](t, rec)
	if !body.Effective.AuditVerbsSelected {
		t.Fatal("a selection was saved and should be reported as in force")
	}
	if got := strings.Join(body.Effective.AuditVerbs, ","); got != "create,delete,exec" {
		t.Fatalf("expected a sorted selection, got %q", got)
	}

	// Unticking every box means "back to everything", not "record nothing" — the
	// floor would keep recording refusals and sessions regardless, and a server
	// claiming to be silent would be lying about itself.
	rec = env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{
		"audit_verbs": []string{},
	})
	body = decode[settingsResponse](t, rec)
	if body.Effective.AuditVerbsSelected {
		t.Fatalf("an empty selection should clear it, got %v", body.Effective.AuditVerbs)
	}
}

func TestAuditVerbSelectionRefusesAnUnknownVerb(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPut, "/api/v1/settings", env.tokenFor(t, admin), map[string]any{
		"audit_verbs": []string{"delete", "telekinesis"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestRecordingRetentionDefaultsToAndIsCappedByTheAuditWindow(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	// Unset follows the audit window: a recording is evidence about a line in the
	// trail, so it must not outlive it.
	body := decode[settingsResponse](t,
		env.do(t, http.MethodGet, "/api/v1/settings", token, nil))
	if body.Effective.SessionRecordingRetentionDays != body.Effective.AuditRetentionDays {
		t.Fatalf("expected the recording window to default to the audit one, got %d vs %d",
			body.Effective.SessionRecordingRetentionDays, body.Effective.AuditRetentionDays)
	}

	// Shorter is the useful direction and is honoured.
	rec := env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{
		"audit_retention_days":             90,
		"session_recording_retention_days": 14,
	})
	body = decode[settingsResponse](t, rec)
	if body.Effective.SessionRecordingRetentionDays != 14 {
		t.Fatalf("expected 14 days of recordings, got %d",
			body.Effective.SessionRecordingRetentionDays)
	}

	// Longer than the trail is clamped rather than refused, because the audit
	// window is itself editable — shortening it has to pull recordings in with it.
	rec = env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{
		"session_recording_retention_days": 365,
	})
	body = decode[settingsResponse](t, rec)
	if body.Effective.SessionRecordingRetentionDays != 90 {
		t.Fatalf("expected the recording window clamped to the audit window, got %d",
			body.Effective.SessionRecordingRetentionDays)
	}

	// And when the audit window moves down, the stored 365 follows it rather than
	// becoming a validation error nobody can see.
	rec = env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{
		"audit_retention_days": 30,
	})
	body = decode[settingsResponse](t, rec)
	if body.Effective.SessionRecordingRetentionDays != 30 {
		t.Fatalf("expected the clamp to follow the audit window down, got %d",
			body.Effective.SessionRecordingRetentionDays)
	}
}

func TestRecordingSwitchCannotTurnRecordingOnWhereThereIsNone(t *testing.T) {
	// A server started without a recording directory has nowhere to write, and no
	// database row changes that. A switch that silently did nothing would be worse
	// than one that says why.
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPut, "/api/v1/settings", env.tokenFor(t, admin), map[string]any{
		"record_exec_sessions": true,
	})
	body := decode[settingsResponse](t, rec)
	if body.Effective.RecordingAvailable {
		t.Fatal("the default test stack has no recording directory")
	}
	if body.Effective.RecordExecSessions {
		t.Fatal("recording must stay off where the process cannot record")
	}
}

func TestRecordingSwitchTurnsRecordingOff(t *testing.T) {
	env := newTestEnvWith(t, func(opts *Options) {
		opts.RecordingDir = t.TempDir()
	})
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	body := decode[settingsResponse](t,
		env.do(t, http.MethodGet, "/api/v1/settings", token, nil))
	if !body.Effective.RecordExecSessions || !body.Effective.RecordingAvailable {
		t.Fatalf("a server with a recording directory records by default: %+v", body.Effective)
	}

	rec := env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{
		"record_exec_sessions": false,
	})
	body = decode[settingsResponse](t, rec)
	if body.Effective.RecordExecSessions {
		t.Fatal("the switch should have turned recording off")
	}
	if !body.Effective.RecordingAvailable {
		t.Fatal("the capability is still there; only the setting changed")
	}
}

// The ceiling on a generated kubeconfig is the one thing an administrator
// changes here that a *user* then relies on, so it has to move in both
// directions and it has to be bounded at both ends.
func TestKubeconfigCeilingDefaultsAndMoves(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	body := decode[settingsResponse](t,
		env.do(t, http.MethodGet, "/api/v1/settings", token, nil))
	if body.Effective.KubeconfigMaxTTLHours != 24 {
		t.Fatalf("expected a 24h default ceiling, got %d", body.Effective.KubeconfigMaxTTLHours)
	}
	if body.Overrides.KubeconfigMaxTTLHours != 0 {
		t.Fatalf("expected no stored override, got %d", body.Overrides.KubeconfigMaxTTLHours)
	}

	// A quarter, which is what "three months" means and the absolute bound.
	body = decode[settingsResponse](t, env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{
		"kubeconfig_max_ttl_hours": 2160,
	}))
	if body.Effective.KubeconfigMaxTTLHours != 2160 {
		t.Fatalf("expected the raised ceiling, got %d", body.Effective.KubeconfigMaxTTLHours)
	}
	// A raised ceiling is disclosed, because the two connection modes differ on
	// whether a long-lived credential can be withdrawn.
	if !hasWarningAbout(body.Warnings, "90 days") {
		t.Fatalf("expected a warning naming the window, got %v", body.Warnings)
	}

	// Lowering it below the default is the same decision as raising it.
	body = decode[settingsResponse](t, env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{
		"kubeconfig_max_ttl_hours": 8,
	}))
	if body.Effective.KubeconfigMaxTTLHours != 8 {
		t.Fatalf("expected an 8h ceiling, got %d", body.Effective.KubeconfigMaxTTLHours)
	}
	if hasWarningAbout(body.Warnings, "Kubeconfigs may be issued") {
		t.Fatal("a ceiling at or below the default needs no disclosure")
	}

	// Zero clears it back to the build's default, the same way every other
	// setting is cleared.
	body = decode[settingsResponse](t, env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{
		"kubeconfig_max_ttl_hours": 0,
	}))
	if body.Effective.KubeconfigMaxTTLHours != 24 || body.Overrides.KubeconfigMaxTTLHours != 0 {
		t.Fatalf("expected the override to be cleared, got %+v", body.Overrides)
	}
}

func TestKubeconfigCeilingIsBounded(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	token := env.tokenFor(t, admin)

	// Past the build's absolute bound, and below the floor a request is measured
	// against — a ceiling there would refuse every request.
	for _, hours := range []int{2161, -1} {
		rec := env.do(t, http.MethodPut, "/api/v1/settings", token, map[string]any{
			"kubeconfig_max_ttl_hours": hours,
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%d hours: expected status %d, got %d (%s)",
				hours, http.StatusBadRequest, rec.Code, rec.Body.String())
		}
	}
}

func hasWarningAbout(warnings []string, needle string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, needle) {
			return true
		}
	}
	return false
}
