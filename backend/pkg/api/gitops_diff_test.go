package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

/*
 * The diff route's order is its security, so each test below pins one step
 * stopping the ones after it from running.
 */

type diffProbe struct {
	liveReads, sourceReads, argoCalls int
	asked                             observability.ArgoObject
}

func trackedLive(t *testing.T) []byte {
	t.Helper()
	object := map[string]any{
		"kind": "Deployment", "apiVersion": "apps/v1",
		"metadata": map[string]any{
			"name": "web", "namespace": "shop",
			"annotations": map[string]any{argoTrackingAnnotation: "shop:apps/Deployment:shop/web"},
		},
	}
	body, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func argoCluster() *fakeCluster {
	cluster := newFakeCluster()
	cluster.object(argoApps[:len(argoApps)-len("/applications")]+"/namespaces/argocd/applications/shop",
		argoApp("argocd", "shop", "OutOfSync", "Healthy",
			argoEntry("apps", "Deployment", "shop", "web", "OutOfSync", "Healthy")))
	return cluster
}

func (p *diffProbe) steps(live func() ([]byte, bool), service trafficReader, source *db.ObservabilitySource,
	state observability.ArgoState, argoErr error,
) gitopsDiffSteps {
	return gitopsDiffSteps{
		readLive: func() ([]byte, bool) {
			p.liveReads++
			return live()
		},
		service: service,
		source: func() (*db.ObservabilitySource, error) {
			p.sourceReads++
			if source == nil {
				return nil, db.ErrNotFound
			}
			return source, nil
		},
		argo: func(_ db.ObservabilitySource, object observability.ArgoObject) (observability.ArgoState, error) {
			p.argoCalls++
			p.asked = object
			return state, argoErr
		},
	}
}

func argoDatasource() *db.ObservabilitySource {
	return &db.ObservabilitySource{Kind: db.SourceGitOps, Provider: db.ProviderArgoCD, Enabled: true}
}

func TestGitopsDiffRefusesASecretBeforeAnyRead(t *testing.T) {
	probe := &diffProbe{}
	steps := probe.steps(func() ([]byte, bool) { return nil, true }, newFakeCluster(), argoDatasource(),
		observability.ArgoState{}, nil)

	status, _, err := steps.run("secrets", adminGrant)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusConflict || probe.liveReads+probe.sourceReads+probe.argoCalls != 0 {
		t.Fatalf("expected 409 with nothing read, got %d (%+v)", status, probe)
	}
}

func TestGitopsDiffClusterRefusalNeverReachesArgo(t *testing.T) {
	probe := &diffProbe{}
	refused := func() ([]byte, bool) { return nil, false } // answered in the cluster's words
	steps := probe.steps(refused, argoCluster(), argoDatasource(), observability.ArgoState{}, nil)

	status, _, err := steps.run("deployments", adminGrant)
	if err != nil {
		t.Fatal(err)
	}
	if status != 0 || probe.liveReads != 1 || probe.argoCalls != 0 || probe.sourceReads != 0 {
		t.Fatalf("expected the live refusal to end the request, got %d (%+v)", status, probe)
	}
}

func TestGitopsDiffApplicationComesFromTheObject(t *testing.T) {
	probe := &diffProbe{}
	live := trackedLive(t)
	state := observability.ArgoState{
		NormalizedLive: `{"kind":"Deployment","spec":{"replicas":3}}`,
		PredictedLive:  `{"kind":"Deployment","spec":{"replicas":1}}`,
		Modified:       true,
	}
	steps := probe.steps(func() ([]byte, bool) { return live, true }, argoCluster(), argoDatasource(), state, nil)

	status, body, err := steps.run("deployments", adminGrant)
	if err != nil {
		t.Fatal(err)
	}
	view, ok := body.(gitopsDiffView)
	if status != http.StatusOK || !ok || !view.Available {
		t.Fatalf("expected a diff, got %d %+v", status, body)
	}
	want := observability.ArgoObject{App: "shop", AppNamespace: "argocd", Group: "apps", Kind: "Deployment",
		Namespace: "shop", Name: "web"}
	if probe.asked != want {
		t.Fatalf("expected Argo CD asked for %+v, got %+v", want, probe.asked)
	}
	if !slices.Equal(changedPaths(view), []string{"changed spec.replicas"}) || !view.Modified {
		t.Fatalf("expected spec.replicas to differ, got %+v", view)
	}
}

func TestGitopsDiffUntrackedObjectIsRefusedWithoutAskingArgo(t *testing.T) {
	probe := &diffProbe{}
	untracked := []byte(`{"kind":"Deployment","apiVersion":"apps/v1","metadata":{"name":"web","namespace":"shop"}}`)
	steps := probe.steps(func() ([]byte, bool) { return untracked, true }, newFakeCluster(), argoDatasource(),
		observability.ArgoState{}, nil)

	status, _, _ := steps.run("deployments", adminGrant)
	if status != http.StatusConflict || probe.argoCalls != 0 {
		t.Fatalf("expected 409 without an Argo call, got %d (%+v)", status, probe)
	}
}

func TestGitopsDiffWithoutASourceIsUnavailableNotFailed(t *testing.T) {
	live := trackedLive(t)
	for name, source := range map[string]*db.ObservabilitySource{
		"none registered": nil,
		"turned off":      {Kind: db.SourceGitOps, Provider: db.ProviderArgoCD},
	} {
		t.Run(name, func(t *testing.T) {
			probe := &diffProbe{}
			steps := probe.steps(func() ([]byte, bool) { return live, true }, argoCluster(), source,
				observability.ArgoState{}, nil)
			status, body, err := steps.run("deployments", adminGrant)
			if err != nil {
				t.Fatal(err)
			}
			view := body.(gitopsDiffView)
			if status != http.StatusOK || view.Available || view.Reason == "" || probe.argoCalls != 0 {
				t.Fatalf("expected available:false with a reason, got %d %+v", status, view)
			}
		})
	}
}

func TestGitopsDiffArgoRefusalIsAReason(t *testing.T) {
	probe := &diffProbe{}
	live := trackedLive(t)
	steps := probe.steps(func() ([]byte, bool) { return live, true }, argoCluster(), argoDatasource(),
		observability.ArgoState{}, &observability.ArgoError{Status: http.StatusForbidden, Message: "not shown"})

	_, body, err := steps.run("deployments", adminGrant)
	if err != nil {
		t.Fatal(err)
	}
	if view := body.(gitopsDiffView); view.Available || view.Reason != "not shown" {
		t.Fatalf("expected Argo CD's refusal as the reason, got %+v", view)
	}

	steps = probe.steps(func() ([]byte, bool) { return live, true }, argoCluster(), argoDatasource(),
		observability.ArgoState{}, errors.New("dial tcp: refused"))
	_, body, _ = steps.run("deployments", adminGrant)
	if view := body.(gitopsDiffView); view.Available || !strings.Contains(view.Reason, "could not be reached") {
		t.Fatalf("expected a transport failure named as one, got %+v", view)
	}
}

func changedPaths(view gitopsDiffView) []string {
	paths := []string{}
	if view.Diff == nil {
		return paths
	}
	for _, change := range view.Diff.Changes {
		paths = append(paths, string(change.Kind)+" "+change.Path)
	}
	return paths
}

// Live is the before side and desired the after: the diff reads as what a sync
// would change.
func TestGitopsDiffChanges(t *testing.T) {
	cases := map[string]struct {
		live, desired string
		want          []string
	}{
		"scalar changed": {
			`{"spec":{"replicas":3,"paused":false}}`,
			`{"spec":{"replicas":1,"paused":false}}`,
			[]string{"changed spec.replicas"},
		},
		"list element added": {
			`{"spec":{"ports":[{"port":80}]}}`,
			`{"spec":{"ports":[{"port":80},{"port":443}]}}`,
			[]string{"added spec.ports[1]"},
		},
		"map key removed": {
			`{"metadata":{"labels":{"app":"web","team":"shop"}}}`,
			`{"metadata":{"labels":{"app":"web"}}}`,
			[]string{"removed metadata.labels.team"},
		},
		"nested scalar in a list": {
			`{"spec":{"containers":[{"name":"web","image":"web:2"}]}}`,
			`{"spec":{"containers":[{"name":"web","image":"web:1"}]}}`,
			[]string{"changed spec.containers[0].image"},
		},
		"nothing differs": {`{"spec":{"replicas":1}}`, `{"spec":{"replicas":1}}`, []string{}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			view := renderGitopsDiff(&gitopsDiffApp{Name: "shop"},
				observability.ArgoState{NormalizedLive: tc.live, PredictedLive: tc.desired})
			if !slices.Equal(changedPaths(view), tc.want) {
				t.Fatalf("expected %q, got %q", tc.want, changedPaths(view))
			}
		})
	}
}

