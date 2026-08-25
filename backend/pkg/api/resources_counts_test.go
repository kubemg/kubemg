package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// A count is a number an operator reads off the sidebar and believes. The two
// places it can quietly become the wrong number are the limit=1 arithmetic and
// the summing across a scoped grant's namespaces, so both are pinned here.

func TestCountFromPage(t *testing.T) {
	remaining := func(n int64) *int64 { return &n }

	cases := []struct {
		name        string
		items       int
		cont        string
		remaining   *int64
		wantCount   *int64
		wantApprox  bool
		wantFound   bool
		wantUnknown bool
	}{
		{
			// The whole list fit in the page, so the page is the answer.
			name: "an empty cluster counts zero", items: 0, cont: "",
			wantCount: remaining(0), wantFound: true,
		},
		{
			name: "a single object counts one", items: 1, cont: "",
			wantCount: remaining(1), wantFound: true,
		},
		{
			// This is the case the route exists for: one object came back and
			// the API server reported the other 999.
			name: "one page plus the remainder is the total", items: 1, cont: "token",
			remaining: remaining(999), wantCount: remaining(1000),
			wantApprox: true, wantFound: true,
		},
		{
			// Reporting the page size here would render a busy cluster as "1".
			name: "no remainder reported is no count", items: 1, cont: "token",
			remaining: nil, wantFound: true, wantUnknown: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var page listPage[struct{}]
			page.Items = make([]struct{}, tc.items)
			page.Metadata.Continue = tc.cont
			page.Metadata.RemainingItemCount = tc.remaining

			got := countFromPage(page)
			if got.found != tc.wantFound {
				t.Fatalf("found = %v, want %v", got.found, tc.wantFound)
			}
			if tc.wantUnknown {
				if got.count != nil {
					t.Fatalf("count = %d, want none: the cluster reported no total", *got.count)
				}
				return
			}
			if got.count == nil || *got.count != *tc.wantCount {
				t.Fatalf("count = %v, want %d", got.count, *tc.wantCount)
			}
			if got.approximate != tc.wantApprox {
				t.Fatalf("approximate = %v, want %v", got.approximate, tc.wantApprox)
			}
		})
	}
}

// An all-namespaces read on a scoped grant counts each namespace and adds them
// up. Getting this wrong reports one namespace's total as the cluster's.
func TestCountOverSumsNamespaces(t *testing.T) {
	totals := map[string]int64{"team-a": 12, "team-b": 30}

	view := countOver(func(candidates []string) countResult {
		for name, total := range totals {
			if strings.Contains(candidates[0], "/namespaces/"+name+"/") {
				count := total
				return countResult{count: &count, found: true, ok: true}
			}
		}
		t.Fatalf("unexpected candidate %v", candidates)
		return countResult{}
	}, [][]string{
		{"/api/v1/namespaces/team-a/pods"},
		{"/api/v1/namespaces/team-b/pods"},
	}, "pods")

	if !view.Available || view.Count == nil {
		t.Fatalf("view = %+v, want a count", view)
	}
	if *view.Count != 42 {
		t.Fatalf("count = %d, want the 42 across both namespaces", *view.Count)
	}
}

func TestCountOverReportsWhatTheClusterWouldNotAnswer(t *testing.T) {
	t.Run("not served by this cluster", func(t *testing.T) {
		view := countOver(func([]string) countResult {
			// found=false with no reason is every candidate answering 404.
			return countResult{ok: true}
		}, [][]string{{"/apis/acme.io/v1/widgets"}}, "crd:acme.io/v1/widgets")

		if view.Available {
			t.Fatalf("view = %+v, want unavailable", view)
		}
		if !strings.Contains(view.Reason, "does not serve") {
			t.Fatalf("reason = %q, want it to say the cluster does not serve this", view.Reason)
		}
	})

	t.Run("the cluster's own refusal is carried through", func(t *testing.T) {
		view := countOver(func([]string) countResult {
			return countResult{reason: "secrets is forbidden for user dev", ok: true}
		}, [][]string{{"/api/v1/namespaces/team-a/secrets"}}, "secrets")

		if view.Available {
			t.Fatalf("view = %+v, want unavailable", view)
		}
		// The API server's own words, or an operator cannot tell an RBAC denial
		// from an uninstalled API.
		if !strings.Contains(view.Reason, "forbidden") {
			t.Fatalf("reason = %q, want the cluster's own explanation", view.Reason)
		}
	})

	t.Run("a total the cluster would not report is stated, not guessed", func(t *testing.T) {
		view := countOver(func([]string) countResult {
			return countResult{found: true, ok: true}
		}, [][]string{{"/api/v1/pods"}}, "pods")

		if !view.Available {
			t.Fatal("the list exists; only its total is unknown")
		}
		if view.Count != nil {
			t.Fatalf("count = %d, want none", *view.Count)
		}
		if view.Reason == "" {
			t.Fatal("expected a reason explaining the missing total")
		}
	})
}

