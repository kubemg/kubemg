package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// What is pinned here is the chain in front of the tunnel, exactly as the
// manifest write's tests pin it: which kinds may be created at all, that a
// manifest cannot land in a namespace nobody asked for, and — the one thing
// only this route can get wrong — that a create is posted to the *collection*
// rather than to an object path.

const configMapManifest = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n"

func createURL(clusterID uint, query string) string {
	return "/api/v1/clusters/" + itoa(clusterID) + "/resources/object?" + query
}

// The deny list is a product rule rather than a permission: the cluster's RBAC
// would happily let an admin create a ClusterRoleBinding, and kubemg still
// refuses to be the tool that authors it.
func TestCreateResourceObjectRefusesKindsItDoesNotAuthor(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	cases := []struct {
		kind  string
		query string
		yaml  string
	}{
		{"roles", "kind=roles&namespace=default", "apiVersion: rbac.authorization.k8s.io/v1\nkind: Role\nmetadata:\n  name: r\n"},
		{"rolebindings", "kind=rolebindings&namespace=default", "apiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n  name: r\n"},
		{"clusterroles", "kind=clusterroles", "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: r\n"},
		{"clusterrolebindings", "kind=clusterrolebindings", "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRoleBinding\nmetadata:\n  name: r\n"},
		{"nodes", "kind=nodes", "apiVersion: v1\nkind: Node\nmetadata:\n  name: n\n"},
	}

	for _, test := range cases {
		t.Run(test.kind, func(t *testing.T) {
			rec := env.do(t, http.MethodPost, createURL(cluster.ID, test.query),
				token, map[string]string{"yaml": test.yaml})
			if rec.Code != http.StatusConflict {
				t.Fatalf("expected status %d, got %d (%s)",
					http.StatusConflict, rec.Code, rec.Body.String())
			}
		})
	}
}

// A Secret is read-only in the editor because kubemg redacts what it read, which
// is a fact about a rendered manifest and not about one an operator typed — so
// creating one is allowed and must reach the tunnel rather than the 409 an
// unwritable kind gets.
func TestCreateResourceObjectAllowsSecrets(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodPost, createURL(cluster.ID, "kind=secrets&namespace=default"),
		token, map[string]string{"yaml": "apiVersion: v1\nkind: Secret\nmetadata:\n  name: creds\n"})
	// No agent is attached, so a request that passed every local check stops at
	// the tunnel lookup. That is the assertion: it got that far.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d (%s)",
			http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
}

func TestCreateResourceObjectRefusesUnknownKind(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	for _, kind := range []string{"", "componentstatuses", "../secrets"} {
		rec := env.do(t, http.MethodPost, createURL(cluster.ID, "kind="+kind+"&namespace=default"),
			token, map[string]string{"yaml": configMapManifest})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("kind %q: expected status %d, got %d (%s)",
				kind, http.StatusBadRequest, rec.Code, rec.Body.String())
		}
	}
}

func TestCreateResourceObjectValidatesManifest(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	cases := []struct {
		name string
		yaml string
	}{
		{"empty", "   "},
		{"not yaml", "kind: [ConfigMap"},
		{"not an object", "- one\n- two\n"},
		{"no apiVersion", "kind: ConfigMap\nmetadata:\n  name: app\n"},
		{"no kind", "apiVersion: v1\nmetadata:\n  name: app\n"},
		{"no metadata", "apiVersion: v1\nkind: ConfigMap\n"},
		{"no name", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  labels:\n    a: b\n"},
		// A different API is a different resource: the create is addressed by
		// the kind key the list was read under, and moving it elsewhere would
		// make that key a lie.
		{"foreign apiVersion", "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app\n"},
		// The namespace in the address is the one being created in; a manifest
		// naming another one is refused rather than quietly redirected.
		{"namespace mismatch", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: other\n"},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			rec := env.do(t, http.MethodPost, createURL(cluster.ID, "kind=configmaps&namespace=default"),
				token, map[string]string{"yaml": test.yaml})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d (%s)",
					http.StatusBadRequest, rec.Code, rec.Body.String())
			}
		})
	}
}

