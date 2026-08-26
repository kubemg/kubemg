package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The workload actions are writes, so what is pinned here is what keeps them
// narrow: only the kinds that can answer for the action are addressable, the
// path is built from the fixed table rather than from the caller, and the
// restart stamp lands on the pod template and nowhere else.

func TestWorkloadActionsCoverTheRightKinds(t *testing.T) {
	cases := []struct {
		key                                string
		known                              bool
		scalable, restartable, suspendable bool
	}{
		{key: "deployments", known: true, scalable: true, restartable: true},
		{key: "statefulsets", known: true, scalable: true, restartable: true},
		// A DaemonSet runs one pod per node; there is no replica count to set.
		{key: "daemonsets", known: true, restartable: true},
		// A ReplicaSet scales, but rolling it is the Deployment's job — restarting
		// one directly would be undone by its owner.
		{key: "replicasets", known: true, scalable: true},
		// A CronJob has neither: it owns Jobs rather than pods. Its off switch
		// is the schedule, and it is the only kind that has one.
		{key: "cronjobs", known: true, suspendable: true},
		{key: "pods"},
		{key: "jobs"},
		{key: "secrets"},
		{key: "crd:networking.istio.io/v1/gateways"},
		{key: ""},
	}

	for _, test := range cases {
		action, known := workloadActions[test.key]
		if known != test.known {
			t.Fatalf("%q known = %v, want %v", test.key, known, test.known)
		}
		if !known {
			continue
		}
		if action.scalable != test.scalable || action.restartable != test.restartable ||
			action.suspendable != test.suspendable {
			t.Fatalf("%q scalable/restartable/suspendable = %v/%v/%v, want %v/%v/%v",
				test.key, action.scalable, action.restartable, action.suspendable,
				test.scalable, test.restartable, test.suspendable)
		}
	}
}

func TestWorkloadActionPathIsBuiltFromTheTable(t *testing.T) {
	action := workloadActions["deployments"]

	if got, want := action.objectPath("shop", "checkout"),
		"/apis/apps/v1/namespaces/shop/deployments/checkout"; got != want {
		t.Fatalf("object path = %q, want %q", got, want)
	}
	// A name is escaped rather than trusted: it reaches the API path, and the
	// caller supplies it.
	if got, want := action.objectPath("shop", "a/b"),
		"/apis/apps/v1/namespaces/shop/deployments/a%2Fb"; got != want {
		t.Fatalf("escaped path = %q, want %q", got, want)
	}
}

func TestStampRestartWritesThePodTemplateAnnotation(t *testing.T) {
	object := map[string]any{
		"kind": "Deployment",
		"spec": map[string]any{
			"replicas": float64(3),
			"template": map[string]any{
				"metadata": map[string]any{
					"labels": map[string]any{"app": "checkout"},
				},
			},
		},
	}

	if reason := stampRestart(object, "2026-07-28T09:00:00Z"); reason != "" {
		t.Fatalf("expected a well-formed workload to be stamped: %s", reason)
	}

	spec := object["spec"].(map[string]any)
	template := spec["template"].(map[string]any)
	metadata := template["metadata"].(map[string]any)
	annotations, ok := metadata["annotations"].(map[string]any)
	if !ok {
		t.Fatal("expected the annotations map to be created")
	}
	if annotations[restartedAtAnnotation] != "2026-07-28T09:00:00Z" {
		t.Fatalf("annotations = %v", annotations)
	}
	// Nothing else moves: a restart changes the template hash and that is all.
	if spec["replicas"] != float64(3) {
		t.Fatalf("replicas = %v, want the object left alone", spec["replicas"])
	}
	if labels := metadata["labels"].(map[string]any); labels["app"] != "checkout" {
		t.Fatalf("labels = %v, want the template's own metadata kept", labels)
	}
}

// A workload with no pod template is refused rather than written back with one
// invented, because an object whose shape is not what it should be is not one to
// guess about — the write would replace it.
func TestStampRestartRefusesAWorkloadWithNoTemplate(t *testing.T) {
	for name, object := range map[string]map[string]any{
		"no spec":     {"kind": "Deployment"},
		"no template": {"spec": map[string]any{"replicas": float64(1)}},
		"not an object": {"spec": map[string]any{
			"template": "somehow a string",
		}},
	} {
		if reason := stampRestart(object, "2026-07-28T09:00:00Z"); reason == "" {
			t.Fatalf("%s: expected the workload to be refused", name)
		}
	}
}