func TestGitopsDiffStripsWhatTheClusterWrites(t *testing.T) {
	side := `{"kind":"Deployment","metadata":{"name":"web","uid":"u-1","resourceVersion":"9","generation":4,` +
		`"creationTimestamp":"2026-01-01T00:00:00Z","managedFields":[{"manager":"argocd"}],` +
		`"annotations":{"kubectl.kubernetes.io/last-applied-configuration":"{}"}},` +
		`"spec":{"replicas":1},"status":{"readyReplicas":1}}`
	view := renderGitopsDiff(&gitopsDiffApp{Name: "shop"}, observability.ArgoState{NormalizedLive: side, PredictedLive: side})
	for _, gone := range []string{"uid", "resourceVersion", "generation", "creationTimestamp", "managedFields",
		"last-applied", "status", "readyReplicas", "annotations"} {
		if strings.Contains(view.Live, gone) || strings.Contains(view.Desired, gone) {
			t.Fatalf("expected %q stripped, got:\n%s", gone, view.Live)
		}
	}
	if !strings.Contains(view.Live, "replicas: 1") || len(view.Diff.Changes) != 0 {
		t.Fatalf("expected the spec kept and nothing differing, got %+v", view)
	}
}

func TestGitopsDiffCapsEachSide(t *testing.T) {
	big := map[string]any{}
	for i := range 4000 {
		big["key-"+strings.Repeat("x", 40)+itoa(uint(i))] = strings.Repeat("v", 60)
	}
	data, _ := json.Marshal(map[string]any{"kind": "ConfigMap", "data": big})
	view := renderGitopsDiff(&gitopsDiffApp{Name: "shop"},
		observability.ArgoState{NormalizedLive: string(data), PredictedLive: `{"kind":"ConfigMap"}`})
	if !view.LiveTruncated || len(view.Live) > maxDiffSide || !strings.HasSuffix(view.Live, "\n") {
		t.Fatalf("expected the live side cut at a line under %d bytes, got %d (truncated=%v)",
			maxDiffSide, len(view.Live), view.LiveTruncated)
	}
	if view.DesiredTruncated {
		t.Fatal("expected the small side left whole")
	}
	if !slices.Equal(changedPaths(view), []string{"removed data"}) {
		t.Fatalf("expected one change for a key present on one side only, got %d", len(view.Diff.Changes))
	}
}

func TestGitopsDiffRouteRefusesASecretOnAnAgentClusterWithoutATunnel(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	// No agent is attached: a read would answer 503. A 409 is the refusal by
	// kind, made before anything was read.
	rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID)+
		"/gitops/diff?kind=secrets&namespace=shop&name=db", token, nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "reveal") {
		t.Fatalf("expected 409 naming the reveal route, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestGitopsDiffOffer(t *testing.T) {
	if offer := gitopsDiffOfferFor(func() (*db.ObservabilitySource, error) { return nil, db.ErrNotFound }); offer.Available || offer.Reason != diffNoSource {
		t.Fatalf("expected no source named, got %+v", offer)
	}
	if offer := gitopsDiffOfferFor(func() (*db.ObservabilitySource, error) { return nil, errors.New("db down") }); offer.Available || offer.Reason == diffNoSource {
		t.Fatalf("expected a store failure not to read as none registered, got %+v", offer)
	}
	if offer := gitopsDiffOfferFor(func() (*db.ObservabilitySource, error) { return argoDatasource(), nil }); !offer.Available {
		t.Fatalf("expected an enabled source offered, got %+v", offer)
	}
}
