package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func TestAgentURLIsWhatInstallPackagesCarry(t *testing.T) {
	env := newTestEnvWith(t, func(opts *Options) { opts.AgentURL = "https://agents.example.com/" })
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")

	install := mintInstall(t, env, admin, cluster.ID)
	if install.BastionURL != "https://agents.example.com" {
		t.Fatalf("bastion-url = %q, want the agent URL", install.BastionURL)
	}
	if !strings.Contains(install.Manifest, `"https://agents.example.com"`) {
		t.Fatal("the rendered manifest does not carry the agent URL")
	}
	for _, link := range []string{install.ManifestURL, install.ArchiveURL, install.ApplyCommand} {
		if !strings.Contains(link, "https://agents.example.com/install/") {
			t.Errorf("%q is not on the agent URL", link)
		}
	}

	// Kubeconfigs point kubectl at the console's address, which the agent URL
	// does not move.
	rec := env.do(t, http.MethodPost, generatePath(cluster.ID), env.tokenFor(t, admin), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("kubeconfig: status %d (%s)", rec.Code, rec.Body.String())
	}
	if server := decode[generateKubeconfigResponse](t, rec).Server; !strings.HasPrefix(server, "https://kubemg.example.com/") {
		t.Fatalf("kubeconfig points at %q, want the public URL", server)
	}
}

func TestWithoutAnAgentURLInstallsFollowThePublicURL(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")

	install := mintInstall(t, env, admin, cluster.ID)
	if install.BastionURL != "https://kubemg.example.com" ||
		!strings.HasPrefix(install.ManifestURL, "https://kubemg.example.com/install/") {
		t.Fatalf("install is on %q / %q, want the public URL", install.BastionURL, install.ManifestURL)
	}
}

func TestAgentListenerAnswersOnlyTheTunnelAndTheInstallDownloads(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")
	install := mintInstall(t, env, admin, cluster.ID)
	listener := AgentListenerHandler(env.router)
	token := env.tokenFor(t, admin)

	serve := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		listener.ServeHTTP(rec, req)
		return rec
	}

	if rec := serve(http.MethodGet, pathOf(install.ManifestURL)); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "kind: Deployment") {
		t.Fatalf("install manifest: status %d (%s)", rec.Code, rec.Body.String())
	}
	// A dial without a registration token is refused by the tunnel itself,
	// which is the route answering rather than the listener's 404.
	if rec := serve(http.MethodGet, "/agent/v1/tunnel"); rec.Code == http.StatusNotFound {
		t.Fatal("the tunnel route is not served")
	}

	for _, refused := range []struct{ method, path string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/login"},
		{http.MethodPost, "/api/v1/auth/login"},
		{http.MethodGet, "/api/v1/auth/me"},
		{http.MethodGet, "/api/v1/settings"},
		{http.MethodGet, "/api/v1/clusters/" + itoa(cluster.ID) + "/proxy/api/v1/pods"},
		{http.MethodGet, "/api/v1/clusters/" + itoa(cluster.ID) + "/kustomize"},
		{http.MethodGet, "/downloads/cli"},
		{http.MethodGet, "/metrics"},
		{http.MethodPost, "/agent/v1/tunnel"},
		{http.MethodGet, "/agent/v1/tunnel/"},
		{http.MethodPost, pathOf(install.ManifestURL)},
		{http.MethodGet, "/install/x/../../api/v1/settings"},
		{http.MethodGet, "/install/x/agent.yaml/../../../api/v1/auth/me"},
		{http.MethodGet, "/install/x/y/agent.yaml"},
		{http.MethodGet, "/install//agent.yaml"},
	} {
		if rec := serve(refused.method, refused.path); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: status %d, want 404", refused.method, refused.path, rec.Code)
		}
	}
}

// An agent attaches through the agent listener exactly as it does through the
// main one: the filter passes the upgrade through untouched.
func TestAgentAttachesThroughTheAgentListener(t *testing.T) {
	env := newTestEnv(t)
	cluster := env.store.addAgentCluster("edge-us", db.EnvStaging, "kmg_install-token")
	server := httptest.NewServer(AgentListenerHandler(env.router))
	t.Cleanup(server.Close)

	if _, err := dialAgent(t, server, "kmg_install-token"); err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitUntil(t, func() bool { return env.registry.Connected(cluster.ID) })

	resp, err := http.Post(server.URL+"/api/v1/auth/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("sign-in on the agent listener: status %d, want 404", resp.StatusCode)
	}
}