// The action routes sit under the same group as `/pods/:pod`, and gin panics at
// registration on a route conflict — so building the router is the check. They
// are registered with the rest of the resource API, which exists only where
// there is a proxy to read the cluster through.
func TestRouterRegistersWorkloadActions(t *testing.T) {
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
		"POST /api/v1/clusters/:id/resources/scale",
		"POST /api/v1/clusters/:id/resources/restart",
		"POST /api/v1/clusters/:id/resources/suspend",
		"POST /api/v1/clusters/:id/resources/node/schedulable",
		// Deleting is the same address as reading and writing the object, which
		// is the point: it reaches nothing the manifest editor does not.
		"DELETE /api/v1/clusters/:id/resources/object",
	} {
		if !found[want] {
			t.Fatalf("route %s is not registered", want)
		}
	}
}

/* --------------------------------------------------------------- suspend --- */

// A CronJob's schedule is one field, and suspend changes that field and no
// other. The whole reason this route exists rather than an operator editing the
// manifest is that hand-editing writes the object back in full.
func TestStampSuspendChangesOnlyTheScheduleSwitch(t *testing.T) {
	object := map[string]any{
		"kind": "CronJob",
		"spec": map[string]any{
			"schedule":          "0 3 * * *",
			"concurrencyPolicy": "Forbid",
			"jobTemplate":       map[string]any{"spec": map[string]any{}},
		},
	}

	if reason := stampSuspend(object, true); reason != "" {
		t.Fatalf("expected a well-formed cronjob to be stamped: %s", reason)
	}

	spec := object["spec"].(map[string]any)
	if spec["suspend"] != true {
		t.Fatalf("suspend = %v, want true", spec["suspend"])
	}
	if spec["schedule"] != "0 3 * * *" || spec["concurrencyPolicy"] != "Forbid" {
		t.Fatalf("spec = %v, want everything else left alone", spec)
	}
	if _, ok := spec["jobTemplate"].(map[string]any); !ok {
		t.Fatal("expected the job template to survive a suspend")
	}
}

// An absent `spec.suspend` is the API server's default and means running. A
// cronjob whose spec is missing, or whose switch is not a boolean, is not one to
// guess about — the write that follows replaces the object.
func TestSuspendStateReadsTheDefault(t *testing.T) {
	cases := []struct {
		name      string
		object    map[string]any
		suspended bool
		refused   bool
	}{
		{
			name:   "absent means running",
			object: map[string]any{"spec": map[string]any{"schedule": "@daily"}},
		},
		{
			name:      "suspended",
			object:    map[string]any{"spec": map[string]any{"suspend": true}},
			suspended: true,
		},
		{
			name:   "explicitly running",
			object: map[string]any{"spec": map[string]any{"suspend": false}},
		},
		{
			name:    "no spec",
			object:  map[string]any{"kind": "CronJob"},
			refused: true,
		},
		{
			name:    "not a boolean",
			object:  map[string]any{"spec": map[string]any{"suspend": "yes"}},
			refused: true,
		},
	}

	for _, test := range cases {
		suspended, reason := suspendState(test.object)
		if (reason != "") != test.refused {
			t.Fatalf("%s: reason = %q, refused = %v", test.name, reason, test.refused)
		}
		if !test.refused && suspended != test.suspended {
			t.Fatalf("%s: suspended = %v, want %v", test.name, suspended, test.suspended)
		}
	}
}

// The words matter here because they are what a bulk run reports per row: a
// no-op has to read as a statement about the object rather than as a failure.
func TestSuspendMessagesNameTheStateReached(t *testing.T) {
	if got := suspendedMessage("nightly-report", true); !strings.Contains(got, "suspended") {
		t.Fatalf("suspend message = %q", got)
	}
	if got := suspendedMessage("nightly-report", false); !strings.Contains(got, "resumed") {
		t.Fatalf("resume message = %q", got)
	}
	if got := alreadySuspended(true); !strings.Contains(got, "already") {
		t.Fatalf("no-op message = %q", got)
	}
}

/* ---------------------------------------------------------------- delete --- */

