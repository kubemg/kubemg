package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The manifest surface is the first write path in the resource API, so what is
// pinned here is the chain that stands in front of the tunnel: only kinds from
// the fixed table can be addressed, a scoped grant cannot reach past its
// namespaces, and a manifest cannot rename or move the object it replaces.

func TestResourceObjectRefusesUnknownKind(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	// "componentstatuses" is a real Kubernetes resource and deliberately not one
	// of ours: the editor addresses the inventory the sidebar browses, not the
	// API. (It used to be "clusterroles", until the Access section made that one
	// browsable — which is the point: this list has to name something the
	// sidebar genuinely does not carry.)
	for _, kind := range []string{"", "componentstatuses", "../secrets"} {
		rec := env.do(t, http.MethodGet,
			"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/object?kind="+kind+"&name=x", token, nil)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("kind %q: expected status %d, got %d (%s)",
				kind, http.StatusBadRequest, rec.Code, rec.Body.String())
		}
	}
}

func TestResourceObjectRequiresName(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/object?kind=pods&namespace=default",
		token, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d without a name, got %d (%s)",
			http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestResourceObjectRefusesScopeViolations(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", []string{"team-a"})
	token := env.tokenFor(t, user)

	cases := []struct {
		name  string
		query string
	}{
		// A cluster-scoped object is outside a namespace-scoped grant entirely.
		{"cluster scoped kind", "kind=nodes&name=node-1"},
		// A namespaced one has to name a namespace the grant covers.
		{"namespace outside grant", "kind=configmaps&name=app&namespace=team-b"},
	}

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		for _, test := range cases {
			t.Run(method+" "+test.name, func(t *testing.T) {
				rec := env.do(t, method,
					"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/object?"+test.query,
					token, map[string]string{"yaml": "kind: ConfigMap\n"})

				if rec.Code != http.StatusForbidden {
					t.Fatalf("expected status %d, got %d (%s)",
						http.StatusForbidden, rec.Code, rec.Body.String())
				}
			})
		}
	}
}

func TestResourceObjectRefusesDirectClusters(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addCluster("legacy", "dev") // direct mode
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/object?kind=pods&name=web&namespace=default",
		token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d for a direct-mode cluster, got %d (%s)",
			http.StatusConflict, rec.Code, rec.Body.String())
	}
}

func TestResourceObjectRequiresAuth(t *testing.T) {
	env := newTestEnv(t)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/object?kind=pods&name=web", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d without a token, got %d", http.StatusUnauthorized, rec.Code)
	}
}

// A Secret's values are redacted on the way out, so the manifest KubeMG showed
// is not the whole object. Writing it back would replace every value with the
// placeholder standing in for it, which is why the write is refused outright
// rather than left to the cluster's RBAC to allow.
func TestResourceObjectRefusesWritingSecrets(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodPut,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/object?kind=secrets&name=creds&namespace=default",
		token, map[string]string{"yaml": "apiVersion: v1\nkind: Secret\nmetadata:\n  name: creds\n"})

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d writing a Secret, got %d (%s)",
			http.StatusConflict, rec.Code, rec.Body.String())
	}
	if body := decode[map[string]string](t, rec); !strings.Contains(body["error"], "redacted") {
		t.Fatalf("expected the refusal to explain the redaction, got %q", body["error"])
	}
}

// A manifest that names a different object would be a rename or a move, and
// both create something new rather than changing what was opened.
func TestResourceObjectRefusesMismatchedManifest(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	cases := []struct {
		name     string
		manifest string
		wants    string
	}{
		{"not yaml", "\tapiVersion: :\n  - [", "valid YAML"},
		{"empty", "   ", "empty"},
		{"no apiVersion", "kind: ConfigMap\nmetadata:\n  name: app\n", "apiVersion"},
		{"no kind", "apiVersion: v1\nmetadata:\n  name: app\n", "kind"},
		{
			"renamed",
			"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: other\n  namespace: default\n",
			"renaming",
		},
		{
			"moved",
			"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: elsewhere\n",
			"moving",
		},
		{
			// The path is built from the kind table, so a manifest cannot point
			// the write at an API this resource is not served by.
			"foreign apiVersion",
			"apiVersion: apps/v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: default\n",
			"not the API",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			rec := env.do(t, http.MethodPut,
				"/api/v1/clusters/"+itoa(cluster.ID)+
					"/resources/object?kind=configmaps&name=app&namespace=default",
				token, map[string]string{"yaml": test.manifest})

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d (%s)",
					http.StatusBadRequest, rec.Code, rec.Body.String())
			}
			if body := decode[map[string]string](t, rec); !strings.Contains(body["error"], test.wants) {
				t.Fatalf("expected the refusal to mention %q, got %q", test.wants, body["error"])
			}
		})
	}
}

