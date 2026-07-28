package api

import (
	"net/http"
	"testing"
)

// The custom-resource route is the one place a caller names an API instead of
// picking a kind from the fixed table, so what is pinned here is the boundary
// that keeps naming an API from meaning reaching one: the path is built from
// three validated components, the core group is not one of them, and a
// namespace-scoped grant is still refused a cluster-wide read.

func TestCustomResourcePathRejectsAnythingButAnAPI(t *testing.T) {
	cases := []struct {
		name                     string
		group, version, resource string
	}{
		{"empty", "", "", ""},
		// The core group has no dot. Its lists are served by handlers that redact
		// before answering, and this route must not become the way around them.
		{"core group", "v1", "v1", "secrets"},
		{"traversal in group", "../../api", "v1", "secrets"},
		{"traversal in resource", "networking.istio.io", "v1", "../../../secrets"},
		{"slash in resource", "networking.istio.io", "v1", "pods/exec"},
		{"subresource", "networking.istio.io", "v1", "gateways/status"},
		{"query smuggled in", "networking.istio.io", "v1", "gateways?watch=true"},
		{"uppercase group", "Networking.Istio.IO", "v1", "gateways"},
		{"not a version", "networking.istio.io", "latest", "gateways"},
		{"absolute path", "/apis/networking.istio.io", "v1", "gateways"},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := customResourcePath(test.group, test.version, test.resource); err == nil {
				t.Fatalf("expected %q/%q/%q to be refused",
					test.group, test.version, test.resource)
			}
		})
	}
}

func TestCustomResourcePathBuildsTheAPIPath(t *testing.T) {
	path, err := customResourcePath("networking.istio.io", "v1beta1", "destinationrules")
	if err != nil {
		t.Fatalf("expected a valid API to be accepted: %v", err)
	}

	if got, want := path.clusterWide(),
		"/apis/networking.istio.io/v1beta1/destinationrules"; got != want {
		t.Fatalf("cluster-wide path = %q, want %q", got, want)
	}
	if got, want := path.namespaced("istio-system"),
		"/apis/networking.istio.io/v1beta1/namespaces/istio-system/destinationrules"; got != want {
		t.Fatalf("namespaced path = %q, want %q", got, want)
	}
}

func TestParseCustomKind(t *testing.T) {
	path, ok := parseCustomKind("crd:networking.istio.io/v1/gateways")
	if !ok {
		t.Fatal("expected a well-formed custom kind to parse")
	}
	if got, want := path.clusterWide(), "/apis/networking.istio.io/v1/gateways"; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}

	// A key that is not a custom kind, or that is one but names something other
	// than an API, resolves to nothing rather than to a path.
	for _, key := range []string{
		"pods",
		"crd:",
		"crd:networking.istio.io/v1",
		"crd:networking.istio.io/v1/gateways/extra",
		"crd:v1/v1/secrets",
		"crd:../../api/v1/secrets",
	} {
		if _, ok := parseCustomKind(key); ok {
			t.Fatalf("expected %q to be refused", key)
		}
	}
}

// customObjectKind decides namespacing from whether a namespace was named,
// which is what puts a cluster-scoped custom resource in front of the same
// cluster-scope check every other cluster-wide read passes.
func TestCustomObjectKindNamespacing(t *testing.T) {
	namespaced, ok := customObjectKind("crd:networking.istio.io/v1/gateways", "istio-system")
	if !ok || !namespaced.namespaced {
		t.Fatal("expected a named namespace to give a namespaced kind")
	}
	if !namespaced.writable {
		t.Fatal("a custom resource round-trips like a built-in one and should be writable")
	}

	clustered, ok := customObjectKind("crd:cert-manager.io/v1/clusterissuers", "")
	if !ok || clustered.namespaced {
		t.Fatal("expected no namespace to give a cluster-scoped kind")
	}

	if _, ok := customObjectKind("clusterroles", "default"); ok {
		t.Fatal("expected a non-custom key to be refused")
	}
}

func TestCustomResourceListRefusesBadAPI(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	base := "/api/v1/clusters/" + itoa(cluster.ID) + "/resources/custom"
	for _, query := range []string{
		"",
		"?group=v1&version=v1&plural=secrets",
		"?group=networking.istio.io&version=v1&plural=gateways%2Fstatus",
		"?group=networking.istio.io&version=nope&plural=gateways",
	} {
		rec := env.do(t, http.MethodGet, base+query, token, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q: expected status %d, got %d (%s)",
				query, http.StatusBadRequest, rec.Code, rec.Body.String())
		}
	}
}

// A cluster-scoped custom resource reaches past a namespace-scoped grant exactly
// as nodes or persistent volumes do, and is refused for the same reason.
func TestCustomResourceListRefusesClusterScopeForScopedGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", []string{"team-a"})
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+
			"/resources/custom?group=cert-manager.io&version=v1&plural=clusterissuers&scope=cluster",
		token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)",
			http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

// A namespaced custom resource is subject to the same namespace enforcement as
// every other namespaced list: a namespace outside the grant is refused before
// anything reaches the tunnel.
func TestCustomResourceListRefusesNamespaceOutsideGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+
			"/resources/custom?group=networking.istio.io&version=v1&plural=gateways&namespace=team-b",
		token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)",
			http.StatusForbidden, rec.Code, rec.Body.String())
	}
}
