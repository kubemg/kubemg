package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// Rollout history and rollback are read from where the cluster already keeps
// them — ReplicaSets for a Deployment, ControllerRevisions for a StatefulSet
// or DaemonSet — so what is pinned here is the same thing every workload
// action pins: the table only knows the three kinds it should, ownership is
// decided by ownerReferences and never by the label selector alone, and a
// rollback resolves against what a fresh read just returned rather than
// against anything a caller could have made up.

func TestWorkloadRevisionKindsCoverTheThreeKinds(t *testing.T) {
	cases := []struct {
		key   string
		known bool
		style revisionStyle
	}{
		{key: "deployments", known: true, style: replicaSetRevisions},
		{key: "statefulsets", known: true, style: controllerRevisionRevisions},
		{key: "daemonsets", known: true, style: controllerRevisionRevisions},
		// A ReplicaSet is rolled by the Deployment that owns it, not on its
		// own — and a CronJob owns Jobs rather than pods, so neither has a
		// rollout history of its own.
		{key: "replicasets"},
		{key: "cronjobs"},
		{key: "jobs"},
		{key: "pods"},
		{key: ""},
	}

	for _, test := range cases {
		revKind, known := workloadRevisionKinds[test.key]
		if known != test.known {
			t.Fatalf("%q known = %v, want %v", test.key, known, test.known)
		}
		if !known {
			continue
		}
		if revKind.style != test.style {
			t.Fatalf("%q style = %v, want %v", test.key, revKind.style, test.style)
		}
		if revKind.kind == "" {
			t.Fatalf("%q has no reported kind", test.key)
		}
	}
}

/* --------------------------------------------------------------- ownership --- */

// Ownership is ownerReferences, never the label selector alone: the selector
// only narrows the list read, so a foreign ReplicaSet sharing the
// Deployment's own labels must not survive the filter that follows.
func TestOwnedRevisionObjectsIgnoresAForeignReplicaSetWithMatchingLabels(t *testing.T) {
	ownedBy := func(name, ownerKind, ownerUID string) map[string]any {
		return map[string]any{
			"metadata": map[string]any{
				"name": name,
				"ownerReferences": []any{
					map[string]any{"kind": ownerKind, "uid": ownerUID},
				},
			},
		}
	}

	items := []map[string]any{
		ownedBy("checkout-1", "Deployment", "deploy-uid"),
		ownedBy("checkout-2", "Deployment", "deploy-uid"),
		// Same labels a selector match would have picked up, but owned by an
		// entirely different Deployment.
		ownedBy("checkout-copy", "Deployment", "some-other-uid"),
		// Owned by a ReplicaSet rather than a Deployment — a hand-created
		// object, or one this file has no business reading as a revision.
		ownedBy("checkout-manual", "ReplicaSet", "deploy-uid"),
		// No owner at all.
		{"metadata": map[string]any{"name": "checkout-orphan"}},
	}

	got := ownedRevisionObjects("deploy-uid", "Deployment", items)
	if len(got) != 2 {
		t.Fatalf("owned = %d, want 2 (%v)", len(got), got)
	}
	for i, want := range []string{"checkout-1", "checkout-2"} {
		if got[i]["metadata"].(map[string]any)["name"] != want {
			t.Fatalf("owned[%d] = %v, want %s", i, got[i], want)
		}
	}
}

func TestOwnedRevisionObjectsRefusesAnEmptyParentUID(t *testing.T) {
	items := []map[string]any{
		{"metadata": map[string]any{
			"name":            "checkout-1",
			"ownerReferences": []any{map[string]any{"kind": "Deployment", "uid": ""}},
		}},
	}
	// A workload with no UID at all (should never happen against a real
	// cluster) must never match everything by matching on emptiness.
	if got := ownedRevisionObjects("", "Deployment", items); len(got) != 0 {
		t.Fatalf("expected no matches for an empty parent UID, got %v", got)
	}
}

/* ------------------------------------------------------------------ selector --- */

func TestWorkloadSelectorFromRefusesAWorkloadWithNoSelector(t *testing.T) {
	cases := map[string]map[string]any{
		"no spec":         {"kind": "Deployment"},
		"no selector":     {"spec": map[string]any{}},
		"empty selector":  {"spec": map[string]any{"selector": map[string]any{}}},
		"selector wrong type": {"spec": map[string]any{"selector": "not a selector"}},
	}
	for name, live := range cases {
		if _, err := workloadSelectorFrom(live); err == nil {
			t.Fatalf("%s: expected a refusal", name)
		}
	}
}

