package helm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"
)

/*
 * Rendering, and the order things are written in.
 *
 * This is the whole reason the Helm library is here rather than a YAML walk.
 * `.Values` is not the values document — it is the chart's own defaults, deep
 * merged with every subchart's, with the operator's on top, with `global`
 * threaded through, with subcharts switched on and off by `condition` and
 * `tags`. `.Capabilities.APIVersions.Has` is how half the ecosystem's charts
 * decide whether to emit an Ingress or a Route. `tpl` renders a value as a
 * template. None of that is optional if the goal is that `helm upgrade` run
 * afterwards produces the same objects.
 *
 * Order is the other half, and it is Helm's, not this file's:
 *
 *   1. CRDs, from `crds/`, first and separately. A chart that defines a CRD and
 *      an instance of it in the same release cannot have them applied in one
 *      pass — the API server does not serve the kind until the definition is
 *      established.
 *   2. Everything else in `releaseutil.InstallOrder`: Namespace, then the things
 *      objects reference (ServiceAccount, Secret, ConfigMap, PV/PVC, RBAC),
 *      then the workloads. Applying a Deployment before the ConfigMap it mounts
 *      is not fatal — the kubelet retries — but it makes a healthy install look
 *      like a broken one for a minute, which an operator watching a report will
 *      read as a failure.
 *   3. Hooks, in weight order, on either side of the objects.
 */

// ReleaseMeta is what a chart is told about the release it is being rendered
// for. These are the `.Release.*` values, and they are not cosmetic: charts
// name objects from `.Release.Name`, key ConfigMaps by `.Release.Namespace`,
// and branch on `.Release.IsUpgrade` to skip a one-time job.
type ReleaseMeta struct {
	Name      string
	Namespace string
	Revision  int
	IsInstall bool
	IsUpgrade bool
}

// Object is one rendered Kubernetes object, carried as both the YAML the chart
// produced and the JSON that goes down the tunnel.
//
// It keeps its source file because that is the only thing an operator can act
// on when a chart renders something that will not apply: "the Deployment was
// refused" is a support ticket, "templates/deployment.yaml rendered a Deployment
// the cluster refused, and here is what it said" is a fix.
type Object struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
	Source     string `json:"source,omitempty"`

	// Hook is set for an object Helm would run as a hook rather than install as
	// part of the release. Events and Weight order them.
	Hook bool `json:"hook,omitempty"`
	// CRD is set for an object out of the chart's `crds/` directory. It is not
	// part of the release — it is not recorded on it and it does not carry the
	// release's ownership metadata — because a CRD outlives the release that
	// happened to install it.
	CRD    bool     `json:"crd,omitempty"`
	Events []string `json:"events,omitempty"`
	Weight int      `json:"weight,omitempty"`

	// Document is the object as JSON, ready to be written. YAML is what the
	// chart produced and is kept for the manifest the release records, which
	// has to be byte-comparable with what `helm` itself would have stored.
	Document []byte `json:"-"`
	YAML     string `json:"-"`
}

// Ref is how one object is named in a report and matched across two revisions.
// Namespace is part of it because a chart may install into more than one.
func (o Object) Ref() string {
	if o.Namespace == "" {
		return o.Kind + "/" + o.Name
	}
	return o.Namespace + "/" + o.Kind + "/" + o.Name
}

// Rendered is a chart turned into objects, split the way Helm splits them.
type Rendered struct {
	// CRDs come from `crds/`, are applied before anything else, and are
	// deliberately **not** part of the recorded manifest — Helm does not record
	// them either, which is why `helm uninstall` leaves CRDs behind.
	CRDs []Object
	// Objects is the release proper, in install order.
	Objects []Object
	// PreInstall and PostInstall are the hooks either side of it, in weight
	// order. They are separate fields rather than a filter over one list
	// because when they run is the whole of what makes them hooks.
	PreInstall  []Object
	PostInstall []Object

	// Manifest is what the release records: the objects, in order, as one
	// document. It is what a later upgrade diffs against and what a rollback
	// re-applies, so it is produced here rather than reassembled later.
	Manifest string
	// Notes is the rendered NOTES.txt, which is the one piece of a chart's
	// output meant for a person rather than for the API server.
	Notes string
	// Values is the merged, coalesced value set the chart actually rendered
	// with. It is recorded on the release as `chart.values` would be; what the
	// operator supplied is recorded separately as `config`.
	Values map[string]any
}

