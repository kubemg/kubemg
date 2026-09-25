package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The debug route is a read-modify-write onto a pod, the same shape as every
// other workload action, so what is pinned here is what keeps it narrow: the
// path is built the same escaped way objectPath is, and a target container is
// checked against the pod's own before anything is written.

func TestPodObjectPathIsEscaped(t *testing.T) {
	if got, want := podObjectPath("shop", "checkout-7f9"),
		"/api/v1/namespaces/shop/pods/checkout-7f9"; got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	// A name is escaped rather than trusted: it reaches the API path, and the
	// caller supplies it.
	if got, want := podObjectPath("shop", "a/b"),
		"/api/v1/namespaces/shop/pods/a%2Fb"; got != want {
		t.Fatalf("escaped path = %q, want %q", got, want)
	}
}

func TestHasContainerNamedLooksOnlyAtTheWorkloadContainers(t *testing.T) {
	spec := map[string]any{
		"containers": []any{
			map[string]any{"name": "app"},
			map[string]any{"name": "sidecar"},
		},
		"initContainers": []any{
			map[string]any{"name": "migrate"},
		},
		"ephemeralContainers": []any{
			map[string]any{"name": "debug-aaaaaaaa", "targetContainerName": "app"},
		},
	}

	if !hasContainerNamed(spec, "app") {
		t.Fatal("expected app to be found among spec.containers")
	}
	if !hasContainerNamed(spec, "sidecar") {
		t.Fatal("expected sidecar to be found among spec.containers")
	}
	// An init container has already run and exited by the time a pod is
	// running long enough to debug; sharing its process namespace answers a
	// question about a container that is not there anymore.
	if hasContainerNamed(spec, "migrate") {
		t.Fatal("expected an init container not to count as a debug target")
	}
	// An existing ephemeral container is not a target either: debugging a
	// running pod means the workload container running in it right now, not
	// a previous debug session.
	if hasContainerNamed(spec, "debug-aaaaaaaa") {
		t.Fatal("expected an existing ephemeral container not to count as a debug target")
	}
	if hasContainerNamed(spec, "no-such-container") {
		t.Fatal("expected an unknown name to be refused")
	}
}

func TestHasContainerNamedRefusesAMalformedSpec(t *testing.T) {
	cases := map[string]map[string]any{
		"no containers field":   {},
		"containers not a list": {"containers": "not a list"},
		"entry not an object":   {"containers": []any{"not an object"}},
	}
	for name, spec := range cases {
		if hasContainerNamed(spec, "app") {
			t.Fatalf("%s: expected no match against a spec that names nothing", name)
		}
	}
}

// A caller polling for a debug container to start needs to tell "running",
// "still pulling the image" and "will never start" apart without attempting
// an exec first — see DebugContainerSheet on the frontend.
func TestPodViewReportsEphemeralContainerStatus(t *testing.T) {
	raw := []byte(`{
		"metadata": {"name": "checkout-7f9", "namespace": "shop"},
		"spec": {"containers": [{"name": "app", "image": "app:1.0"}]},
		"status": {
			"phase": "Running",
			"containerStatuses": [
				{"name": "app", "image": "app:1.0", "ready": true, "state": {"running": {}}}
			],
			"ephemeralContainerStatuses": [
				{"name": "debug-aaaaaaaa", "state": {"running": {}}},
				{"name": "debug-bbbbbbbb", "state": {
					"waiting": {"reason": "ErrImagePull", "message": "rpc error: manifest unknown"}
				}}
			]
		}
	}`)

	var pod podObject
	if err := json.Unmarshal(raw, &pod); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	containers := pod.view().EphemeralContainers
	if len(containers) != 2 {
		t.Fatalf("ephemeral containers = %d, want 2", len(containers))
	}

	running := containers[0]
	if running.Name != "debug-aaaaaaaa" || !running.Running || running.Reason != "" {
		t.Fatalf("running container = %+v", running)
	}

	waiting := containers[1]
	if waiting.Name != "debug-bbbbbbbb" || waiting.Running ||
		waiting.Reason != "ErrImagePull" || waiting.Message != "rpc error: manifest unknown" {
		t.Fatalf("waiting container = %+v", waiting)
	}
}

// A pod that never had a debug container added reports an empty slice, not a
// missing field — the frontend polls this shape whether or not one exists.
func TestPodViewReportsNoEphemeralContainersAsAnEmptySlice(t *testing.T) {
	raw := []byte(`{"metadata": {"name": "checkout-7f9"}, "spec": {}, "status": {}}`)

	var pod podObject
	if err := json.Unmarshal(raw, &pod); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := pod.view().EphemeralContainers; got == nil || len(got) != 0 {
		t.Fatalf("ephemeral containers = %#v, want an empty, non-nil slice", got)
	}
}

/* -------------------------------------------------------- HTTP surface --- */

// A server started with no debug image configured refuses the write before
// ever reaching the cluster — the same rule the browser shell follows when it
// has no image either.
func TestDebugPodContainerRefusesWithNoImageConfigured(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

	rec := env.do(t, http.MethodPost,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/pods/debug", env.tokenFor(t, admin),
		map[string]string{"pod": "checkout-7f9", "namespace": "shop", "container": "app"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
	if got := decode[struct {
		Error string `json:"error"`
	}](t, rec).Error; got != "no debug image is configured on this server" {
		t.Fatalf("error = %q", got)
	}
}

// The pod and the target container are required before anything is read from
// the cluster, the same shape workloadTarget enforces for a name.
func TestDebugPodContainerRequiresAPodAndATargetContainer(t *testing.T) {
	env := newTestEnvWith(t, func(o *Options) { o.DebugImage = "busybox:1.36" })
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/resources/pods/debug"
	token := env.tokenFor(t, admin)

	if rec := env.do(t, http.MethodPost, path, token,
		map[string]string{"namespace": "shop", "container": "app"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing pod: expected status %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	if rec := env.do(t, http.MethodPost, path, token,
		map[string]string{"pod": "checkout-7f9", "namespace": "shop"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing container: expected status %d, got %d (%s)",
			http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

// A namespace outside a scoped grant is refused before the cluster is asked
// anything, and the refusal names the namespace rather than leaving an
// operator to guess which one they got wrong.
func TestDebugPodContainerRefusesANamespaceOutsideTheGrant(t *testing.T) {
	env := newTestEnvWith(t, func(o *Options) { o.DebugImage = "busybox:1.36" })
	dev := env.store.addUser("dev", "pw", db.RoleUser)
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(dev.ID, cluster.ID, "view", []string{"team-a"})

	rec := env.do(t, http.MethodPost, "/api/v1/clusters/"+itoa(cluster.ID)+"/resources/pods/debug",
		env.tokenFor(t, dev),
		map[string]string{"pod": "checkout-7f9", "namespace": "kube-system", "container": "app"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
	if got := decode[struct {
		Error string `json:"error"`
	}](t, rec).Error; got != "namespace kube-system is outside your granted scope" {
		t.Fatalf("error = %q", got)
	}
}
