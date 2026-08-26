package helm

import (
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
)

// The rule this file exists for: what KubeMG writes has to be what the `helm`
// CLI reads back. A release that is nearly right is worse than no install
// button — it is a trap that springs weeks later, on somebody who did not use
// this console.

func TestASecretIsNamedTheWayHelmLooksForIt(t *testing.T) {
	if got := SecretName("my-app", 3); got != "sh.helm.release.v1.my-app.v3" {
		t.Fatalf("secret name = %q", got)
	}
}

func TestTheFourLabelsHelmQueriesByAreAllWritten(t *testing.T) {
	// `helm list` filters on the label; `helm history` reads the payload. Both
	// have to say the same thing, and writing one without the other is how they
	// come to disagree.
	labels := Labels("my-app", 3, StatusDeployed)
	for key, want := range map[string]string{
		"owner": "helm", "name": "my-app", "status": "deployed", "version": "3",
	} {
		if labels[key] != want {
			t.Fatalf("label %s = %q, want %q", key, labels[key], want)
		}
	}
	if len(labels) != 4 {
		t.Fatalf("a fifth label was written: %v", labels)
	}
}

func TestAReleaseRoundTripsThroughItsStoredForm(t *testing.T) {
	loaded := &chart.Chart{
		Metadata:  &chart.Metadata{APIVersion: "v2", Name: "demo", Version: "1.2.3", AppVersion: "9"},
		Values:    map[string]any{"replicas": 1},
		Templates: []*chart.File{{Name: "templates/cm.yaml", Data: []byte("kind: ConfigMap")}},
	}
	release := NewRelease("app", "team-a", 1, loaded,
		map[string]any{"replicas": 3},
		&Rendered{Manifest: "---\nkind: ConfigMap\n", Notes: "hello"},
		StatusDeployed, "Install complete")

	document, err := release.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	back, err := ParseRelease(document)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if back.Name != "app" || back.Namespace != "team-a" || back.Version != 1 {
		t.Fatalf("identity did not survive: %+v", back)
	}
	if back.Info.Status != StatusDeployed || back.Info.Notes != "hello" {
		t.Fatalf("info did not survive: %+v", back.Info)
	}
	if back.Config["replicas"] != float64(3) {
		t.Fatalf("config did not survive: %v", back.Config)
	}
	if back.Chart == nil || back.Chart.Metadata.Version != "1.2.3" {
		t.Fatalf("the chart did not survive: %+v", back.Chart)
	}
	if len(back.Chart.Templates) != 1 {
		t.Fatalf("the templates did not survive — `helm rollback` re-renders from them")
	}
}

func TestAnUpgradeKeepsWhenTheReleaseWasFirstDeployed(t *testing.T) {
	// `helm history` shows it, and an upgrade that resets it makes a two-year-old
	// release look like it was installed this afternoon.
	previous := &Release{
		Name: "app", Namespace: "team-a", Version: 4,
		Info: Info{FirstDeployed: "2022-01-01T00:00:00Z"},
	}
	next := NextRelease(previous, nil, nil, &Rendered{}, StatusDeployed, "Upgrade complete")

	if next.Version != 5 {
		t.Fatalf("version = %d, want 5", next.Version)
	}
	if next.Info.FirstDeployed != "2022-01-01T00:00:00Z" {
		t.Fatalf("first_deployed was reset to %q", next.Info.FirstDeployed)
	}
	if next.Info.LastDeployed == previous.Info.FirstDeployed {
		t.Fatal("last_deployed was not moved")
	}
}

func TestSubchartsAreReattachedRatherThanLeftLoose(t *testing.T) {
	// A subchart tree that is present in the JSON but never attached renders as
	// though every subchart were disabled — a valid-looking release with half
	// the objects missing.
	parent := &chart.Chart{Metadata: &chart.Metadata{APIVersion: "v2", Name: "parent", Version: "1"}}
	parent.AddDependency(&chart.Chart{
		Metadata: &chart.Metadata{APIVersion: "v2", Name: "child", Version: "1"},
	})

	release := NewRelease("app", "team-a", 1, parent, nil, &Rendered{}, StatusDeployed, "")
	document, _ := release.Encode()
	back, err := ParseRelease(document)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	loaded, err := back.LoadedChart()
	if err != nil {
		t.Fatalf("chart: %v", err)
	}
	if len(loaded.Dependencies()) != 1 {
		t.Fatalf("the subchart was not re-attached: %d dependencies", len(loaded.Dependencies()))
	}
	if loaded.Dependencies()[0].Metadata.Name != "child" {
		t.Fatalf("wrong subchart: %+v", loaded.Dependencies()[0].Metadata)
	}
}

func TestAReleaseWithNoChartSaysSoRatherThanRenderingNothing(t *testing.T) {
	release := &Release{Name: "app"}
	_, err := release.LoadedChart()
	if err == nil || !strings.Contains(err.Error(), "does not carry its chart") {
		t.Fatalf("expected a named refusal, got %v", err)
	}
}

func TestARecordedManifestSplitsBackIntoTheObjectsItHolds(t *testing.T) {
	manifest := "---\n# Source: demo/templates/cm.yaml\n" +
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n" +
		"---\n# Source: demo/templates/svc.yaml\n" +
		"apiVersion: v1\nkind: Service\nmetadata:\n  name: s\n"

	objects, err := ManifestObjects(manifest, "team-a")
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(objects) != 2 {
		t.Fatalf("expected two objects, got %d: %+v", len(objects), objects)
	}
	if objects[0].Kind != "ConfigMap" || objects[1].Kind != "Service" {
		t.Fatalf("order or kinds wrong: %+v", objects)
	}
	if objects[0].Source != "demo/templates/cm.yaml" {
		t.Fatalf("the source header was not read back: %q", objects[0].Source)
	}
	if objects[0].Namespace != "team-a" {
		t.Fatalf("the release's namespace was not applied: %q", objects[0].Namespace)
	}
}

func TestThreeDashesInsideAConfigFileIsNotADocumentBreak(t *testing.T) {
	// `---` inside a block scalar is somebody's config file. A naive split cuts
	// the ConfigMap in half and loses the object.
	manifest := "---\n# Source: demo/templates/cm.yaml\n" +
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\ndata:\n  app.yaml: |\n" +
		"    first: 1\n    ---\n    second: 2\n"

	objects, err := ManifestObjects(manifest, "team-a")
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(objects) != 1 {
		t.Fatalf("expected one object, got %d: %+v", len(objects), objects)
	}
	if !strings.Contains(objects[0].YAML, "second: 2") {
		t.Fatalf("the embedded document was cut: %q", objects[0].YAML)
	}
}

func TestAnUnreadableDocumentIsSkippedRatherThanLosingTheRelease(t *testing.T) {
	// A Helm old enough to have written something this cannot read. Skipping
	// one document loses the ability to remove that object; failing would lose
	// the release.
	manifest := "---\nthis: is not an object\n" +
		"---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n"

	objects, err := ManifestObjects(manifest, "team-a")
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(objects) != 1 || objects[0].Name != "c" {
		t.Fatalf("expected the readable object only, got %+v", objects)
	}
}

func TestAReleaseInstalledWithNoValuesRecordsAMappingRatherThanNothing(t *testing.T) {
	// `helm upgrade --reset-values` leaves exactly this, and Helm writes `{}`.
	release := NewRelease("app", "team-a", 1, nil, nil, &Rendered{}, StatusDeployed, "")
	if release.Config == nil {
		t.Fatal("config is nil — Helm writes an empty mapping")
	}
}
