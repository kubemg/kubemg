package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/helm"
)

/*
 * Installing is the first write in this product that puts an object into a
 * cluster that nobody typed. What is pinned here is everything that has to be
 * decided *before* the first write reaches the tunnel — because a refused
 * install must leave nothing behind, and finding out on object nineteen that a
 * chart contains a ClusterRole is the failure mode this planning step exists to
 * prevent.
 */

func TestRouterRegistersInstallAndUpgrade(t *testing.T) {
	// gin panics at registration on a route conflict, and these two sit under
	// the same group as the values and rollback calls.
	router := NewRouter(Options{
		Store: &db.Store{},
		JWT:   auth.NewManager("secret", time.Hour),
		Proxy: bastion.NewProxy(bastion.ProxyOptions{}),
	})

	found := make(map[string]bool)
	for _, route := range router.Routes() {
		found[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		"POST /api/v1/clusters/:id/resources/helm/releases",
		"POST /api/v1/clusters/:id/resources/helm/releases/:name/upgrade",
		"GET /api/v1/helm/repositories",
		"GET /api/v1/helm/repositories/:name/charts",
		"PUT /api/v1/helm/repositories/:name",
		"DELETE /api/v1/helm/repositories/:name",
		"POST /api/v1/helm/repositories/:name/sync",
	} {
		if !found[want] {
			t.Fatalf("route %s is not registered", want)
		}
	}
}

func TestAReleaseNameThatCouldSteerASelectorIsRefused(t *testing.T) {
	// The name reaches a label selector and a Secret name. A comma would add a
	// selector term, a slash a path segment — so it is validated against an
	// anchored pattern rather than escaped, at the door.
	env := newTestEnv(t)
	user := env.store.addUser("dev", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", nil)
	token := env.tokenFor(t, user)

	for _, name := range []string{"a,owner=helm", "../escape", "UPPER", ""} {
		rec := env.do(t, http.MethodPost,
			"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/helm/releases", token,
			map[string]any{"repository": "charts", "chart": "web", "name": name, "namespace": "shop"})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%q: expected %d, got %d (%s)",
				name, http.StatusBadRequest, rec.Code, rec.Body.String())
		}
	}
}

