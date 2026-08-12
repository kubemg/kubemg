package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// Describe is generic on purpose — there is no per-kind describer to test — so
// what is pinned here is the extraction that has to hold for *every* object:
// the flatten stays bounded, conditions come out structured, and both shapes of
// Kubernetes Event resolve to a time and a count.

func TestDescribeObjectExtractsMetadataAndConditions(t *testing.T) {
	object := map[string]any{
		"kind":       "Deployment",
		"apiVersion": "apps/v1",
		"metadata": map[string]any{
			"name":              "checkout",
			"namespace":         "shop",
			"creationTimestamp": "2026-07-01T10:00:00Z",
			"labels":            map[string]any{"app": "checkout"},
			"annotations": map[string]any{
				"team":                "payments",
				lastAppliedAnnotation: `{"a really long duplicate of the object"}`,
			},
		},
		"spec": map[string]any{"replicas": float64(3), "paused": false},
		"status": map[string]any{
			"readyReplicas": float64(2),
			"conditions": []any{
				map[string]any{
					"type":               "Available",
					"status":             "False",
					"reason":             "MinimumReplicasUnavailable",
					"message":            "Deployment does not have minimum availability.",
					"lastTransitionTime": "2026-07-02T09:00:00Z",
				},
			},
		},
	}

	view := describeObject(object, "checkout", "shop")

	if view.Kind != "Deployment" || view.APIVersion != "apps/v1" {
		t.Fatalf("view = %+v, want the kind and apiVersion carried", view)
	}
	if view.Created.IsZero() {
		t.Fatal("expected creationTimestamp to be parsed")
	}
	if view.Labels["app"] != "checkout" {
		t.Fatalf("labels = %v", view.Labels)
	}
	// kubectl's last-applied copy is a duplicate of the object and routinely
	// longer than it; the drawer must not open on two copies of the same thing.
	if _, present := view.Annotations[lastAppliedAnnotation]; present {
		t.Fatal("expected the last-applied annotation to be dropped")
	}
	if view.Annotations["team"] != "payments" {
		t.Fatalf("annotations = %v", view.Annotations)
	}

	if len(view.Conditions) != 1 {
		t.Fatalf("conditions = %+v, want one", view.Conditions)
	}
	condition := view.Conditions[0]
	if condition.Type != "Available" || condition.Status != "False" ||
		condition.Reason != "MinimumReplicasUnavailable" {
		t.Fatalf("condition = %+v", condition)
	}
	if condition.LastTransitionAt == nil {
		t.Fatal("expected lastTransitionTime to be parsed")
	}

	// A replica count is an integer to everyone but JSON; `3.0` in the drawer
	// would look like a bug in KubeMG rather than a number from the cluster.
	if !hasField(view.Spec, "replicas", "3") {
		t.Fatalf("spec = %+v, want replicas: 3", view.Spec)
	}
	if !hasField(view.Spec, "paused", "false") {
		t.Fatalf("spec = %+v, want paused: false", view.Spec)
	}

	// Conditions are rendered in full above, so repeating them in the flatten
	// would spend the status budget saying the same thing twice.
	if slices.ContainsFunc(view.Status, func(field fieldView) bool {
		return strings.HasPrefix(field.Path, "conditions")
	}) {
		t.Fatalf("status = %+v, want conditions left out of the flatten", view.Status)
	}
	if !hasField(view.Status, "readyReplicas", "2") {
		t.Fatalf("status = %+v, want readyReplicas: 2", view.Status)
	}
}

// The flatten is a page of lines, not a second copy of the object — so it is
// bounded in depth, in count and in value length, and says when it stopped.
func TestSummarizeStaysBounded(t *testing.T) {
	// Deeper than the walk goes: the innermost map is described by its size
	// rather than expanded, which is what stops a Deployment's embedded pod
	// template from becoming the whole summary.
	deep := map[string]any{
		"a": map[string]any{
			"b": map[string]any{
				"c": map[string]any{"d": "too deep", "e": "also too deep"},
			},
		},
	}
	fields, truncated := summarize(deep)
	if truncated {
		t.Fatal("a small object should not report truncation")
	}
	if !hasField(fields, "a.b.c", "2 fields") {
		t.Fatalf("fields = %+v, want the nested map described by size", fields)
	}

	// Past the field budget it stops and says so, rather than quietly returning
	// part of the object as if it were all of it.
	wide := map[string]any{}
	for i := range maxSummaryFields + 20 {
		wide[string(rune('a'+i%26))+string(rune('a'+i/26))] = float64(i)
	}
	fields, truncated = summarize(wide)
	if !truncated {
		t.Fatal("expected a summary past the budget to report truncation")
	}
	if len(fields) > maxSummaryFields {
		t.Fatalf("fields = %d, want at most %d", len(fields), maxSummaryFields)
	}

	// A list of scalars is a value; a list of objects is a structure, and its
	// length is the honest summary of it.
	fields, _ = summarize(map[string]any{
		"ports":      []any{float64(80), float64(443)},
		"containers": []any{map[string]any{"name": "app"}, map[string]any{"name": "sidecar"}},
		"empty":      []any{},
		"missing":    nil,
	})
	if !hasField(fields, "ports", "80, 443") {
		t.Fatalf("fields = %+v, want the scalar list joined", fields)
	}
	if !hasField(fields, "containers", "2 items") {
		t.Fatalf("fields = %+v, want the object list counted", fields)
	}
	if hasPath(fields, "empty") || hasPath(fields, "missing") {
		t.Fatalf("fields = %+v, want empty and null values left out", fields)
	}

	long := strings.Repeat("x", maxSummaryValue+50)
	fields, _ = summarize(map[string]any{"note": long})
	if len(fields) != 1 || len([]rune(fields[0].Value)) > maxSummaryValue+1 {
		t.Fatalf("fields = %+v, want the value truncated", fields)
	}
}

