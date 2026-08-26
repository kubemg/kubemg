package helm

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
)

// testChart builds a chart in memory. The templates are deliberately the shapes
// that break a naive implementation: an object gated on a value, a hook, a CRD
// in `crds/`, and NOTES.txt.
func testChart(templates map[string]string, values map[string]any) *chart.Chart {
	loaded := &chart.Chart{
		Metadata: &chart.Metadata{
			APIVersion: "v2",
			Name:       "demo",
			Version:    "1.0.0",
			AppVersion: "9.9",
		},
		Values: values,
	}
	for name, body := range templates {
		loaded.Templates = append(loaded.Templates, &chart.File{
			Name: "templates/" + name, Data: []byte(body),
		})
	}
	slices.SortFunc(loaded.Templates, func(a, b *chart.File) int {
		return strings.Compare(a.Name, b.Name)
	})
	return loaded
}

func render(t *testing.T, loaded *chart.Chart, values map[string]any) *Rendered {
	t.Helper()
	rendered, err := Render(loaded, values, ReleaseMeta{
		Name: "app", Namespace: "team-a", Revision: 1, IsInstall: true,
	}, chartutil.DefaultCapabilities)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return rendered
}

func TestARenderedObjectWithNoNamespaceTakesTheReleases(t *testing.T) {
	// What `kubectl apply -n` does and what Helm does. The alternative — leaving
	// it unset — means the object lands wherever the API path says, which is the
	// same place, but the *recorded* manifest would then not say where it went,
	// and an upgrade could not match it against the next render.
	rendered := render(t, testChart(map[string]string{
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}-config\n",
	}, nil), nil)

	if len(rendered.Objects) != 1 {
		t.Fatalf("expected one object, got %d", len(rendered.Objects))
	}
	if got := rendered.Objects[0].Namespace; got != "team-a" {
		t.Fatalf("namespace = %q, want team-a", got)
	}
	if got := rendered.Objects[0].Name; got != "app-config" {
		t.Fatalf("name = %q — .Release.Name did not reach the template", got)
	}

	// And it is in the document, not only in the struct: the write sends the
	// document.
	var parsed map[string]any
	if err := json.Unmarshal(rendered.Objects[0].Document, &parsed); err != nil {
		t.Fatalf("document: %v", err)
	}
	metadata, _ := parsed["metadata"].(map[string]any)
	if metadata["namespace"] != "team-a" {
		t.Fatalf("the document does not carry the namespace: %v", metadata)
	}
}

func TestAnObjectThatNamesItsOwnNamespaceKeepsIt(t *testing.T) {
	// A chart that deliberately installs into kube-system is a real chart.
	rendered := render(t, testChart(map[string]string{
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n  namespace: kube-system\n",
	}, nil), nil)

	if got := rendered.Objects[0].Namespace; got != "kube-system" {
		t.Fatalf("namespace = %q, want kube-system", got)
	}
}

func TestATemplateSwitchedOffByAValueRendersNothingRatherThanFailing(t *testing.T) {
	loaded := testChart(map[string]string{
		"cm.yaml": "{{- if .Values.enabled }}\napiVersion: v1\nkind: ConfigMap\n" +
			"metadata:\n  name: c\n{{- end }}\n",
		"always.yaml": "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n",
	}, map[string]any{"enabled": true})

	rendered := render(t, loaded, map[string]any{"enabled": false})
	if len(rendered.Objects) != 1 || rendered.Objects[0].Kind != "Secret" {
		t.Fatalf("expected only the ungated object, got %+v", rendered.Objects)
	}
}

func TestObjectsAreWrittenInHelmsInstallOrder(t *testing.T) {
	// A Deployment applied before the ConfigMap it mounts is not fatal, but it
	// makes a healthy install look broken for a minute — which an operator
	// watching a report reads as a failure.
	rendered := render(t, testChart(map[string]string{
		"z-deploy.yaml": "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: d\n",
		"a-cm.yaml":     "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n",
		"m-ns.yaml":     "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: ns\n",
	}, nil), nil)

	kinds := make([]string, 0, 3)
	for _, object := range rendered.Objects {
		kinds = append(kinds, object.Kind)
	}
	if !slices.Equal(kinds, []string{"Namespace", "ConfigMap", "Deployment"}) {
		t.Fatalf("install order = %v, want Namespace, ConfigMap, Deployment", kinds)
	}
}

