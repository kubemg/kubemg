package helm

import (
	"encoding/json"
	"testing"
)

// The three-way merge is the least visible and most consequential thing here: it
// decides what an upgrade does to fields nobody in the chart ever wrote.

func merged(t *testing.T, original, modified, live string) map[string]any {
	t.Helper()
	document, err := ThreeWayMerge(bytesOf(original), bytesOf(modified), bytesOf(live))
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(document, &parsed); err != nil {
		t.Fatalf("merged document: %v", err)
	}
	return parsed
}

func bytesOf(document string) []byte {
	if document == "" {
		return nil
	}
	return []byte(document)
}

func TestAFieldTheAPIServerFilledInSurvivesAnUpgrade(t *testing.T) {
	// The failure a plain replace produces on the first upgrade of anything
	// real: a Service's allocated clusterIP is not in any render, and sending
	// the object back without it is a refused write at best.
	out := merged(t,
		`{"apiVersion":"v1","kind":"Service","metadata":{"name":"s"},"spec":{"ports":[{"port":80}]}}`,
		`{"apiVersion":"v1","kind":"Service","metadata":{"name":"s"},"spec":{"ports":[{"port":8080}]}}`,
		`{"apiVersion":"v1","kind":"Service","metadata":{"name":"s","resourceVersion":"41"},`+
			`"spec":{"clusterIP":"10.0.0.5","ports":[{"port":80}]}}`)

	spec, _ := out["spec"].(map[string]any)
	if spec["clusterIP"] != "10.0.0.5" {
		t.Fatalf("the allocated clusterIP was lost: %v", spec)
	}
}

func TestAFieldTheChartStoppedRenderingIsRemoved(t *testing.T) {
	// This is the only thing the *previous* revision's manifest is for: a
	// field present in the old render and absent from the new one was removed
	// by the chart and has to be cleared.
	out := merged(t,
		`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c"},"data":{"keep":"a","drop":"b"}}`,
		`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c"},"data":{"keep":"a"}}`,
		`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c","resourceVersion":"7"},`+
			`"data":{"keep":"a","drop":"b"}}`)

	data, _ := out["data"].(map[string]any)
	if _, present := data["drop"]; present {
		t.Fatalf("a field the chart stopped rendering survived: %v", data)
	}
}

func TestAFieldNobodyInTheChartEverWroteIsLeftAlone(t *testing.T) {
	// The other half of the same rule: a label a controller or an operator added
	// was never Helm's, so an upgrade must not remove it.
	out := merged(t,
		`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c"},"data":{"a":"1"}}`,
		`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c"},"data":{"a":"2"}}`,
		`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c","resourceVersion":"7",`+
			`"labels":{"added-by":"someone-else"}},"data":{"a":"1"}}`)

	metadata, _ := out["metadata"].(map[string]any)
	labels, _ := metadata["labels"].(map[string]any)
	if labels["added-by"] != "someone-else" {
		t.Fatalf("a label nothing in the chart wrote was removed: %v", metadata)
	}
	data, _ := out["data"].(map[string]any)
	if data["a"] != "2" {
		t.Fatalf("the chart's own change was not applied: %v", data)
	}
}

func TestTheResourceVersionSurvivesSoTheWriteStaysConditional(t *testing.T) {
	// Losing it turns a conditional write into an unconditional one, which is
	// the whole guard against clobbering somebody else's concurrent change.
	out := merged(t,
		`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c"},"data":{"a":"1"}}`,
		`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c"},"data":{"a":"2"}}`,
		`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c","resourceVersion":"99",`+
			`"uid":"abc","creationTimestamp":"2024-01-01T00:00:00Z"},"data":{"a":"1"}}`)

	metadata, _ := out["metadata"].(map[string]any)
	if metadata["resourceVersion"] != "99" {
		t.Fatalf("resourceVersion = %v", metadata["resourceVersion"])
	}
	if metadata["uid"] != "abc" || metadata["creationTimestamp"] != "2024-01-01T00:00:00Z" {
		t.Fatalf("identity fields were moved: %v", metadata)
	}
}