// A delete is addressed exactly like a read of the same object, and the path it
// builds is the kind table's, escaped, with the ownership policy that makes the
// pods follow the workload. Orphan would leave them running under nothing,
// which reads as a delete that did not work.
func TestDeleteAddressesTheSameObjectTheEditorDoes(t *testing.T) {
	kind := objectKinds["pods"]
	paths := kind.objectPaths("shop", "checkout-7f9")
	if len(paths) != 1 || paths[0] != "/api/v1/namespaces/shop/pods/checkout-7f9" {
		t.Fatalf("paths = %v", paths)
	}
	if deletePropagation != "Background" {
		t.Fatalf("propagation = %q, want Background", deletePropagation)
	}

	// A kind served by two API versions has both candidates, so a delete walks
	// them the way readObject does rather than guessing at one.
	if got := len(objectKinds["httproutes"].objectPaths("shop", "api")); got != 2 {
		t.Fatalf("httproute candidates = %d, want 2", got)
	}
}

// The message is in the tense the cluster means it: a delete is a request for
// removal, and a pod with a grace period or a finalizer is still there when
// this returns.
func TestDeletedMessageDoesNotClaimTheObjectIsGone(t *testing.T) {
	if got := deletedMessage("checkout-7f9"); !strings.Contains(got, "marked for deletion") {
		t.Fatalf("message = %q", got)
	}
}

/* ---------------------------------------------------------- node cordon --- */

// A node's off switch is one field, and cordoning changes that field and no
// other — the same rule the CronJob's suspend follows.
func TestStampSchedulableChangesOnlyTheField(t *testing.T) {
	object := map[string]any{
		"kind": "Node",
		"spec": map[string]any{
			"podCIDR":       "10.244.0.0/24",
			"unschedulable": false,
		},
	}

	stampSchedulable(object, true)

	spec := object["spec"].(map[string]any)
	if spec["unschedulable"] != true {
		t.Fatalf("unschedulable = %v, want true", spec["unschedulable"])
	}
	if spec["podCIDR"] != "10.244.0.0/24" {
		t.Fatalf("spec = %v, want everything else left alone", spec)
	}
}

// stampSchedulable creates the spec map if the read somehow came back without
// one, the same defensive shape stampSuspend does not need because a CronJob
// always has a spec — a Node's is thinner, and this is cheap insurance.
func TestStampSchedulableCreatesAMissingSpec(t *testing.T) {
	object := map[string]any{"kind": "Node"}
	stampSchedulable(object, true)
	spec, ok := object["spec"].(map[string]any)
	if !ok {
		t.Fatal("expected a spec map to be created")
	}
	if spec["unschedulable"] != true {
		t.Fatalf("unschedulable = %v, want true", spec["unschedulable"])
	}
}

// An absent `spec.unschedulable` is the API server's default and means
// schedulable. A node whose spec is missing, or whose switch is not a
// boolean, is not one to guess about — the write that follows replaces the
// object.
func TestNodeSchedulableStateReadsTheDefault(t *testing.T) {
	cases := []struct {
		name          string
		object        map[string]any
		unschedulable bool
		refused       bool
	}{
		{
			name:   "absent means schedulable",
			object: map[string]any{"spec": map[string]any{"podCIDR": "10.244.0.0/24"}},
		},
		{
			name:          "cordoned",
			object:        map[string]any{"spec": map[string]any{"unschedulable": true}},
			unschedulable: true,
		},
		{
			name:   "explicitly schedulable",
			object: map[string]any{"spec": map[string]any{"unschedulable": false}},
		},
		{
			name:    "no spec",
			object:  map[string]any{"kind": "Node"},
			refused: true,
		},
		{
			name:    "not a boolean",
			object:  map[string]any{"spec": map[string]any{"unschedulable": "yes"}},
			refused: true,
		},
	}

	for _, test := range cases {
		unschedulable, reason := nodeSchedulableState(test.object)
		if (reason != "") != test.refused {
			t.Fatalf("%s: reason = %q, refused = %v", test.name, reason, test.refused)
		}
		if !test.refused && unschedulable != test.unschedulable {
			t.Fatalf("%s: unschedulable = %v, want %v", test.name, unschedulable, test.unschedulable)
		}
	}
}