func TestWorkloadSelectorFromRendersMatchLabels(t *testing.T) {
	live := map[string]any{
		"spec": map[string]any{
			"selector": map[string]any{
				"matchLabels": map[string]any{"app": "checkout"},
			},
		},
	}
	got, err := workloadSelectorFrom(live)
	if err != nil {
		t.Fatalf("expected a well-formed selector to encode: %v", err)
	}
	if want := "app=checkout"; got != want {
		t.Fatalf("selector = %q, want %q", got, want)
	}
}

/* ------------------------------------------------------------------ history --- */

func replicaSet(name, revision string, current bool, images []string, replicas, ready int) map[string]any {
	containers := make([]any, 0, len(images))
	for _, image := range images {
		containers = append(containers, map[string]any{"image": image})
	}
	return map[string]any{
		"metadata": map[string]any{
			"name":              name,
			"creationTimestamp": "2026-07-01T00:00:00Z",
			"annotations":       map[string]any{deploymentRevisionAnnotation: revision},
		},
		"spec": map[string]any{
			"replicas": float64(replicas),
			"template": map[string]any{
				"spec": map[string]any{"containers": containers},
			},
		},
		"status": map[string]any{"readyReplicas": float64(ready)},
	}
}

// Newest first, and the revision named by the Deployment's own annotation —
// the same one its current ReplicaSet carries — is the one marked current.
func TestBuildRolloutRevisionsOrdersNewestFirstAndMarksCurrentForDeployments(t *testing.T) {
	revKind := workloadRevisionKinds["deployments"]
	live := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{deploymentRevisionAnnotation: "2"},
		},
	}
	owned := []map[string]any{
		replicaSet("checkout-1", "1", false, []string{"nginx:1.26"}, 0, 0),
		replicaSet("checkout-2", "2", true, []string{"nginx:1.27"}, 2, 2),
	}

	got := buildRolloutRevisions(revKind, live, owned)
	if len(got) != 2 {
		t.Fatalf("revisions = %d, want 2", len(got))
	}
	if got[0].Revision != 2 || !got[0].Current {
		t.Fatalf("revisions[0] = %+v, want revision 2 marked current", got[0])
	}
	if got[1].Revision != 1 || got[1].Current {
		t.Fatalf("revisions[1] = %+v, want revision 1 not current", got[1])
	}
	if got[0].Replicas == nil || *got[0].Replicas != 2 {
		t.Fatalf("replicas = %v, want 2", got[0].Replicas)
	}
	if got[0].Ready == nil || *got[0].Ready != 2 {
		t.Fatalf("ready = %v, want 2", got[0].Ready)
	}
	if len(got[0].Images) != 1 || got[0].Images[0] != "nginx:1.27" {
		t.Fatalf("images = %v", got[0].Images)
	}
}

// A ControllerRevision carries no pod count of its own, so replicas and ready
// must never be invented for one.
func TestBuildRolloutRevisionsOmitsPodCountsForControllerRevisions(t *testing.T) {
	revKind := workloadRevisionKinds["statefulsets"]
	live := map[string]any{
		"status": map[string]any{"currentRevision": "web-2"},
	}
	owned := []map[string]any{
		{
			"metadata":   map[string]any{"name": "web-1", "creationTimestamp": "2026-07-01T00:00:00Z"},
			"revision":   float64(1),
			"data":       map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"image": "postgres:15"}}}}}},
		},
		{
			"metadata": map[string]any{"name": "web-2", "creationTimestamp": "2026-07-02T00:00:00Z"},
			"revision": float64(2),
			"data":     map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"image": "postgres:16"}}}}}},
		},
	}

	got := buildRolloutRevisions(revKind, live, owned)
	if len(got) != 2 {
		t.Fatalf("revisions = %d, want 2", len(got))
	}
	if got[0].Revision != 2 || got[0].Name != "web-2" || !got[0].Current {
		t.Fatalf("revisions[0] = %+v, want revision 2 (web-2) current", got[0])
	}
	if got[0].Replicas != nil || got[0].Ready != nil {
		t.Fatalf("replicas/ready = %v/%v, want both omitted", got[0].Replicas, got[0].Ready)
	}
	if len(got[0].Images) != 1 || got[0].Images[0] != "postgres:16" {
		t.Fatalf("images = %v", got[0].Images)
	}
}