// A Secret's values are top-level `data`, not spec or status, so the flatten
// never walks them — the same rule the lists and the manifest editor follow.
func TestDescribeNeverSummarisesSecretData(t *testing.T) {
	view := describeObject(map[string]any{
		"kind": "Secret",
		"data": map[string]any{"password": "c3VwZXJzZWNyZXQ="},
		"metadata": map[string]any{"name": "db", "namespace": "shop"},
	}, "db", "shop")

	for _, field := range append(view.Spec, view.Status...) {
		if strings.Contains(field.Value, "c3VwZXJzZWNyZXQ=") {
			t.Fatalf("a secret value reached the describe response: %+v", field)
		}
	}
}

// Both event shapes reach the same view. An event written through the newer
// events.k8s.io API arrives on the core list with lastTimestamp and count empty;
// reading only the old shape shows a cluster's newest events with no time at all.
func TestEventViewReadsBothShapes(t *testing.T) {
	legacy := `{
		"type": "Warning", "reason": "BackOff", "message": "Back-off restarting failed container",
		"count": 7,
		"firstTimestamp": "2026-07-01T10:00:00Z",
		"lastTimestamp": "2026-07-01T12:00:00Z",
		"source": {"component": "kubelet", "host": "worker-1"}
	}`
	modern := `{
		"type": "Normal", "reason": "Scheduled", "message": "Successfully assigned shop/checkout",
		"eventTime": "2026-07-01T09:00:00Z",
		"series": {"count": 3, "lastObservedTime": "2026-07-01T11:00:00Z"},
		"reportingComponent": "default-scheduler"
	}`

	var old, recent eventObject
	if err := json.Unmarshal([]byte(legacy), &old); err != nil {
		t.Fatalf("decoding the legacy event: %v", err)
	}
	if err := json.Unmarshal([]byte(modern), &recent); err != nil {
		t.Fatalf("decoding the modern event: %v", err)
	}

	oldView := old.view()
	if oldView.Count != 7 || oldView.LastSeen == nil ||
		!oldView.LastSeen.Equal(time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("legacy view = %+v", oldView)
	}
	if oldView.Source != "kubelet, worker-1" {
		t.Fatalf("legacy source = %q, want the component and the host", oldView.Source)
	}

	newView := recent.view()
	if newView.Count != 3 {
		t.Fatalf("modern count = %d, want the series count", newView.Count)
	}
	if newView.LastSeen == nil ||
		!newView.LastSeen.Equal(time.Date(2026, 7, 1, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("modern view = %+v, want the series observation time", newView)
	}
	if newView.Source != "default-scheduler" {
		t.Fatalf("modern source = %q, want the reporting component", newView.Source)
	}

	// An event carrying no count at all still happened once.
	var bare eventObject
	if err := json.Unmarshal([]byte(`{"reason":"Created"}`), &bare); err != nil {
		t.Fatalf("decoding a bare event: %v", err)
	}
	if got := bare.view().Count; got != 1 {
		t.Fatalf("bare count = %d, want 1", got)
	}
}

// Describe addresses an object through the same fixed kind table the manifest
// editor uses, so a kind KubeMG does not serve is refused before anything
// reaches the tunnel.
func TestDescribeRefusesUnknownKind(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	base := "/api/v1/clusters/" + itoa(cluster.ID) + "/resources/describe"
	for _, query := range []string{
		// A real Kubernetes resource the sidebar does not browse — describe
		// addresses the inventory, not the API.
		"?kind=componentstatuses&name=etcd-0",
		"?kind=pods&namespace=shop",
		"?kind=crd:v1/v1/secrets&name=db&namespace=shop",
	} {
		rec := env.do(t, http.MethodGet, base+query, token, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q: expected status %d, got %d (%s)",
				query, http.StatusBadRequest, rec.Code, rec.Body.String())
		}
	}
}

// A cluster-scoped kind reaches past a namespace-scoped grant, and describe
// refuses it for the same reason every other cluster-wide read does.
func TestDescribeRefusesClusterScopeForScopedGrant(t *testing.T) {
	env := newTestEnv(t)
	user := env.store.addUser("scoped", "secret123", "user")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
	token := env.tokenFor(t, user)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/resources/describe?kind=nodes&name=worker-1",
		token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)",
			http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func hasField(fields []fieldView, path, value string) bool {
	return slices.ContainsFunc(fields, func(field fieldView) bool {
		return field.Path == path && field.Value == value
	})
}

func hasPath(fields []fieldView, path string) bool {
	return slices.ContainsFunc(fields, func(field fieldView) bool {
		return field.Path == path
	})
}