// An accepted manifest is written to the path the kind table says it lives at,
// with the namespace and name of the object being replaced.
func TestObjectWritePath(t *testing.T) {
	cases := []struct {
		name      string
		key       string
		namespace string
		object    string
		want      string
	}{
		{
			"namespaced core", "configmaps", "team a",
			`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"app"}}`,
			"/api/v1/namespaces/team%20a/configmaps/app",
		},
		{
			"namespaced group", "deployments", "prod",
			`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"prod"}}`,
			"/apis/apps/v1/namespaces/prod/deployments/web",
		},
		{
			"cluster scoped", "nodes", "",
			`{"apiVersion":"v1","kind":"Node","metadata":{"name":"node-1"}}`,
			"/api/v1/nodes/node-1",
		},
		{
			// An optional CRD is served at more than one version; the manifest
			// decides which, as long as it is one this kind is served by.
			"older CRD version", "httproutes", "edge",
			`{"apiVersion":"gateway.networking.k8s.io/v1beta1","kind":"HTTPRoute","metadata":{"name":"api"}}`,
			"/apis/gateway.networking.k8s.io/v1beta1/namespaces/edge/httproutes/api",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var object map[string]any
			if err := json.Unmarshal([]byte(test.object), &object); err != nil {
				t.Fatalf("bad test fixture: %v", err)
			}

			name, _ := object["metadata"].(map[string]any)["name"].(string)
			got, reason := objectKinds[test.key].writePath(object, test.namespace, name)
			if reason != "" {
				t.Fatalf("unexpected refusal: %s", reason)
			}
			if got != test.want {
				t.Fatalf("expected path %q, got %q", test.want, got)
			}
		})
	}
}

// What the editor shows is the object minus the server's own bookkeeping —
// managed fields and kubectl's copy of the last applied manifest are most of the
// bytes and none of the meaning.
func TestRenderObjectStripsBookkeeping(t *testing.T) {
	body := []byte(`{
		"apiVersion":"apps/v1","kind":"Deployment",
		"metadata":{
			"name":"web","namespace":"prod","resourceVersion":"4821",
			"managedFields":[{"manager":"kubectl","operation":"Apply"}],
			"annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{}","team":"platform"}
		}
	}`)

	view, err := renderObject(body, objectKinds["deployments"])
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if strings.Contains(view.YAML, "managedFields") {
		t.Fatalf("managed fields survived into the manifest:\n%s", view.YAML)
	}
	if strings.Contains(view.YAML, "last-applied-configuration") {
		t.Fatalf("the last-applied copy survived into the manifest:\n%s", view.YAML)
	}
	// Stripping the noise must not take a real annotation with it.
	if !strings.Contains(view.YAML, "team: platform") {
		t.Fatalf("a real annotation was dropped:\n%s", view.YAML)
	}
	if view.Name != "web" || view.Namespace != "prod" || view.ResourceVersion != "4821" {
		t.Fatalf("unexpected identity: %+v", view)
	}
	if !view.Editable {
		t.Fatalf("a Deployment should be editable")
	}
}

// A Secret's value never enters a response, on this surface as on every other.
func TestRenderObjectRedactsSecretValues(t *testing.T) {
	body := []byte(`{
		"apiVersion":"v1","kind":"Secret",
		"metadata":{"name":"creds","namespace":"default"},
		"type":"Opaque",
		"data":{"password":"aHVudGVyMg=="},
		"stringData":{"token":"plaintext"}
	}`)

	view, err := renderObject(body, objectKinds["secrets"])
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	for _, secret := range []string{"aHVudGVyMg==", "plaintext"} {
		if strings.Contains(view.YAML, secret) {
			t.Fatalf("a secret value reached the response:\n%s", view.YAML)
		}
	}
	// The keys are inventory and stay: knowing a Secret has a "password" is not
	// knowing the password.
	if !strings.Contains(view.YAML, "password:") || !strings.Contains(view.YAML, "token:") {
		t.Fatalf("the keys were dropped along with the values:\n%s", view.YAML)
	}
	if view.Editable {
		t.Fatalf("a redacted Secret must not be offered as editable")
	}
	if view.Reason == "" {
		t.Fatalf("a read-only manifest has to say why")
	}
}

