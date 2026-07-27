package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func agentClusterPayload() map[string]string {
	return map[string]string{
		"name":            "edge-us",
		"environment":     db.EnvStaging,
		"description":     "Edge cluster behind a firewall",
		"connection_mode": db.ModeAgent,
	}
}

func TestCreateAgentClusterNeedsNoCredentials(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPost, "/api/v1/clusters", env.tokenFor(t, admin), agentClusterPayload())
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}

	body := decode[clusterResponse](t, rec)
	if body.ConnectionMode != db.ModeAgent {
		t.Fatalf("expected connection_mode %q, got %q", db.ModeAgent, body.ConnectionMode)
	}
	if body.Status != db.StatusPending {
		t.Fatalf("a freshly registered cluster should be pending, got %q", body.Status)
	}
	if body.AgentAttached {
		t.Fatal("no agent has connected yet")
	}

	stored := env.store.clusters[body.ID]
	if stored.AgentToken == "" {
		t.Fatal("registering in agent mode must mint a registration token")
	}
	if !strings.HasPrefix(stored.AgentToken, "kmg_") {
		t.Fatalf("unexpected token shape: %q", stored.AgentToken)
	}
	if stored.ServiceAccountToken != "" || stored.APIURL != "" {
		t.Fatal("an agent-mode cluster must not store cluster credentials")
	}
}

func TestCreateAgentClusterNeverLeaksItsToken(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPost, "/api/v1/clusters", env.tokenFor(t, admin), agentClusterPayload())
	body := decode[clusterResponse](t, rec)

	// The token is handed out by the installer endpoint, not by the inventory:
	// a non-admin listing clusters must never see it.
	stored := env.store.clusters[body.ID]
	if strings.Contains(rec.Body.String(), stored.AgentToken) {
		t.Fatalf("the registration token leaked into the cluster response: %s", rec.Body.String())
	}

	rec = env.do(t, http.MethodGet, "/api/v1/clusters", env.tokenFor(t, admin), nil)
	if strings.Contains(rec.Body.String(), stored.AgentToken) {
		t.Fatalf("the registration token leaked into the cluster list: %s", rec.Body.String())
	}
}

func TestCreateDirectClusterStillRequiresCredentials(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	payload := map[string]string{
		"name":            "prod-eu",
		"environment":     db.EnvProd,
		"connection_mode": db.ModeDirect,
	}

	rec := env.do(t, http.MethodPost, "/api/v1/clusters", env.tokenFor(t, admin), payload)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestCreateClusterDefaultsToDirectMode(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)

	rec := env.do(t, http.MethodPost, "/api/v1/clusters", env.tokenFor(t, admin), validClusterPayload())
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusCreated, rec.Code, rec.Body.String())
	}

	body := decode[clusterResponse](t, rec)
	if body.ConnectionMode != db.ModeDirect {
		t.Fatalf("a client that sends no mode should get direct, got %q", body.ConnectionMode)
	}
}

func TestClusterKustomizeRendersAnInstallPackage(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")

	rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID)+"/kustomize",
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[agentInstallResponse](t, rec)
	if body.AgentToken != "kmg_install-token" {
		t.Fatalf("expected the cluster's registration token, got %q", body.AgentToken)
	}
	if !strings.HasPrefix(body.ManifestURL, "https://kubemg.example.com/install/kmg_install-token/") {
		t.Fatalf("install URL is not built from the public URL: %q", body.ManifestURL)
	}
	if !strings.Contains(body.ApplyCommand, "kubectl apply -f "+body.ManifestURL) {
		t.Fatalf("unexpected apply command: %q", body.ApplyCommand)
	}
	if !strings.Contains(body.KustomizeCommand, "kubectl apply -k "+body.PackageDir) {
		t.Fatalf("unexpected kustomize command: %q", body.KustomizeCommand)
	}
	if !strings.Contains(body.Manifest, "kind: Deployment") {
		t.Fatal("the rendered manifest is missing the agent deployment")
	}
	if _, ok := body.Files["kustomization.yaml"]; !ok {
		t.Fatal("the package should include the kustomization for GitOps users")
	}
}

func TestClusterKustomizeYAMLFormat(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/kustomize?format=yaml", env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/yaml") {
		t.Fatalf("expected a YAML download, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), "kind: Secret") {
		t.Fatal("the YAML download is missing the registration secret")
	}
}

func TestClusterKustomizeRequiresAdmin(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("devops", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")

	rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID)+"/kustomize",
		env.tokenFor(t, user), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestClusterKustomizeRejectsADirectCluster(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID)+"/kustomize",
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

func TestInstallManifestIsFetchableWithTheTokenAlone(t *testing.T) {
	env := newTestEnv(t)
	env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")

	// kubectl cannot carry a KubeMG session; the token in the path is the
	// credential.
	rec := env.do(t, http.MethodGet, "/install/kmg_install-token/agent.yaml", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, want := range []string{"kind: Namespace", "kind: ServiceAccount", "kind: Secret", "kind: Deployment"} {
		if !strings.Contains(body, want) {
			t.Errorf("manifest is missing %q", want)
		}
	}
	if !strings.Contains(body, "kmg_install-token") {
		t.Fatal("the manifest must carry the registration token the agent will present")
	}
	if strings.Contains(body, "kind: Kustomization") {
		t.Fatal("a kustomization is not applyable with -f")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("an installer keyed by a secret must not be cacheable")
	}
}

func TestInstallArchiveIsATarball(t *testing.T) {
	env := newTestEnv(t)
	env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")

	rec := env.do(t, http.MethodGet, "/install/kmg_install-token/kustomize.tar.gz", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/gzip" {
		t.Fatalf("expected application/gzip, got %q", got)
	}
	// gzip magic number, so a caller piping straight into `tar -xz` succeeds.
	if body := rec.Body.Bytes(); len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
		t.Fatal("the archive is not gzipped")
	}
}

func TestInstallEndpointsRejectAnUnknownToken(t *testing.T) {
	env := newTestEnv(t)
	env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")

	for _, path := range []string{"/install/kmg_wrong/agent.yaml", "/install/kmg_wrong/kustomize.tar.gz"} {
		rec := env.do(t, http.MethodGet, path, "", nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: expected status %d, got %d", path, http.StatusNotFound, rec.Code)
		}
	}
}

func TestCheckAgentClusterUsesTheTunnelNotTheAPIServer(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")

	rec := env.do(t, http.MethodPost, "/api/v1/clusters/"+itoa(cluster.ID)+"/check",
		env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[clusterResponse](t, rec)
	if body.Status != db.StatusUnhealthy {
		t.Fatalf("a cluster with no agent attached is unreachable, got %q", body.Status)
	}
	if body.StatusMessage == "" {
		t.Fatal("an unreachable agent cluster should say why")
	}
	// Dialling an API server this cluster does not have would be meaningless.
	if env.health.calls != 0 {
		t.Fatal("an agent-mode cluster must not be probed over the direct path")
	}
}