// A DaemonSet's status carries no field naming its current revision at all,
// so the newest revision — the one either controller would have just
// created, since both only ever move forward — is what stands in.
func TestBuildRolloutRevisionsFallsBackToNewestWhenNothingNamesCurrent(t *testing.T) {
	revKind := workloadRevisionKinds["daemonsets"]
	live := map[string]any{}
	owned := []map[string]any{
		{"metadata": map[string]any{"name": "agent-1", "creationTimestamp": "2026-07-01T00:00:00Z"}, "revision": float64(1)},
		{"metadata": map[string]any{"name": "agent-3", "creationTimestamp": "2026-07-03T00:00:00Z"}, "revision": float64(3)},
		{"metadata": map[string]any{"name": "agent-2", "creationTimestamp": "2026-07-02T00:00:00Z"}, "revision": float64(2)},
	}

	got := buildRolloutRevisions(revKind, live, owned)
	if len(got) != 3 {
		t.Fatalf("revisions = %d, want 3", len(got))
	}
	if got[0].Revision != 3 || !got[0].Current {
		t.Fatalf("revisions[0] = %+v, want revision 3 marked current", got[0])
	}
	if got[1].Revision != 2 || got[1].Current {
		t.Fatalf("revisions[1] = %+v", got[1])
	}
	if got[2].Revision != 1 || got[2].Current {
		t.Fatalf("revisions[2] = %+v", got[2])
	}
}

// An item the label selector happened to match but that carries no revision
// number at all is not one to guess a number for; it is simply left out.
func TestBuildRolloutRevisionsDropsItemsWithNoRevisionNumber(t *testing.T) {
	revKind := workloadRevisionKinds["deployments"]
	owned := []map[string]any{
		{"metadata": map[string]any{"name": "no-annotation", "creationTimestamp": "2026-07-01T00:00:00Z"}},
		replicaSet("checkout-1", "1", false, nil, 1, 1),
	}
	got := buildRolloutRevisions(revKind, map[string]any{}, owned)
	if len(got) != 1 || got[0].Name != "checkout-1" {
		t.Fatalf("revisions = %+v, want only checkout-1", got)
	}
}

/* --------------------------------------------------------------- rollback --- */

// A revision that no longer exists in the history is a 404; the one already
// running is a 409 that says why rather than writing an object that changes
// nothing.
func TestResolveRollbackTargetRefusesMissingAndCurrentRevisions(t *testing.T) {
	style := replicaSetRevisions
	revisions := []rolloutRevision{
		{Revision: 2, Current: true},
		{Revision: 1},
	}
	owned := []map[string]any{
		replicaSet("checkout-2", "2", true, []string{"nginx:1.27"}, 1, 1),
		replicaSet("checkout-1", "1", false, []string{"nginx:1.26"}, 1, 1),
	}

	if _, status, msg := resolveRollbackTarget(revisions, owned, style, 9); status != http.StatusNotFound || msg == "" {
		t.Fatalf("missing revision: status = %d, msg = %q, want 404 with a reason", status, msg)
	}
	if _, status, msg := resolveRollbackTarget(revisions, owned, style, 2); status != http.StatusConflict || msg == "" {
		t.Fatalf("current revision: status = %d, msg = %q, want 409 with a reason", status, msg)
	}

	target, status, msg := resolveRollbackTarget(revisions, owned, style, 1)
	if status != 0 || msg != "" {
		t.Fatalf("expected revision 1 to resolve cleanly, got status %d msg %q", status, msg)
	}
	if target == nil || target["metadata"].(map[string]any)["name"] != "checkout-1" {
		t.Fatalf("target = %v, want checkout-1", target)
	}
}

// The write is the target revision's pod template onto the live object, its
// pod-template-hash stripped and nothing else about the live object touched
// besides the eventual managedFields strip every write path shares.
func TestApplyRolloutTargetRestoresTheTemplateAndStripsThePodTemplateHash(t *testing.T) {
	live := map[string]any{
		"kind": "Deployment",
		"metadata": map[string]any{
			"name":            "checkout",
			"resourceVersion": "555",
			"managedFields":   []any{map[string]any{"manager": "kubectl"}},
		},
		"spec": map[string]any{
			"replicas": float64(2),
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]any{"app": "checkout", "pod-template-hash": "new999"},
				},
			},
		},
	}
	target := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]any{"app": "checkout", "pod-template-hash": "old111"},
				},
				"spec": map[string]any{
					"containers": []any{map[string]any{"image": "checkout:1.9"}},
				},
			},
		},
	}

	if reason := applyRolloutTarget(replicaSetRevisions, live, target); reason != "" {
		t.Fatalf("unexpected refusal: %s", reason)
	}
	// Nothing else about the write path's own discipline is skipped — the
	// managedFields strip every workload write applies still has to run.
	stripManagedFields(live)

	metadata := live["metadata"].(map[string]any)
	if metadata["resourceVersion"] != "555" {
		t.Fatalf("resourceVersion = %v, want preserved for the conditional write", metadata["resourceVersion"])
	}
	if _, ok := metadata["managedFields"]; ok {
		t.Fatal("expected managedFields to be stripped")
	}

	template := live["spec"].(map[string]any)["template"].(map[string]any)
	labels := template["metadata"].(map[string]any)["labels"].(map[string]any)
	if _, ok := labels["pod-template-hash"]; ok {
		t.Fatal("expected pod-template-hash to be stripped from the restored template")
	}
	if labels["app"] != "checkout" {
		t.Fatalf("labels = %v, want the rest of the restored template kept", labels)
	}
	containers := template["spec"].(map[string]any)["containers"].([]any)
	if len(containers) != 1 || containers[0].(map[string]any)["image"] != "checkout:1.9" {
		t.Fatalf("containers = %v, want the target revision's own image", containers)
	}
	// Nothing outside the template moved.
	if live["spec"].(map[string]any)["replicas"] != float64(2) {
		t.Fatalf("replicas = %v, want the object left alone", live["spec"].(map[string]any)["replicas"])
	}
}