func TestInstallingIntoANamespaceOutsideTheGrantIsRefused(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", []string{"team-a"})

	rec := env.do(t, http.MethodPost,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/helm/releases", env.tokenFor(t, user),
		map[string]any{"repository": "charts", "chart": "web", "name": "web", "namespace": "team-b"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestValuesThatAreNotAMappingAreRefusedBeforeAnythingIsFetched(t *testing.T) {
	// A bare scalar or a list is not something Helm can merge into a chart, and
	// the API server would only refuse it much later.
	env := newTestEnv(t)
	user := env.store.addUser("dev", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", nil)

	rec := env.do(t, http.MethodPost,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/helm/releases", env.tokenFor(t, user),
		map[string]any{
			"repository": "charts", "chart": "web", "name": "web",
			"namespace": "shop", "yaml": "- one\n- two\n",
		})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestAChartFromARepositoryNobodyDeclaredIsAFourOhFour(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("dev", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", nil)

	rec := env.do(t, http.MethodPost,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/helm/releases", env.tokenFor(t, user),
		map[string]any{"repository": "nowhere", "chart": "web", "name": "web", "namespace": "shop"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNotFound, rec.Code, rec.Body.String())
	}
}

func TestAVersionKubemgDoesNotHoldIsRefusedRatherThanFetchedBlind(t *testing.T) {
	// Resolving the version against the *stored catalogue* is what keeps an
	// install from being steered at an arbitrary URL: the archive fetched is one
	// the last sync recorded, at the URL that sync recorded for it.
	env := newTestEnv(t)
	user := env.store.addUser("dev", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", nil)
	env.store.helmRepos["charts"] = &db.HelmRepository{ID: 4, Name: "charts", URL: "https://example.com"}
	env.store.helmCharts[4] = []db.HelmChart{
		{RepositoryID: 4, Name: "web", Versions: `[{"version":"1.0.0","urls":["web.tgz"]}]`},
	}

	rec := env.do(t, http.MethodPost,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/helm/releases", env.tokenFor(t, user),
		map[string]any{
			"repository": "charts", "chart": "web", "version": "9.9.9",
			"name": "web", "namespace": "shop",
		})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d (%s)", http.StatusNotFound, rec.Code, rec.Body.String())
	}
}

/* --------------------------------------------------------------- the plan --- */

// planFor drives the pre-flight check directly. It is the only part of the write
// path that can be asserted without a cluster, and it is the part that decides
// whether anything is written at all.
func planFor(t *testing.T, grant db.UserClusterAccess,
	discovery *clusterDiscovery, rendered *helm.Rendered,
) (*applyPlan, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	plan, _ := (&server{}).planApply(c, discovery, grant, rendered, nil, "app", "team-a")
	return plan, recorder
}

// twoKinds is a discovery answer holding one namespaced kind and one
// cluster-scoped one.
func twoKinds() *clusterDiscovery {
	return &clusterDiscovery{resources: map[string]map[string]apiResource{
		"v1": {
			"ConfigMap": {Plural: "configmaps", Namespaced: true},
			"Namespace": {Plural: "namespaces", Namespaced: false},
		},
		"rbac.authorization.k8s.io/v1": {
			"ClusterRole": {Plural: "clusterroles", Namespaced: false},
		},
	}}
}

func TestAScopedGrantIsToldUpFrontThatAChartReachesOutsideIt(t *testing.T) {
	// A chart is not a manifest the operator wrote — they may have no idea it
	// contains a ClusterRole until it is refused. Being told before the first
	// write is the difference between a message they can act on and a
	// half-installed release.
	rendered := &helm.Rendered{Objects: []helm.Object{
		{APIVersion: "v1", Kind: "ConfigMap", Name: "c", Namespace: "team-a"},
		{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole", Name: "r"},
	}}

	plan, recorder := planFor(t, db.UserClusterAccess{Namespaces: "team-a"}, twoKinds(), rendered)
	if plan != nil {
		t.Fatal("the run was planned rather than refused")
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected %d, got %d (%s)", http.StatusForbidden, recorder.Code, recorder.Body.String())
	}
	if !contains(recorder.Body.String(), "ClusterRole") {
		t.Fatalf("the refusal does not name the object: %s", recorder.Body.String())
	}
}

func TestAScopedGrantIsRefusedAChartThatInstallsIntoAnotherNamespace(t *testing.T) {
	rendered := &helm.Rendered{Objects: []helm.Object{
		{APIVersion: "v1", Kind: "ConfigMap", Name: "c", Namespace: "kube-system"},
	}}

	plan, recorder := planFor(t, db.UserClusterAccess{Namespaces: "team-a"}, twoKinds(), rendered)
	if plan != nil || recorder.Code != http.StatusForbidden {
		t.Fatalf("expected a refusal, got %d (%s)", recorder.Code, recorder.Body.String())
	}
	if !contains(recorder.Body.String(), "kube-system") {
		t.Fatalf("the refusal does not name the namespace: %s", recorder.Body.String())
	}
}

func TestAnUnscopedGrantMayInstallAClusterScopedObject(t *testing.T) {
	// Nothing here is a new permission: the write is impersonated, so the
	// cluster's own RBAC has the last word. What this checks is that the plan
	// does not add a refusal of its own.
	rendered := &helm.Rendered{Objects: []helm.Object{
		{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole", Name: "r"},
	}}

	plan, recorder := planFor(t, db.UserClusterAccess{}, twoKinds(), rendered)
	if plan == nil {
		t.Fatalf("an unscoped grant was refused: %d (%s)", recorder.Code, recorder.Body.String())
	}
}

func TestAKindTheClusterDoesNotServeIsRefusedRatherThanGuessedAt(t *testing.T) {
	// A chart that installs a CRD and an instance of it in one run. Writing to
	// a path this invented would be a 404 the operator cannot read.
	rendered := &helm.Rendered{Objects: []helm.Object{
		{APIVersion: "example.com/v1", Kind: "Widget", Name: "w", Namespace: "team-a",
			Source: "demo/templates/widget.yaml"},
	}}

	plan, recorder := planFor(t, db.UserClusterAccess{}, twoKinds(), rendered)
	if plan != nil || recorder.Code != http.StatusConflict {
		t.Fatalf("expected %d, got %d (%s)", http.StatusConflict, recorder.Code, recorder.Body.String())
	}
	if !contains(recorder.Body.String(), "widget.yaml") {
		t.Fatalf("the refusal does not name the source file: %s", recorder.Body.String())
	}
}

func TestTheRunIsOrderedCRDsThenPreHooksThenTheReleaseThenPostHooks(t *testing.T) {
	rendered := &helm.Rendered{
		CRDs:        []helm.Object{{APIVersion: "v1", Kind: "ConfigMap", Name: "crd", Namespace: "team-a"}},
		PreInstall:  []helm.Object{{APIVersion: "v1", Kind: "ConfigMap", Name: "pre", Namespace: "team-a"}},
		Objects:     []helm.Object{{APIVersion: "v1", Kind: "ConfigMap", Name: "main", Namespace: "team-a"}},
		PostInstall: []helm.Object{{APIVersion: "v1", Kind: "ConfigMap", Name: "post", Namespace: "team-a"}},
	}

	plan, recorder := planFor(t, db.UserClusterAccess{}, twoKinds(), rendered)
	if plan == nil {
		t.Fatalf("planning failed: %s", recorder.Body.String())
	}

	names := make([]string, 0, 4)
	for _, object := range plan.objects {
		names = append(names, object.Name)
	}
	want := []string{"crd", "pre", "main", "post"}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("write order = %v, want %v", names, want)
		}
	}
}

/* ----------------------------------------------------------- what it drops --- */

func TestAnObjectThePreviousRevisionWroteAndThisOneDoesNotIsRemoved(t *testing.T) {
	// The only thing a release's recorded manifest exists for that a re-render
	// cannot supply: a template deleted from a chart leaves an object running
	// that nothing owns any more.
	previous := []helm.Object{
		{APIVersion: "v1", Kind: "ConfigMap", Name: "kept", Namespace: "team-a"},
		{APIVersion: "v1", Kind: "Service", Name: "withdrawn", Namespace: "team-a"},
	}
	wanted := []helm.Object{
		{APIVersion: "v1", Kind: "ConfigMap", Name: "kept", Namespace: "team-a"},
	}

	removals := removalsOf(previous, wanted)
	if len(removals) != 1 || removals[0].Name != "withdrawn" {
		t.Fatalf("removals = %+v", removals)
	}
}

func TestAnObjectThatOnlyMovedNamespaceIsNotTreatedAsTheSameOne(t *testing.T) {
	// A chart may install into more than one namespace, so the namespace is
	// part of an object's identity here rather than context.
	previous := []helm.Object{{APIVersion: "v1", Kind: "ConfigMap", Name: "c", Namespace: "old"}}
	wanted := []helm.Object{{APIVersion: "v1", Kind: "ConfigMap", Name: "c", Namespace: "new"}}

	if got := removalsOf(previous, wanted); len(got) != 1 || got[0].Namespace != "old" {
		t.Fatalf("removals = %+v", got)
	}
}

func TestRemovalsGoInReverseSoDependantsLeaveFirst(t *testing.T) {
	previous := []helm.Object{
		{APIVersion: "v1", Kind: "ConfigMap", Name: "first", Namespace: "team-a"},
		{APIVersion: "v1", Kind: "ConfigMap", Name: "second", Namespace: "team-a"},
	}

	removals := removalsOf(previous, nil)
	if len(removals) != 2 || removals[0].Name != "second" {
		t.Fatalf("removals = %+v, want the write order reversed", removals)
	}
}

/* ------------------------------------------------------------ addressing --- */

func TestAnObjectsPathIsBuiltFromWhatDiscoveryReported(t *testing.T) {
	// The plural is not derivable — `Ingress` is `ingresses` and `Endpoints` is
	// `endpoints`, and no pluralisation rule gets both right — so it comes from
	// the API server and the path is assembled around it.
	cases := []struct {
		apiVersion string
		resource   apiResource
		namespace  string
		name       string
		want       string
	}{
		{"v1", apiResource{Plural: "configmaps", Namespaced: true}, "shop", "c",
			"/api/v1/namespaces/shop/configmaps/c"},
		{"apps/v1", apiResource{Plural: "deployments", Namespaced: true}, "shop", "d",
			"/apis/apps/v1/namespaces/shop/deployments/d"},
		{"rbac.authorization.k8s.io/v1", apiResource{Plural: "clusterroles"}, "", "r",
			"/apis/rbac.authorization.k8s.io/v1/clusterroles/r"},
	}
	for _, test := range cases {
		if got := objectPath(test.apiVersion, test.resource, test.namespace, test.name); got != test.want {
			t.Fatalf("path = %q, want %q", got, test.want)
		}
	}
}

func TestADiscoveryPathSeparatesTheCoreGroupFromTheRest(t *testing.T) {
	// The core group is served at /api/v1 and is absent from /apis. A chart
	// that renders a ConfigMap — which is every chart — needs it.
	if got := discoveryPath("v1"); got != "/api/v1" {
		t.Fatalf("core group path = %q", got)
	}
	if got := discoveryPath("apps/v1"); got != "/apis/apps/v1" {
		t.Fatalf("group path = %q", got)
	}
}

func TestAClusterScopedObjectIsWrittenWithoutANamespace(t *testing.T) {
	// The render gives every object the release's namespace, because it does not
	// know which kinds are cluster-scoped — only discovery does. Sending one on
	// a cluster-scoped object makes the API server refuse the pair.
	stripped := withoutNamespace([]byte(
		`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"n","namespace":"team-a"}}`))
	if contains(string(stripped), "team-a") {
		t.Fatalf("the namespace survived: %s", stripped)
	}
	if !contains(string(stripped), `"name":"n"`) {
		t.Fatalf("the object was damaged: %s", stripped)
	}
}