// Render turns a chart and a values document into the objects that make up a
// release.
//
// `chartutil.ProcessDependenciesWithMerge` is called before rendering and not
// after: it is what applies `condition` and `tags`, and a subchart switched off
// by a value has to be removed from the chart *before* the engine walks its
// templates, or it renders and then has to be un-rendered.
func Render(loaded *chart.Chart, values map[string]any,
	meta ReleaseMeta, capabilities *chartutil.Capabilities,
) (*Rendered, error) {
	if err := Installable(loaded); err != nil {
		return nil, err
	}
	if values == nil {
		values = map[string]any{}
	}
	if capabilities == nil {
		capabilities = chartutil.DefaultCapabilities
	}

	// `ProcessDependenciesWithMerge` **mutates the chart**: it removes the
	// subcharts a `condition` or a `tags` value switched off, which is exactly
	// what has to happen before the engine walks the templates. It must not
	// happen to the caller's chart, though, because the caller goes on to
	// *store* that chart on the release — and storing a chart with its disabled
	// subcharts deleted means the subchart is gone for good, and no later values
	// edit can ever turn it back on. So the render works on a copy.
	loaded = cloneChart(loaded)
	if err := chartutil.ProcessDependenciesWithMerge(loaded, values); err != nil {
		return nil, fmt.Errorf("this chart's dependencies could not be resolved: %w", err)
	}

	options := chartutil.ReleaseOptions{
		Name:      meta.Name,
		Namespace: meta.Namespace,
		Revision:  meta.Revision,
		IsInstall: meta.IsInstall,
		IsUpgrade: meta.IsUpgrade,
	}
	renderValues, err := chartutil.ToRenderValues(loaded, values, options, capabilities)
	if err != nil {
		return nil, fmt.Errorf("this chart's values could not be prepared: %w", err)
	}

	files, err := engine.Render(loaded, renderValues)
	if err != nil {
		// A template error is the single most common failure of an install and
		// the message Helm produces already names the file and the line. It is
		// passed through rather than replaced.
		return nil, fmt.Errorf("this chart did not render: %w", err)
	}

	rendered := &Rendered{Values: mergedValuesOf(renderValues)}
	rendered.Notes, files = takeNotes(files)

	hooks, manifests, err := releaseutil.SortManifests(files, capabilities.APIVersions, releaseutil.InstallOrder)
	if err != nil {
		return nil, fmt.Errorf("this chart rendered something that is not a Kubernetes object: %w", err)
	}

	rendered.Objects, rendered.Manifest, err = objectsOf(manifests, meta.Namespace)
	if err != nil {
		return nil, err
	}
	if rendered.CRDs, err = crdsOf(loaded); err != nil {
		return nil, err
	}
	rendered.PreInstall, rendered.PostInstall = hookObjects(hooks, meta)

	if len(rendered.Objects) == 0 && len(rendered.CRDs) == 0 &&
		len(rendered.PreInstall) == 0 && len(rendered.PostInstall) == 0 {
		return nil, fmt.Errorf("this chart rendered no objects with the values it was given")
	}
	return rendered, nil
}

