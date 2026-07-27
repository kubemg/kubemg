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