// generateName is the cluster naming the object, which is a name — so it has to
// satisfy the same check metadata.name does.
func TestCreateResourceObjectAcceptsGenerateName(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodPost, createURL(cluster.ID, "kind=jobs&namespace=default"),
		token, map[string]string{
			"yaml": "apiVersion: batch/v1\nkind: Job\nmetadata:\n  generateName: backfill-\n",
		})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected the request to reach the tunnel, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// "All namespaces" is a way of reading a list. There is nothing for it to mean
// when creating one object, and taking the grant's first namespace instead would
// be kubemg choosing where somebody's workload lands.
func TestCreateResourceObjectRefusesAllNamespaces(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodPost,
		createURL(cluster.ID, "kind=configmaps&namespace=default&all_namespaces=true"),
		token, map[string]string{"yaml": configMapManifest})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)",
			http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestCreateResourceObjectRefusesScopeViolations(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", db.RoleUser)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", []string{"team-a"})
	token := env.tokenFor(t, user)

	cases := []struct {
		name  string
		query string
		yaml  string
	}{
		{
			"cluster scoped kind",
			"kind=namespaces",
			"apiVersion: v1\nkind: Namespace\nmetadata:\n  name: team-b\n",
		},
		{"namespace outside grant", "kind=configmaps&namespace=team-b", configMapManifest},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			rec := env.do(t, http.MethodPost, createURL(cluster.ID, test.query),
				token, map[string]string{"yaml": test.yaml})
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected status %d, got %d (%s)",
					http.StatusForbidden, rec.Code, rec.Body.String())
			}
		})
	}
}

// The one thing only this route can get wrong: a create goes to the collection,
// not to a named object. A guardrail pattern is what makes the constructed path
// assertable without a cluster on the other end — the rule matches the request
// line the proxy is about to send.
func TestCreateResourceObjectPostsToTheCollectionPath(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	createPolicy(t, env, token, map[string]any{
		"name":    "collection path",
		"pattern": `^POST /api/v1/namespaces/default/configmaps$`,
		"target":  db.GuardrailTargetAPIRequest,
	})

	rec := env.do(t, http.MethodPost, createURL(cluster.ID, "kind=configmaps&namespace=default"),
		token, map[string]string{"yaml": configMapManifest})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected the pattern to match the request line, got %d (%s)",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "guardrail_blocked") {
		t.Fatalf("a guardrail refusal must say so: %s", rec.Body.String())
	}
}

// And the cluster-scoped half of the same assertion, which has no /namespaces/
// segment at all.
func TestCreateResourceObjectClusterScopedCollectionPath(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	createPolicy(t, env, token, map[string]any{
		"name":    "cluster collection path",
		"pattern": `^POST /api/v1/namespaces$`,
		"target":  db.GuardrailTargetAPIRequest,
	})

	rec := env.do(t, http.MethodPost, createURL(cluster.ID, "kind=namespaces"),
		token, map[string]string{"yaml": "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: team-c\n"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected the pattern to match the request line, got %d (%s)",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "guardrail_blocked") {
		t.Fatalf("a guardrail refusal must say so: %s", rec.Body.String())
	}
}

// A CRD-served kind is addressed by its API rather than by a table entry, and
// creating one has to build the same path the list read does.
func TestCreateResourceObjectCustomResourcePath(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	createPolicy(t, env, token, map[string]any{
		"name":    "custom collection path",
		"pattern": `^POST /apis/kafka.strimzi.io/v1beta2/namespaces/default/kafkatopics$`,
		"target":  db.GuardrailTargetAPIRequest,
	})

	rec := env.do(t, http.MethodPost,
		createURL(cluster.ID, "kind=crd:kafka.strimzi.io/v1beta2/kafkatopics&namespace=default"),
		token, map[string]string{
			"yaml": "apiVersion: kafka.strimzi.io/v1beta2\nkind: KafkaTopic\nmetadata:\n  name: orders\n",
		})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected the pattern to match the request line, got %d (%s)",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "guardrail_blocked") {
		t.Fatalf("a guardrail refusal must say so: %s", rec.Body.String())
	}
}
