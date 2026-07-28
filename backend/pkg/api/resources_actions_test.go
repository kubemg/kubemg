package api

import (
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
		key                   string
		known                 bool
		scalable, restartable bool
	}{
		{key: "deployments", known: true, scalable: true, restartable: true},
		{key: "statefulsets", known: true, scalable: true, restartable: true},
		// A DaemonSet runs one pod per node; there is no replica count to set.
		{key: "daemonsets", known: true, restartable: true},
		// A ReplicaSet scales, but rolling it is the Deployment's job — restarting
		// one directly would be undone by its owner.
		{key: "replicasets", known: true, scalable: true},
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
		if action.scalable != test.scalable || action.restartable != test.restartable {
			t.Fatalf("%q scalable/restartable = %v/%v, want %v/%v",
				test.key, action.scalable, action.restartable, test.scalable, test.restartable)
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
	} {
		if !found[want] {
			t.Fatalf("route %s is not registered", want)
		}
	}
}