func TestACRDIsSeparateFromTheReleaseAndOutOfTheRecordedManifest(t *testing.T) {
	// Helm does not record CRDs on the release, and it must not: a CRD outlives
	// the release that happened to install it, and recording it would make a
	// later uninstall delete a kind other releases are using.
	loaded := testChart(map[string]string{
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n",
	}, nil)
	loaded.Files = append(loaded.Files, &chart.File{
		Name: "crds/widget.yaml",
		Data: []byte("apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\n" +
			"metadata:\n  name: widgets.example.com\n"),
	})

	rendered := render(t, loaded, nil)
	if len(rendered.CRDs) != 1 || rendered.CRDs[0].Kind != "CustomResourceDefinition" {
		t.Fatalf("expected one CRD, got %+v", rendered.CRDs)
	}
	if strings.Contains(rendered.Manifest, "CustomResourceDefinition") {
		t.Fatal("the recorded manifest must not carry CRDs")
	}
	// And a CRD is cluster-scoped, so it carries no namespace even though the
	// release has one.
	if rendered.CRDs[0].Namespace != "" {
		t.Fatalf("a CRD took the release's namespace: %q", rendered.CRDs[0].Namespace)
	}
}

func TestHooksAreSplitOutOfTheReleaseAndOrderedByWeight(t *testing.T) {
	rendered := render(t, testChart(map[string]string{
		"post.yaml": "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: after\n" +
			"  annotations:\n    \"helm.sh/hook\": post-install\n    \"helm.sh/hook-weight\": \"5\"\n",
		"pre-late.yaml": "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: pre-late\n" +
			"  annotations:\n    \"helm.sh/hook\": pre-install\n    \"helm.sh/hook-weight\": \"10\"\n",
		"pre-early.yaml": "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: pre-early\n" +
			"  annotations:\n    \"helm.sh/hook\": pre-install\n    \"helm.sh/hook-weight\": \"-5\"\n",
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n",
	}, nil), nil)

	if len(rendered.Objects) != 1 {
		t.Fatalf("hooks leaked into the release proper: %+v", rendered.Objects)
	}
	if len(rendered.PreInstall) != 2 || len(rendered.PostInstall) != 1 {
		t.Fatalf("hooks split wrongly: pre=%d post=%d", len(rendered.PreInstall), len(rendered.PostInstall))
	}
	if rendered.PreInstall[0].Name != "pre-early" {
		t.Fatalf("pre-install hooks out of weight order: %+v", rendered.PreInstall)
	}
}

func TestATestHookIsNotRun(t *testing.T) {
	// `helm test` is a separate command an operator invokes deliberately.
	// Running one as part of an install would be this console doing something
	// nobody asked for, in a namespace it was asked to install into.
	rendered := render(t, testChart(map[string]string{
		"test.yaml": "apiVersion: v1\nkind: Pod\nmetadata:\n  name: probe\n" +
			"  annotations:\n    \"helm.sh/hook\": test\n",
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n",
	}, nil), nil)

	if len(rendered.PreInstall) != 0 || len(rendered.PostInstall) != 0 {
		t.Fatalf("a test hook was scheduled: pre=%+v post=%+v", rendered.PreInstall, rendered.PostInstall)
	}
}

func TestNotesAreReadBackAndAreNotAnObject(t *testing.T) {
	rendered := render(t, testChart(map[string]string{
		"NOTES.txt": "Visit http://{{ .Release.Name }}.example.com\n",
		"cm.yaml":   "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n",
	}, nil), nil)

	if rendered.Notes != "Visit http://app.example.com" {
		t.Fatalf("notes = %q", rendered.Notes)
	}
	if len(rendered.Objects) != 1 {
		t.Fatalf("NOTES.txt reached the sorter: %+v", rendered.Objects)
	}
}