// A namespace selection narrows the namespaced lists beside it; it says nothing
// about Nodes. Reading a cluster-scoped kind through a namespaced path would
// 404 and report a cluster's nodes as an uninstalled API.
func TestCountPathsReadClusterScopedKindsClusterWide(t *testing.T) {
	scope := readScope{Namespaces: []string{"team-a"}, Namespace: "team-a"}

	nodes := countPaths(objectKind{versions: []resourceListPath{{"/api/v1", "nodes"}}}, scope)
	if len(nodes) != 1 || nodes[0][0] != "/api/v1/nodes" {
		t.Fatalf("cluster-scoped paths = %v, want the cluster-wide list", nodes)
	}

	pods := countPaths(
		objectKind{versions: []resourceListPath{{"/api/v1", "pods"}}, namespaced: true}, scope)
	if len(pods) != 1 || pods[0][0] != "/api/v1/namespaces/team-a/pods" {
		t.Fatalf("namespaced paths = %v, want the selected namespace", pods)
	}
}

// Every API version a kind is served under travels in one group, so a Gateway
// API cluster on v1beta1 counts rather than reporting nothing.
func TestCountPathsCarryEveryVersion(t *testing.T) {
	groups := countPaths(objectKinds["httproutes"], readScope{All: true})
	if len(groups) != 1 || len(groups[0]) != 2 {
		t.Fatalf("groups = %v, want one group holding both versions", groups)
	}
}

/* ------------------------------------------------------- the HTTP layer --- */

func TestCountsRefuseCurrentlyUnknownKeys(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/counts?all_namespaces=true&keys=nonsense", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	// An unknown key is reported on its own row rather than failing the batch:
	// the sidebar and objectKinds are two views of one inventory, and drift
	// between them is worth seeing.
	body := decode[struct {
		Counts map[string]resourceCountView `json:"counts"`
	}](t, rec)
	entry, ok := body.Counts["nonsense"]
	if !ok {
		t.Fatalf("counts = %+v, want a row for the unknown key", body.Counts)
	}
	if entry.Available || entry.Reason == "" {
		t.Fatalf("entry = %+v, want it marked unavailable with a reason", entry)
	}
}

func TestCountsRefuseAnUnboundedFanOut(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	// A grant this wide multiplies every key by every namespace.
	namespaces := make([]string, 0, maxFanOut)
	for i := range maxFanOut {
		namespaces = append(namespaces, "team-"+itoa(uint(i)))
	}
	env.store.grant(user.ID, cluster.ID, "view", namespaces)
	token := env.tokenFor(t, user)

	keys := []string{"pods", "deployments", "services", "configmaps", "jobs"}
	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/counts?all_namespaces=true&keys="+
			strings.Join(keys, ","), token, nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d for %d keys over %d namespaces, got %d (%s)",
			http.StatusBadRequest, len(keys), maxFanOut, rec.Code, rec.Body.String())
	}
	// The refusal has to say what to do about it, the way resourceScope's does.
	if body := decode[map[string]string](t, rec); !strings.Contains(body["error"], "pick one namespace") {
		t.Fatalf("error = %q, want it to name the way out", body["error"])
	}
}

func TestCountsRequireAKind(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/counts", token, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d with no keys, got %d", http.StatusBadRequest, rec.Code)
	}
}

// Counts inherit the guard chain every other resource read has: a direct-mode
// cluster has no tunnel to count through, and a scoped grant cannot reach a
// cluster-wide kind.
func TestCountsRefuseDirectClusters(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addCluster("legacy", "dev")
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/counts?all_namespaces=true&keys=pods", token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

func TestCountsRefuseClusterScopedKindsForAScopedGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/counts?all_namespaces=true&keys=nodes", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	// Refused as one row rather than as the whole batch — the other counts in
	// the request are still owed to the caller.
	body := decode[struct {
		Counts map[string]resourceCountView `json:"counts"`
	}](t, rec)
	entry := body.Counts["nodes"]
	if entry.Available {
		t.Fatalf("nodes = %+v, want it refused for a scoped grant", entry)
	}
	if !strings.Contains(entry.Reason, "team-a") {
		t.Fatalf("reason = %q, want it to name the granted namespace", entry.Reason)
	}
}

func TestCountsRequireAuth(t *testing.T) {
	env := newTestEnv(t)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/counts?all_namespaces=true&keys=pods", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d without a token, got %d", http.StatusUnauthorized, rec.Code)
	}
}

// countKeys is what stands between the sidebar and a caller naming an
// unbounded number of kinds.
func TestCountKeysParsing(t *testing.T) {
	var grant db.UserClusterAccess
	_ = grant

	t.Run("splits and de-duplicates", func(t *testing.T) {
		c, _ := testContext()
		c.Request.URL.RawQuery = "keys=pods,%20deployments&keys=pods"

		keys, ok := countKeys(c)
		if !ok {
			t.Fatal("expected the keys to parse")
		}
		if strings.Join(keys, ",") != "pods,deployments" {
			t.Fatalf("keys = %v, want pods and deployments once each", keys)
		}
	})

	t.Run("caps the number of kinds", func(t *testing.T) {
		names := make([]string, 0, maxCountKeys+1)
		for i := range maxCountKeys + 1 {
			names = append(names, "kind-"+itoa(uint(i)))
		}
		c, rec := testContext()
		c.Request.URL.RawQuery = "keys=" + strings.Join(names, ",")

		if _, ok := countKeys(c); ok {
			t.Fatal("expected more than the cap to be refused")
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})
}