func TestGroupPath(t *testing.T) {
	// The core group is the one without a slash, and it is the only one served
	// under /api rather than /apis.
	if got := groupPath("v1"); got != "/api/v1" {
		t.Fatalf("expected /api/v1, got %q", got)
	}
	if got := groupPath("apps/v1"); got != "/apis/apps/v1" {
		t.Fatalf("expected /apis/apps/v1, got %q", got)
	}
}

// A guardrail refusal on the manifest write must carry the same
// guardrail_blocked/policy/scope fields the streaming path has always
// surfaced, so the editor's confirmation step can explain a refusal against
// the rule that caused it rather than showing an indistinguishable 403. This
// is the write path's half of that fix; TestGlobalGuardrailBlocksAProxiedDelete
// in guardrails_test.go is the streaming path's, already in place before this
// change.
func TestResourceObjectWriteRefusedByGuardrailSurfacesPolicy(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	createPolicy(t, env, token, map[string]any{
		"name":    "no configmap writes",
		"pattern": `^PUT /api/v1/namespaces/default/configmaps/`,
		"target":  db.GuardrailTargetAPIRequest,
	})

	rec := env.do(t, http.MethodPut,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/object?kind=configmaps&name=app&namespace=default",
		token, map[string]string{
			"yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: default\n",
		})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}

	body := decode[struct {
		Error  string `json:"error"`
		Guard  bool   `json:"guardrail_blocked"`
		Policy string `json:"policy"`
		Scope  string `json:"scope"`
	}](t, rec)
	if !body.Guard {
		t.Fatalf("the refusal must be marked as a guardrail: %s", rec.Body.String())
	}
	if body.Policy != "no configmap writes" {
		t.Fatalf("the refusal must name the rule, got %q", body.Policy)
	}
	if body.Scope != "global" {
		t.Fatalf("expected the global scope, got %q", body.Scope)
	}
}

// A refusal that never reaches the guardrail engine at all — a cluster with no
// agent attached — must not carry the guardrail fields; a client should only
// see guardrail_blocked when a policy is actually what stopped it.
func TestResourceObjectWriteWithoutGuardrailHasNoGuardrailFields(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodPut,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/object?kind=configmaps&name=app&namespace=default",
		token, map[string]string{
			"yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: default\n",
		})
	// No agent is attached, so this reaches the tunnel lookup and stops there.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "guardrail_blocked") {
		t.Fatalf("a plain tunnel failure must not claim to be a guardrail: %s", rec.Body.String())
	}
}

// previewResourceObjectDiff shares the manifest write's own request validation
// — unknown kind, redacted kind, unparsable YAML — since it is the same
// question asked one step earlier: what would this manifest do, rather than
// having it actually done.
func TestPreviewResourceObjectDiffValidatesInput(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	// A Secret is redacted and non-writable, so there is nothing to preview:
	// the same 409 the write path gives, for the same reason.
	rec := env.do(t, http.MethodPost,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/object/diff?kind=secrets&name=creds&namespace=default",
		token, map[string]string{"yaml": "apiVersion: v1\nkind: Secret\nmetadata:\n  name: creds\n"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d previewing a Secret, got %d (%s)",
			http.StatusConflict, rec.Code, rec.Body.String())
	}

	// Unparsable YAML is refused before any read of the cluster happens.
	rec = env.do(t, http.MethodPost,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/object/diff?kind=configmaps&name=app&namespace=default",
		token, map[string]string{"yaml": "\tapiVersion: :\n  - ["})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d for invalid YAML, got %d (%s)",
			http.StatusBadRequest, rec.Code, rec.Body.String())
	}

	// A manifest naming a different object is refused for the same reason the
	// write is: previewing a rename answers a question about an object that
	// would not actually be the one replaced.
	rec = env.do(t, http.MethodPost,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/object/diff?kind=configmaps&name=app&namespace=default",
		token, map[string]string{
			"yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: other\n  namespace: default\n",
		})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d for a renamed object, got %d (%s)",
			http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	if body := decode[map[string]string](t, rec); !strings.Contains(body["error"], "renaming") {
		t.Fatalf("expected the refusal to mention renaming, got %q", body["error"])
	}
}

// A valid preview request reaches the pre-image read, which — with no agent
// attached — stops at the tunnel lookup exactly like the write does. That is
// the proof this endpoint goes through the same impersonated path as
// everything else rather than answering from something cached or invented.
func TestPreviewResourceObjectDiffReachesTheTunnel(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", db.RoleAdmin)
	token := env.tokenFor(t, admin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodPost,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/object/diff?kind=configmaps&name=app&namespace=default",
		token, map[string]string{
			"yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n  namespace: default\n",
		})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	}
}