func TestAPartialIsNotAnObject(t *testing.T) {
	rendered := render(t, testChart(map[string]string{
		"_helpers.tpl": `{{- define "demo.name" -}}demo{{- end -}}`,
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  " +
			`name: {{ include "demo.name" . }}` + "\n",
	}, nil), nil)

	if len(rendered.Objects) != 1 || rendered.Objects[0].Name != "demo" {
		t.Fatalf("partials mishandled: %+v", rendered.Objects)
	}
}

func TestACapabilityCheckSeesWhatTheClusterServes(t *testing.T) {
	// Half the ecosystem's charts decide whether to emit an Ingress this way.
	loaded := testChart(map[string]string{
		"ing.yaml": `{{- if .Capabilities.APIVersions.Has "route.openshift.io/v1" }}
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: r
{{- else }}
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: i
{{- end }}
`,
	}, nil)

	plain := render(t, loaded, nil)
	if plain.Objects[0].Kind != "Ingress" {
		t.Fatalf("expected an Ingress on a plain cluster, got %s", plain.Objects[0].Kind)
	}

	openshift, err := Render(loaded, nil, ReleaseMeta{Name: "app", Namespace: "team-a", Revision: 1},
		&chartutil.Capabilities{
			KubeVersion: chartutil.DefaultCapabilities.KubeVersion,
			APIVersions: chartutil.VersionSet{"v1", "route.openshift.io/v1"},
			HelmVersion: chartutil.DefaultCapabilities.HelmVersion,
		})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if openshift.Objects[0].Kind != "Route" {
		t.Fatalf("the capability set did not reach the template: %s", openshift.Objects[0].Kind)
	}
}

func TestATemplateErrorNamesTheFile(t *testing.T) {
	// The single most common failure of an install. Helm's own message already
	// names the file and the line; replacing it with a generic sentence is how
	// a fixable problem becomes a support ticket.
	_, err := Render(testChart(map[string]string{
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Values.missing.deep }}\n",
	}, nil), nil, ReleaseMeta{Name: "app", Namespace: "team-a", Revision: 1},
		chartutil.DefaultCapabilities)

	if err == nil {
		t.Fatal("expected a template error")
	}
	if !strings.Contains(err.Error(), "cm.yaml") {
		t.Fatalf("the error does not name the file: %v", err)
	}
}

func TestAChartThatRendersNothingIsRefusedRatherThanInstalledEmpty(t *testing.T) {
	_, err := Render(testChart(map[string]string{
		"cm.yaml": "{{- if .Values.enabled }}\napiVersion: v1\nkind: ConfigMap\n" +
			"metadata:\n  name: c\n{{- end }}\n",
	}, map[string]any{"enabled": true}), map[string]any{"enabled": false},
		ReleaseMeta{Name: "app", Namespace: "team-a", Revision: 1}, chartutil.DefaultCapabilities)

	if err == nil || !strings.Contains(err.Error(), "no objects") {
		t.Fatalf("expected a refusal naming the empty render, got %v", err)
	}
}

func TestALibraryChartIsRefusedBeforeAnythingIsRendered(t *testing.T) {
	loaded := testChart(nil, nil)
	loaded.Metadata.Type = "library"
	if err := Installable(loaded); err == nil || !strings.Contains(err.Error(), "library chart") {
		t.Fatalf("expected a library refusal, got %v", err)
	}
}

func TestAVersionWithAPlusInItStillParses(t *testing.T) {
	// Every managed provider reports one, and a `minor` of "31+" would make a
	// chart's own kubeVersion comparison refuse to parse the whole version.
	version, err := KubeVersionOf("1", "31+", "v1.31.4-eks-2d5f260")
	if err != nil {
		t.Fatalf("KubeVersionOf: %v", err)
	}
	if version.Minor != "31" {
		t.Fatalf("minor = %q, want 31", version.Minor)
	}
	if version.Version != "v1.31.4-eks-2d5f260" {
		t.Fatalf("version = %q", version.Version)
	}
}

func TestAClusterThatReportsNoVersionIsAnErrorRatherThanAGuess(t *testing.T) {
	if _, err := KubeVersionOf("", "", ""); err == nil {
		t.Fatal("expected an error rather than a default version")
	}
}