// A StatefulSet/DaemonSet's ControllerRevision carries its template inside
// `data.spec.template`, and that is what a rollback restores.
func TestApplyRolloutTargetReadsTheControllerRevisionTemplate(t *testing.T) {
	live := map[string]any{
		"spec": map[string]any{
			"serviceName": "web",
			"template":    map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "web"}}},
		},
	}
	target := map[string]any{
		"revision": float64(2),
		"data": map[string]any{
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{"containers": []any{map[string]any{"image": "postgres:15"}}},
				},
			},
		},
	}

	if reason := applyRolloutTarget(controllerRevisionRevisions, live, target); reason != "" {
		t.Fatalf("unexpected refusal: %s", reason)
	}
	template := live["spec"].(map[string]any)["template"].(map[string]any)
	containers := template["spec"].(map[string]any)["containers"].([]any)
	if len(containers) != 1 || containers[0].(map[string]any)["image"] != "postgres:15" {
		t.Fatalf("containers = %v, want the ControllerRevision's own image", containers)
	}
	// The rest of the object — a field a pod template write must never touch —
	// is exactly as it was.
	if live["spec"].(map[string]any)["serviceName"] != "web" {
		t.Fatalf("serviceName = %v, want left alone", live["spec"].(map[string]any)["serviceName"])
	}
}

func TestApplyRolloutTargetRefusesARevisionWithNoTemplate(t *testing.T) {
	live := map[string]any{"spec": map[string]any{}}
	if reason := applyRolloutTarget(replicaSetRevisions, live, map[string]any{}); reason == "" {
		t.Fatal("expected a refusal for a revision with no pod template")
	}
}

func TestApplyRolloutTargetRefusesALiveObjectWithNoSpec(t *testing.T) {
	target := replicaSet("checkout-1", "1", false, []string{"nginx:1.26"}, 1, 1)
	if reason := applyRolloutTarget(replicaSetRevisions, map[string]any{}, target); reason == "" {
		t.Fatal("expected a refusal for a live object with no spec")
	}
}

/* ------------------------------------------------------------ HTTP-level --- */

// The routes sit under the same resources group as the other workload
// actions, and gin panics at registration on a route conflict — so building
// the router is the check that history and rollback have not collided with
// anything already there.
func TestRouterRegistersWorkloadRolloutRoutes(t *testing.T) {
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
		"GET /api/v1/clusters/:id/resources/workload/history",
		"POST /api/v1/clusters/:id/resources/workload/rollback",
	} {
		if !found[want] {
			t.Fatalf("route %s is not registered", want)
		}
	}
}

// An unsupported kind is refused before anything reaches the tunnel — the
// same 409 shape scale/restart/suspend give a workload that cannot answer for
// the action asked of it.
func TestShowWorkloadHistoryRefusesAnUnsupportedKind(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addSuperAdmin("root", "secret123")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/workload/history?kind=cronjobs&name=nightly&namespace=default",
		token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

func TestRollbackWorkloadRefusesAnUnsupportedKind(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addSuperAdmin("root", "secret123")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodPost,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/workload/rollback",
		token, map[string]any{"kind": "cronjobs", "name": "nightly", "namespace": "default", "revision": 1})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

// Namespace enforcement is the same here as for every other namespaced
// workload route: a namespace outside the grant is refused before anything
// reaches the tunnel.
func TestShowWorkloadHistoryRefusesNamespaceOutsideGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", []string{"team-a"})
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+
			"/resources/workload/history?kind=deployments&name=checkout&namespace=team-b",
		token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestRollbackWorkloadRefusesNamespaceOutsideGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "edit", []string{"team-a"})
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodPost,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/workload/rollback",
		token, map[string]any{
			"kind": "deployments", "name": "checkout", "namespace": "team-b", "revision": 1,
		})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

// A missing revision number in the body is a 400 before anything else is
// even looked at, the same discipline the scale route holds a missing
// replica count to.
func TestRollbackWorkloadRefusesAMissingRevision(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addSuperAdmin("root", "secret123")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodPost,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/workload/rollback",
		token, map[string]any{"kind": "deployments", "name": "checkout", "namespace": "default"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}