// takeNotes pulls NOTES.txt out of the rendered set. It is not a manifest and
// must never reach the sorter, which would report it as an object with no kind.
// A subchart's NOTES are ignored the way Helm ignores them.
func takeNotes(files map[string]string) (string, map[string]string) {
	notes := ""
	kept := make(map[string]string, len(files))
	for name, content := range files {
		base := name[strings.LastIndex(name, "/")+1:]
		if base == "NOTES.txt" {
			// Only the top-level chart's notes are shown, which is Helm's rule:
			// a subchart's notes are addressed to somebody installing it alone.
			// A subchart is identified by the `charts/` segment the loader puts
			// in its path rather than by counting separators, because the
			// top-level path already has two.
			if !strings.Contains(name, "/charts/") {
				notes = content
			}
			continue
		}
		if strings.HasPrefix(base, "_") {
			// Partials. They define templates and render to nothing.
			continue
		}
		kept[name] = content
	}
	return strings.TrimSpace(notes), kept
}

// objectsOf turns sorted manifests into writable objects and the manifest
// string the release records.
//
// An empty document is dropped rather than reported: a chart that wraps a whole
// file in `{{- if .Values.enabled }}` renders an empty file when it is off, and
// that is the template working rather than failing.
func objectsOf(manifests []releaseutil.Manifest, namespace string) ([]Object, string, error) {
	objects := make([]Object, 0, len(manifests))
	var manifest strings.Builder

	for _, item := range manifests {
		if strings.TrimSpace(stripComments(item.Content)) == "" {
			continue
		}
		object, err := objectOf(item.Content, item.Name, namespace)
		if err != nil {
			return nil, "", err
		}

		objects = append(objects, object)
		manifest.WriteString("---\n# Source: " + item.Name + "\n")
		manifest.WriteString(strings.TrimPrefix(item.Content, "\n"))
		if !strings.HasSuffix(item.Content, "\n") {
			manifest.WriteString("\n")
		}
	}
	return objects, manifest.String(), nil
}

// crdsOf reads the chart's `crds/` directory. Helm applies these first and does
// not record them on the release, and both halves of that are deliberate: a CRD
// is shared state that outlives the release which happened to install it, so
// recording it would make a later uninstall delete a kind other releases are
// using.
func crdsOf(loaded *chart.Chart) ([]Object, error) {
	crds := loaded.CRDObjects()
	objects := make([]Object, 0, len(crds))
	for _, crd := range crds {
		// One file may hold several CRDs.
		for _, document := range releaseutil.SplitManifests(string(crd.File.Data)) {
			if strings.TrimSpace(stripComments(document)) == "" {
				continue
			}
			object, err := objectOf(document, crd.Filename, "")
			if err != nil {
				return nil, err
			}
			object.CRD = true
			objects = append(objects, object)
		}
	}
	return objects, nil
}

// hookObjects splits hooks into the two passes this supports and drops the rest.
//
// **What is deliberately not implemented is the honest part.** Helm runs a hook
// and *waits* for it — a pre-install Job has to reach completion before the
// release is applied — and waiting means watching a Job to a terminal state,
// with a timeout, with `hook-delete-policy` deciding whether the evidence
// survives. That is a long-running operation, and the surface here is one HTTP
// call that answers with a report. So hooks are **applied in weight order and
// not waited on**, and the events this does not implement at all — `test`,
// every `-delete` and `-rollback` phase — are dropped rather than run at the
// wrong time. Both facts are stated on the response, not discovered afterwards.
func hookObjects(hooks []*release.Hook, meta ReleaseMeta) (pre, post []Object) {
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		object, err := objectOf(hook.Manifest, hook.Path, meta.Namespace)
		if err != nil {
			// A hook that does not parse is skipped rather than failing the
			// install: the sorter already accepted it as an object, and the
			// release proper is what the operator asked for.
			continue
		}
		object.Hook = true
		object.Weight = hook.Weight
		object.Events = hookEvents(hook)

		switch {
		case hasHookEvent(hook, release.HookPreInstall, release.HookPreUpgrade):
			pre = append(pre, object)
		case hasHookEvent(hook, release.HookPostInstall, release.HookPostUpgrade):
			post = append(post, object)
		}
	}

	sort.SliceStable(pre, func(i, j int) bool { return pre[i].Weight < pre[j].Weight })
	sort.SliceStable(post, func(i, j int) bool { return post[i].Weight < post[j].Weight })
	return pre, post
}