func TestRenderingDoesNotDeleteASubchartFromTheCallersChart(t *testing.T) {
	// `ProcessDependenciesWithMerge` removes the subcharts a condition switched
	// off, which is right for the render and wrong for the chart the caller is
	// about to store on the release: a stored chart with its disabled subcharts
	// deleted can never have them turned back on, and a values edit that only
	// flips `redis.enabled` back to true would render nothing.
	parent := testChart(map[string]string{
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n",
	}, map[string]any{"redis": map[string]any{"enabled": true}})
	parent.Metadata.Dependencies = []*chart.Dependency{
		{Name: "redis", Version: "1.0.0", Condition: "redis.enabled"},
	}
	parent.AddDependency(&chart.Chart{
		Metadata: &chart.Metadata{APIVersion: "v2", Name: "redis", Version: "1.0.0"},
	})

	render(t, parent, map[string]any{"redis": map[string]any{"enabled": false}})

	if len(parent.Dependencies()) != 1 {
		t.Fatal("the render deleted the disabled subchart from the caller's chart — " +
			"storing that chart would make the subchart unrecoverable")
	}
}

func TestAnObjectIsStampedWithHelmsOwnershipMetadata(t *testing.T) {
	// `helm` reads these to decide whether an object that already exists is one
	// it may adopt. Without them a later `helm install` of the same chart
	// refuses what this console created with "invalid ownership metadata", and
	// every three-way merge after Helm has touched the object diffs on
	// annotations nothing in the chart ever wrote.
	stamped := WithOwnership(
		[]byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c"}}`), "app", "team-a")

	var parsed map[string]any
	if err := json.Unmarshal(stamped, &parsed); err != nil {
		t.Fatalf("stamped document: %v", err)
	}
	metadata, _ := parsed["metadata"].(map[string]any)
	labels, _ := metadata["labels"].(map[string]any)
	annotations, _ := metadata["annotations"].(map[string]any)

	if labels["app.kubernetes.io/managed-by"] != "Helm" {
		t.Fatalf("labels = %v", labels)
	}
	if annotations["meta.helm.sh/release-name"] != "app" ||
		annotations["meta.helm.sh/release-namespace"] != "team-a" {
		t.Fatalf("annotations = %v", annotations)
	}
}

func TestAChartsOwnManagedByLabelIsOverwrittenTheWayHelmOverwritesIt(t *testing.T) {
	// Helm passes `force: true` here, on the argument that these are the
	// resources the chart is rendering right now. Matching it is the point:
	// leaving the chart's value alone would mean `helm upgrade` flipping the
	// label on its next run and this console flipping it back, so every upgrade
	// would diff on a field neither of them cares about.
	stamped := WithOwnership([]byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"c",`+
		`"labels":{"app.kubernetes.io/managed-by":"something-else"}}}`), "app", "team-a")

	if strings.Contains(string(stamped), "something-else") {
		t.Fatalf("the chart's value survived where Helm would have replaced it: %s", stamped)
	}
}

func TestACRDIsNotStampedWithTheReleaseThatInstalledIt(t *testing.T) {
	// A CRD outlives the release that happened to install it — it is not
	// recorded on the release either — so claiming ownership of one on a
	// release's behalf would be a claim the next uninstall could act on.
	loaded := testChart(map[string]string{
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n",
	}, nil)
	loaded.Files = append(loaded.Files, &chart.File{
		Name: "crds/widget.yaml",
		Data: []byte("apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\n" +
			"metadata:\n  name: widgets.example.com\n"),
	})

	rendered := render(t, loaded, nil)
	if !rendered.CRDs[0].CRD {
		t.Fatal("a crds/ object is not marked as one, so the apply cannot tell it apart")
	}
	if rendered.Objects[0].CRD {
		t.Fatal("a template object was marked as a CRD")
	}
}

func TestOwnershipIsNotRecordedInTheManifest(t *testing.T) {
	// Helm stamps at write time, not into the render, and recording it would
	// make it look like something the chart declared — a chart that later
	// declared its own managed-by label could then never change it.
	rendered := render(t, testChart(map[string]string{
		"cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n",
	}, nil), nil)

	if strings.Contains(rendered.Manifest, "meta.helm.sh") {
		t.Fatalf("the recorded manifest carries ownership metadata: %s", rendered.Manifest)
	}
}