func TestASidecarSomethingElseInjectedSurvivesAStrategicMerge(t *testing.T) {
	// `containers` is keyed by name rather than being a list to replace. A JSON
	// merge patch replaces it wholesale, which is how a sidecar added by a
	// mutating webhook disappears on every upgrade — the exact reason a
	// registered kind gets a strategic merge.
	out := merged(t,
		`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"p"},"spec":{"containers":`+
			`[{"name":"app","image":"app:1"}]}}`,
		`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"p"},"spec":{"containers":`+
			`[{"name":"app","image":"app:2"}]}}`,
		`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"p","resourceVersion":"3"},`+
			`"spec":{"containers":[{"name":"app","image":"app:1"},{"name":"sidecar","image":"mesh:1"}]}}`)

	spec, _ := out["spec"].(map[string]any)
	containers, _ := spec["containers"].([]any)
	names := map[string]string{}
	for _, entry := range containers {
		container, _ := entry.(map[string]any)
		names[container["name"].(string)], _ = container["image"].(string)
	}
	if names["sidecar"] != "mesh:1" {
		t.Fatalf("the injected sidecar was replaced away: %v", containers)
	}
	if names["app"] != "app:2" {
		t.Fatalf("the chart's own image change did not land: %v", containers)
	}
}

func TestACustomResourceMergesWithoutASchema(t *testing.T) {
	// No Go type exists for a CRD's kind anywhere, so this is the JSON merge
	// patch fallback — the same one Helm falls back to, for the same reason.
	out := merged(t,
		`{"apiVersion":"example.com/v1","kind":"Widget","metadata":{"name":"w"},"spec":{"size":1}}`,
		`{"apiVersion":"example.com/v1","kind":"Widget","metadata":{"name":"w"},"spec":{"size":2}}`,
		`{"apiVersion":"example.com/v1","kind":"Widget","metadata":{"name":"w","resourceVersion":"5"},`+
			`"spec":{"size":1,"status":"ready"}}`)

	spec, _ := out["spec"].(map[string]any)
	if spec["size"] != float64(2) {
		t.Fatalf("the change did not apply: %v", spec)
	}
	if spec["status"] != "ready" {
		t.Fatalf("a field outside the chart was removed: %v", spec)
	}
}

func TestAnObjectWithNoPreviousRenderRemovesNothing(t *testing.T) {
	// A release adopting an object somebody else created, or an upgrade from a
	// revision whose manifest was not recorded. Nothing here can tell which of
	// the live object's fields Helm used to own, so nothing is removed.
	out := merged(t, "",
		`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c"},"data":{"a":"2"}}`,
		`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c","resourceVersion":"1"},`+
			`"data":{"a":"1","untouched":"yes"}}`)

	data, _ := out["data"].(map[string]any)
	if data["untouched"] != "yes" {
		t.Fatalf("a two-way merge removed something: %v", data)
	}
	if data["a"] != "2" {
		t.Fatalf("the change did not apply: %v", data)
	}
}

func TestAnObjectThatIsNotThereYetIsSentAsRendered(t *testing.T) {
	document, err := ThreeWayMerge(nil, []byte(`{"apiVersion":"v1","kind":"ConfigMap"}`), nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if string(document) != `{"apiVersion":"v1","kind":"ConfigMap"}` {
		t.Fatalf("a create was rewritten: %s", document)
	}
}

func TestAFieldSomebodyElseChangedIsNotAPermanentConflict(t *testing.T) {
	// The failure this pins is the ordinary one, not an exotic one: `helm
	// upgrade` run once from a terminal against a release installed here
	// changes the live object without changing the recorded manifest, and with
	// a conflict-reporting merge every field it touched would refuse to compute
	// for ever — the object becomes permanently un-upgradeable and
	// un-rollback-able. Helm's own updateResource passes overwrite=true for
	// exactly this reason, and this asserts we do the same.
	out := merged(t,
		`{"apiVersion":"v1","kind":"Service","metadata":{"name":"s"},"spec":{"ports":[{"port":80}]}}`,
		`{"apiVersion":"v1","kind":"Service","metadata":{"name":"s"},"spec":{"ports":[{"port":8080}]}}`,
		`{"apiVersion":"v1","kind":"Service","metadata":{"name":"s","resourceVersion":"2"},`+
			`"spec":{"ports":[{"port":9090}]}}`)

	spec, _ := out["spec"].(map[string]any)
	ports, _ := spec["ports"].([]any)
	first, _ := ports[0].(map[string]any)
	if first["port"] != float64(8080) {
		t.Fatalf("the chart did not win a field somebody else had also changed: %v", spec)
	}
}
