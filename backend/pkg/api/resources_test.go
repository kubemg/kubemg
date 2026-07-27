package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The inventory routes all share one guard chain: the cluster has to be
// agent-based, the caller has to hold a grant on it, and a namespace-scoped
// grant cannot ask for a cluster-wide list. These tests pin that chain, which is
// what stands between a scoped user and objects from namespaces they were never
// given.

func TestClusterScopedResourcesRefuseScopedGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
	token := env.tokenFor(t, user)

	for _, resource := range []string{"nodes", "persistentvolumes", "storageclasses", "crds"} {
		t.Run(resource, func(t *testing.T) {
			rec := env.do(t, http.MethodGet,
				"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/"+resource, token, nil)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected status %d for a scoped grant, got %d (%s)",
					http.StatusForbidden, rec.Code, rec.Body.String())
			}
			// The refusal has to name the scope, or the operator has no way to
			// tell it apart from a cluster RBAC denial.
			if body := decode[map[string]string](t, rec); !strings.Contains(body["error"], "team-a") {
				t.Fatalf("expected the refusal to name the granted namespace, got %q", body["error"])
			}
		})
	}
}

func TestNamespacedResourcesRefuseNamespaceOutsideGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
	token := env.tokenFor(t, user)

	for _, resource := range []string{"services", "configmaps", "secrets", "jobs"} {
		t.Run(resource, func(t *testing.T) {
			rec := env.do(t, http.MethodGet,
				"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/"+resource+"?namespace=team-b",
				token, nil)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected status %d for a namespace outside the grant, got %d (%s)",
					http.StatusForbidden, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestInventoryRoutesRefuseDirectClusters(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addCluster("legacy", "dev") // direct mode
	token := env.tokenFor(t, admin)

	// Every list is read through a tunnel, so a direct-mode cluster has no live
	// state to offer and says so rather than half-answering.
	for _, resource := range []string{
		"deployments", "statefulsets", "daemonsets", "jobs", "cronjobs",
		"services", "ingresses", "httproutes", "virtualservices",
		"persistentvolumes", "persistentvolumeclaims", "storageclasses",
		"configmaps", "secrets", "crds", "nodes",
	} {
		t.Run(resource, func(t *testing.T) {
			rec := env.do(t, http.MethodGet,
				"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/"+resource+"?namespace=default",
				token, nil)

			if rec.Code != http.StatusConflict {
				t.Fatalf("expected status %d for a direct-mode cluster, got %d (%s)",
					http.StatusConflict, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestInventoryRoutesRequireAuth(t *testing.T) {
	env := newTestEnv(t)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/nodes", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d without a token, got %d", http.StatusUnauthorized, rec.Code)
	}
}

// An all-namespaces read is two different reads: one cluster-wide call for an
// unscoped grant, and one call per granted namespace for a scoped one. Getting
// that wrong either leaks objects past a grant or refuses a legitimate view.
func TestResourceScopeAllNamespaces(t *testing.T) {
	pods := resourceListPath{"/api/v1", "pods"}

	cases := []struct {
		name      string
		query     string
		grant     db.UserClusterAccess
		wantPaths []string
		wantAll   bool
		wantNS    string
		wantOK    bool
	}{
		{
			name:      "one namespace when none is asked for",
			query:     "?namespace=team-a",
			grant:     db.UserClusterAccess{},
			wantPaths: []string{"/api/v1/namespaces/team-a/pods"},
			wantNS:    "team-a",
			wantOK:    true,
		},
		{
			name:      "an unscoped grant reads the cluster in one call",
			query:     "?all_namespaces=true",
			grant:     db.UserClusterAccess{},
			wantPaths: []string{"/api/v1/pods"},
			wantAll:   true,
			wantOK:    true,
		},
		{
			name:  "a scoped grant reads only its own namespaces",
			query: "?all_namespaces=true",
			grant: db.UserClusterAccess{Namespaces: "team-a,team-b"},
			wantPaths: []string{
				"/api/v1/namespaces/team-a/pods",
				"/api/v1/namespaces/team-b/pods",
			},
			wantAll: true,
			wantOK:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodGet, "/resources/pods"+tc.query, nil)

			s := &server{}
			scope, ok := s.resourceScope(c, tc.grant)
			if ok != tc.wantOK {
				t.Fatalf("expected ok=%v, got %v (%s)", tc.wantOK, ok, rec.Body.String())
			}
			if scope.All != tc.wantAll {
				t.Fatalf("expected All=%v, got %v", tc.wantAll, scope.All)
			}
			if scope.Namespace != tc.wantNS {
				t.Fatalf("expected namespace %q, got %q", tc.wantNS, scope.Namespace)
			}

			paths := scope.paths(pods)
			if len(paths) != len(tc.wantPaths) {
				t.Fatalf("expected paths %v, got %v", tc.wantPaths, paths)
			}
			for i := range paths {
				if paths[i] != tc.wantPaths[i] {
					t.Fatalf("expected paths %v, got %v", tc.wantPaths, paths)
				}
			}
		})
	}
}

func TestResourceScopeRefusesWideFanOut(t *testing.T) {
	namespaces := make([]string, 0, maxFanOut+1)
	for i := 0; i <= maxFanOut; i++ {
		namespaces = append(namespaces, "team-"+strconv.Itoa(i))
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/resources/pods?all_namespaces=true", nil)

	s := &server{}
	// A grant that covers half a cluster would turn one list into dozens of
	// tunnel calls, so it is asked to pick a namespace instead.
	if _, ok := s.resourceScope(c, db.UserClusterAccess{
		Namespaces: strings.Join(namespaces, ","),
	}); ok {
		t.Fatal("expected a grant covering more than the fan-out cap to be refused")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestAllNamespacesStillRefusesClusterScopedListsForScopedGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
	token := env.tokenFor(t, user)

	// "All namespaces" is not a way around a scope: nodes have no namespace to
	// be scoped by, so the refusal stands.
	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/nodes?all_namespaces=true", token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestNodeRoles(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   []string
	}{
		{
			name:   "a node with no role labels is a worker",
			labels: map[string]string{"kubernetes.io/hostname": "node-1"},
			want:   []string{"worker"},
		},
		{
			name: "roles come off the node-role labels, sorted",
			labels: map[string]string{
				"node-role.kubernetes.io/worker":        "",
				"node-role.kubernetes.io/control-plane": "",
				"kubernetes.io/os":                      "linux",
			},
			want: []string{"control-plane", "worker"},
		},
		{
			name:   "a bare prefix is not a role",
			labels: map[string]string{"node-role.kubernetes.io/": ""},
			want:   []string{"worker"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nodeRoles(tc.labels)
			if len(got) != len(tc.want) {
				t.Fatalf("expected roles %v, got %v", tc.want, got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("expected roles %v, got %v", tc.want, got)
				}
			}
		})
	}
}

func TestResourceListPath(t *testing.T) {
	path := resourceListPath{"/apis/apps/v1", "deployments"}.namespaced("team a")
	if path != "/apis/apps/v1/namespaces/team%20a/deployments" {
		t.Fatalf("expected the namespace to be escaped, got %q", path)
	}

	if got := (resourceListPath{"/api/v1", "nodes"}).clusterWide(); got != "/api/v1/nodes" {
		t.Fatalf("unexpected cluster-wide path %q", got)
	}
}