// The words matter here because a no-op has to read as a statement about the
// node rather than as a failure, and the cordon message must never claim to
// have moved anything already running.
func TestNodeSchedulableMessagesNameTheStateReached(t *testing.T) {
	if got := schedulableMessage("node-1", true); !strings.Contains(got, "cordoned") ||
		!strings.Contains(got, "not moved") {
		t.Fatalf("cordon message = %q", got)
	}
	if got := schedulableMessage("node-1", false); !strings.Contains(got, "uncordoned") {
		t.Fatalf("uncordon message = %q", got)
	}
	if got := alreadySchedulable(true); !strings.Contains(got, "already") {
		t.Fatalf("no-op message = %q", got)
	}
}

// The write is conditional on the resourceVersion the object was read at, and
// strips managedFields the way the manifest editor does — the two things
// setNodeSchedulable does to the object between the read and the write,
// beyond the one field it means to change.
func TestNodeSchedulableWritePreservesResourceVersionAndStripsManagedFields(t *testing.T) {
	object := map[string]any{
		"kind": "Node",
		"metadata": map[string]any{
			"name":            "node-1",
			"resourceVersion": "12345",
			"managedFields":   []any{map[string]any{"manager": "kubelet"}},
		},
		"spec": map[string]any{"unschedulable": false},
	}

	stampSchedulable(object, true)
	stripManagedFields(object)

	metadata := object["metadata"].(map[string]any)
	if metadata["resourceVersion"] != "12345" {
		t.Fatalf("resourceVersion = %v, want it left untouched", metadata["resourceVersion"])
	}
	if _, ok := metadata["managedFields"]; ok {
		t.Fatal("expected managedFields to be stripped before the write")
	}
	if object["spec"].(map[string]any)["unschedulable"] != true {
		t.Fatal("expected the requested field to be written")
	}
}

func TestNodeObjectPathIsBuiltFromTheFixedTable(t *testing.T) {
	if got, want := nodeObjectPath("node-1"), "/api/v1/nodes/node-1"; got != want {
		t.Fatalf("node path = %q, want %q", got, want)
	}
	// A name is escaped rather than trusted, the same as every other object
	// path here.
	if got, want := nodeObjectPath("a/b"), "/api/v1/nodes/a%2Fb"; got != want {
		t.Fatalf("escaped node path = %q, want %q", got, want)
	}
}

// The route shares the other resource writes' guards: authenticated, agent-
// only, and — because a Node is cluster-scoped — refused to a namespace-scoped
// grant before the tunnel is touched.
func TestNodeSchedulableRouteSharesTheResourceGuards(t *testing.T) {
	t.Run("a scoped grant is refused", func(t *testing.T) {
		env := newTestEnv(t)
		user := env.store.addUser("scoped", "secret123", "user")
		cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
		env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
		token := env.tokenFor(t, user)

		rec := env.do(t, http.MethodPost,
			"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/node/schedulable", token,
			map[string]any{"name": "node-1", "unschedulable": true})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected %d for a scoped grant, got %d (%s)",
				http.StatusForbidden, rec.Code, rec.Body.String())
		}
	})

	t.Run("a missing node name is refused before the tunnel is touched", func(t *testing.T) {
		env := newTestEnv(t)
		admin := env.store.addUser("admin", "secret123", "admin")
		cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
		token := env.tokenFor(t, admin)

		rec := env.do(t, http.MethodPost,
			"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/node/schedulable", token,
			map[string]any{"name": "  ", "unschedulable": true})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected %d for a blank node name, got %d (%s)",
				http.StatusBadRequest, rec.Code, rec.Body.String())
		}
	})

	t.Run("direct clusters have no live state", func(t *testing.T) {
		env := newTestEnv(t)
		admin := env.store.addUser("admin", "secret123", "admin")
		cluster := env.store.addCluster("legacy", "dev")
		token := env.tokenFor(t, admin)

		rec := env.do(t, http.MethodPost,
			"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/node/schedulable", token,
			map[string]any{"name": "node-1", "unschedulable": true})
		if rec.Code != http.StatusConflict {
			t.Fatalf("expected %d for a direct-mode cluster, got %d (%s)",
				http.StatusConflict, rec.Code, rec.Body.String())
		}
	})

	t.Run("authentication is required", func(t *testing.T) {
		env := newTestEnv(t)
		cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

		rec := env.do(t, http.MethodPost,
			"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/node/schedulable", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected %d without a token, got %d", http.StatusUnauthorized, rec.Code)
		}
	})
}