// hasHookEvent reports whether a hook runs at any of the given phases, narrowed
// by whether this is an install or an upgrade at the caller.
func hasHookEvent(hook *release.Hook, wanted ...release.HookEvent) bool {
	for _, event := range hook.Events {
		for _, want := range wanted {
			if event == want {
				return true
			}
		}
	}
	return false
}

// hookEvents names a hook's phases for the report, so an operator reading it
// sees why an object was written outside the release proper.
func hookEvents(hook *release.Hook) []string {
	events := make([]string, 0, len(hook.Events))
	for _, event := range hook.Events {
		events = append(events, string(event))
	}
	return events
}

// objectOf parses one rendered document into an object.
//
// The namespace rule is the load-bearing one. A rendered object that names no
// namespace takes the release's — which is what `kubectl apply -n` does and what
// Helm does — but an object that names a *different* one keeps it, because a
// chart that deliberately installs into `kube-system` is a real chart. Which of
// those two happened is checked against the caller's grant later, by the same
// namespace enforcement every other write goes through; it is not decided here.
func objectOf(document, source, namespace string) (Object, error) {
	encoded, err := yaml.YAMLToJSON([]byte(document))
	if err != nil {
		return Object{}, fmt.Errorf("%s rendered something that is not YAML: %w", source, err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(encoded, &parsed); err != nil || parsed == nil {
		return Object{}, fmt.Errorf("%s rendered something that is not a Kubernetes object", source)
	}

	object := Object{Source: source, YAML: document}
	object.APIVersion, _ = parsed["apiVersion"].(string)
	object.Kind, _ = parsed["kind"].(string)
	if object.APIVersion == "" || object.Kind == "" {
		return Object{}, fmt.Errorf("%s rendered an object with no apiVersion or kind", source)
	}

	metadata, _ := parsed["metadata"].(map[string]any)
	if metadata == nil {
		return Object{}, fmt.Errorf("%s rendered a %s with no metadata", source, object.Kind)
	}
	object.Name, _ = metadata["name"].(string)
	if strings.TrimSpace(object.Name) == "" {
		// `generateName` is legal for a Job and nothing else a chart installs,
		// and an object with no name cannot be diffed against a later revision,
		// which is what an upgrade needs from every recorded object.
		return Object{}, fmt.Errorf("%s rendered a %s with no name", source, object.Kind)
	}
	object.Namespace, _ = metadata["namespace"].(string)
	if object.Namespace == "" && namespace != "" {
		object.Namespace = namespace
		metadata["namespace"] = namespace
		if encoded, err = json.Marshal(parsed); err != nil {
			return Object{}, fmt.Errorf("%s could not be encoded", source)
		}
	}

	object.Document = encoded
	return object, nil
}

// stripComments removes comment-only lines before deciding a document is empty.
// `releaseutil` already leaves a `# Source:` header on everything it splits, so
// a file that rendered to nothing is not literally empty.
func stripComments(document string) string {
	var out strings.Builder
	for _, line := range strings.Split(document, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String()
}

// mergedValuesOf pulls `.Values` back out of the render context, which is the
// coalesced set the chart saw rather than the document the operator submitted.
func mergedValuesOf(renderValues chartutil.Values) map[string]any {
	values, _ := renderValues["Values"].(chartutil.Values)
	return map[string]any(values)
}

// KubeVersionOf turns what `/version` reports into the `.Capabilities.KubeVersion`
// a chart branches on. A cluster that reports a version with a `+` in it — every
// managed provider does — is normal, and `semver` accepts it as build metadata.
func KubeVersionOf(major, minor, gitVersion string) (*chartutil.KubeVersion, error) {
	version := strings.TrimSpace(gitVersion)
	if version == "" {
		if major == "" || minor == "" {
			return nil, fmt.Errorf("the cluster did not report its version")
		}
		version = "v" + strings.TrimSpace(major) + "." + strings.TrimSpace(minor) + ".0"
	}
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}

	// Minor is reported as "31+" by more than one provider. A chart comparing
	// against it with semver would refuse to parse the whole version.
	return &chartutil.KubeVersion{
		Version: version,
		Major:   strings.TrimRight(strings.TrimSpace(major), "+"),
		Minor:   strings.TrimRight(strings.TrimSpace(minor), "+"),
	}, nil
}

// weightAnnotation is Helm's own key, read back for the report.
const weightAnnotation = "helm.sh/hook-weight"

// WeightOf reads a hook weight off an object's annotations, for a caller that
// has an object rather than a parsed hook.
func WeightOf(annotations map[string]string) int {
	weight, err := strconv.Atoi(strings.TrimSpace(annotations[weightAnnotation]))
	if err != nil {
		return 0
	}
	return weight
}

/* ------------------------------------------------------------ ownership --- */

// Helm's ownership metadata. Every object Helm applies carries these, and it is
// how `helm` decides whether an object that already exists is one it may adopt:
// a resource carrying another release's name is refused with "invalid ownership
// metadata" rather than overwritten.
const (
	managedByLabel           = "app.kubernetes.io/managed-by"
	managedByHelm            = "Helm"
	releaseNameAnnotation    = "meta.helm.sh/release-name"
	releaseNamespaceAnnotate = "meta.helm.sh/release-namespace"
)

// WithOwnership stamps an object with the metadata Helm stamps on it.
//
// This is not decoration, and leaving it off is the kind of omission that only
// shows up weeks later. Two things depend on it. `helm` reads these to decide
// whether an object it is about to write is *its* — without them a later `helm
// install` of the same chart refuses to adopt what this console created. And an
// object that Helm has since touched carries them while a manifest recorded
// without them does not, which makes every subsequent three-way merge diff on
// annotations that nothing in the chart ever wrote.
//
// It is applied at **write** time and deliberately not recorded in the
// manifest, which is exactly what Helm does: `setMetadataVisitor` runs over the
// target resources on the way to the API server, and the recorded manifest is
// the render. Recording them would make them look like something the chart
// declared, and a chart that later declared its own `managed-by` label could
// then never change it.
//
// A value the chart set itself is **overwritten**, which is Helm's own
// behaviour — it passes `force: true` on the argument that these are the
// resources the chart is rendering right now — and matching it is the point:
// leaving a chart's own `managed-by` alone would mean `helm upgrade` flipping
// the label on its next run and this console flipping it back, so every upgrade
// would diff on a field neither of them cares about.
func WithOwnership(document []byte, release, namespace string) []byte {
	var parsed map[string]any
	if err := json.Unmarshal(document, &parsed); err != nil {
		// Unreadable here means the write is about to fail with the cluster's
		// own message, which is a better one than anything this could produce.
		return document
	}

	metadata, ok := parsed["metadata"].(map[string]any)
	if !ok {
		return document
	}
	setStringKey(metadata, "labels", managedByLabel, managedByHelm)
	setStringKey(metadata, "annotations", releaseNameAnnotation, release)
	setStringKey(metadata, "annotations", releaseNamespaceAnnotate, namespace)

	stamped, err := json.Marshal(parsed)
	if err != nil {
		return document
	}
	return stamped
}

// setStringKey writes one entry into an object's labels or annotations, creating
// the map if the object declared none.
func setStringKey(metadata map[string]any, field, key, value string) {
	entries, ok := metadata[field].(map[string]any)
	if !ok {
		entries = map[string]any{}
		metadata[field] = entries
	}
	entries[key] = value
}
