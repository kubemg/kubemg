package helm

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/chart"
)

/*
 * The release object, and why it is built here rather than in the API layer.
 *
 * The one rule that makes an install through this console safe is that the
 * release KubeMG writes has to be a release the `helm` CLI reads back. An
 * operator will run `helm list`, `helm history`, `helm get values` and
 * eventually `helm upgrade` against it, and every one of those reads the same
 * Secret. A release that is *nearly* right is worse than no install button at
 * all: it is a trap that springs weeks later, on somebody who did not use this
 * console and has no reason to suspect it.
 *
 * So the field names here are Helm's own JSON names, not this codebase's, and
 * the shape is `release.Release` — with the chart carried the way Helm carries
 * it, files included, because `helm upgrade --reuse-values` and `helm rollback`
 * both read the chart back out of the stored release rather than re-fetching it.
 */

// Status is the value both the Secret's `status` label and the release's
// `info.status` carry. They are one field written twice, and Helm reads a
// different one in `helm list` (the label) than in `helm history` (the payload),
// so writing one without the other is how the two disagree.
type Status string

const (
	StatusDeployed   Status = "deployed"
	StatusFailed     Status = "failed"
	StatusSuperseded Status = "superseded"
)

// Release is a Helm release as it is stored. It is a struct rather than the
// library's `release.Release` for one reason: the stored form has to round-trip
// through JSON with Helm's own key names, and the library's type carries
// pointers and interfaces whose zero values marshal into keys Helm's reader
// treats as set.
type Release struct {
	Name      string         `json:"name"`
	Info      Info           `json:"info"`
	Chart     *StoredChart   `json:"chart,omitempty"`
	Config    map[string]any `json:"config,omitempty"`
	Manifest  string         `json:"manifest,omitempty"`
	Hooks     []StoredHook   `json:"hooks,omitempty"`
	Version   int            `json:"version"`
	Namespace string         `json:"namespace"`
}

// Info is the release's own account of itself, and it is what `helm history`
// prints. `description` is the row an operator reads to work out who did this,
// which is why every write from here names KubeMG in it.
type Info struct {
	FirstDeployed string `json:"first_deployed,omitempty"`
	LastDeployed  string `json:"last_deployed,omitempty"`
	Deleted       string `json:"deleted,omitempty"`
	Description   string `json:"description,omitempty"`
	Status        Status `json:"status,omitempty"`
	Notes         string `json:"notes,omitempty"`
}

// StoredChart is the chart as the release carries it: its metadata, its default
// values, and its files. Helm stores the whole chart, and it has to — `helm
// rollback` re-renders from it, and `helm upgrade --reuse-values` reads the
// previous chart's defaults out of it. Dropping the files to save a few
// kilobytes in a Secret would break both, in a way that only shows up when
// somebody reaches for them.
type StoredChart struct {
	Metadata  *chart.Metadata `json:"metadata,omitempty"`
	Templates []*chart.File   `json:"templates,omitempty"`
	Values    map[string]any  `json:"values,omitempty"`
	Files     []*chart.File   `json:"files,omitempty"`
	Schema    []byte          `json:"schema,omitempty"`
	// Dependencies is the resolved subchart set, after conditions and tags.
	// Helm names this key `dependencies`.
	Dependencies []*StoredChart `json:"dependencies,omitempty"`
	Lock         *chart.Lock    `json:"lock,omitempty"`
}

// StoredHook is a hook as the release records it, so `helm get hooks` answers.
type StoredHook struct {
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`
	Path           string   `json:"path"`
	Manifest       string   `json:"manifest"`
	Events         []string `json:"events,omitempty"`
	Weight         int      `json:"weight,omitempty"`
	DeletePolicies []string `json:"delete_policies,omitempty"`
}

// SecretName is how Helm 3 names the Secret one revision is stored in. It is a
// format rather than a lookup, and every read in this product resolves a
// revision against what the cluster returned instead of building one of these —
// but a *write* has to produce the name Helm will look for.
func SecretName(release string, revision int) string {
	return "sh.helm.release.v1." + release + ".v" + strconv.Itoa(revision)
}

// Labels are the four labels Helm queries by. `owner=helm` is what makes a
// Secret a release at all, `name` is what groups revisions, `status` is what
// `helm list` filters on and `version` is the revision.
//
// A fifth is written that Helm does not require and does not mind: nothing.
// The temptation to label the Secret with the identity that wrote it is a real
// one and is refused here — a label is data every reader of the namespace can
// see, the audit trail already records who wrote it, and Helm's own tooling
// round-trips these Secrets in ways that would silently drop an extra key.
func Labels(release string, revision int, status Status) map[string]string {
	return map[string]string{
		"owner":   "helm",
		"name":    release,
		"status":  string(status),
		"version": strconv.Itoa(revision),
	}
}

// NewRelease builds the release a fresh install records.
func NewRelease(name, namespace string, revision int, loaded *chart.Chart,
	config map[string]any, rendered *Rendered, status Status, description string,
) *Release {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return &Release{
		Name:      name,
		Namespace: namespace,
		Version:   revision,
		Config:    valuesOrEmpty(config),
		Manifest:  rendered.Manifest,
		Chart:     storedChartOf(loaded),
		Hooks:     storedHooksOf(rendered),
		Info: Info{
			FirstDeployed: now,
			LastDeployed:  now,
			Status:        status,
			Description:   description,
			Notes:         rendered.Notes,
		},
	}
}

// NextRelease builds the release an upgrade records, carrying forward the one
// thing an upgrade must not restate: when the release was *first* deployed.
// `helm history` shows it, and an upgrade that resets it makes a two-year-old
// release look like it was installed this afternoon.
func NextRelease(previous *Release, loaded *chart.Chart, config map[string]any,
	rendered *Rendered, status Status, description string,
) *Release {
	next := NewRelease(previous.Name, previous.Namespace, previous.Version+1,
		loaded, config, rendered, status, description)
	if previous.Info.FirstDeployed != "" {
		next.Info.FirstDeployed = previous.Info.FirstDeployed
	}
	return next
}

// storedChartOf converts a loaded chart into the stored form, recursively.
func storedChartOf(loaded *chart.Chart) *StoredChart {
	if loaded == nil {
		return nil
	}
	stored := &StoredChart{
		Metadata:  loaded.Metadata,
		Templates: loaded.Templates,
		Values:    loaded.Values,
		Files:     loaded.Files,
		Schema:    loaded.Schema,
		Lock:      loaded.Lock,
	}
	for _, dependency := range loaded.Dependencies() {
		stored.Dependencies = append(stored.Dependencies, storedChartOf(dependency))
	}
	return stored
}

// storedHooksOf records the hooks the render produced, whether or not they were
// applied. `helm get hooks` shows what the chart declares, not what ran.
func storedHooksOf(rendered *Rendered) []StoredHook {
	hooks := make([]StoredHook, 0, len(rendered.PreInstall)+len(rendered.PostInstall))
	for _, object := range append(append([]Object{}, rendered.PreInstall...), rendered.PostInstall...) {
		hooks = append(hooks, StoredHook{
			Name:     object.Name,
			Kind:     object.Kind,
			Path:     object.Source,
			Manifest: object.YAML,
			Events:   object.Events,
			Weight:   object.Weight,
		})
	}
	if len(hooks) == 0 {
		return nil
	}
	return hooks
}

// valuesOrEmpty keeps `config` a mapping. A release installed with no values is
// a real state, and Helm writes `{}` for it rather than omitting the key.
func valuesOrEmpty(values map[string]any) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	return values
}

// ManifestObjects splits a recorded manifest back into the objects it holds.
//
// This is the read an upgrade and a rollback both need: the previous revision's
// manifest is the `original` of the three-way merge, and the set of objects a
// removed template used to produce. It is deliberately the *recorded* manifest
// rather than a re-render of the stored chart — re-rendering would produce
// today's answer to `.Capabilities` and `lookup`, and the question being asked
// is what was actually written last time.
func ManifestObjects(manifest, namespace string) ([]Object, error) {
	documents := splitManifest(manifest)
	objects := make([]Object, 0, len(documents))
	for _, document := range documents {
		if strings.TrimSpace(stripComments(document.body)) == "" {
			continue
		}
		object, err := objectOf(document.body, document.source, namespace)
		if err != nil {
			// A recorded manifest that no longer parses is somebody else's
			// release, or a Helm old enough to have written something this
			// cannot read. Skipping the document loses the ability to remove
			// that one object; failing the upgrade would lose the release.
			continue
		}
		objects = append(objects, object)
	}
	return objects, nil
}

// manifestDocument is one `---`-separated document of a recorded manifest, with
// the `# Source:` header Helm writes above it read back off.
type manifestDocument struct {
	source string
	body   string
}

// splitManifest cuts a recorded manifest into its documents, in the order they
// were written — which is the order they were applied, and therefore the
// reverse of the order they have to be deleted in.
//
// It splits on a `---` that is alone on its line rather than on the string
// anywhere, because `---` inside a block scalar is somebody's config file and
// not a document break; that is the same rule the YAML parser applies and the
// one a naive `strings.Split` gets wrong.
func splitManifest(manifest string) []manifestDocument {
	documents := make([]manifestDocument, 0, 8)
	var body strings.Builder
	source := ""

	flush := func() {
		if strings.TrimSpace(body.String()) != "" {
			documents = append(documents, manifestDocument{source: source, body: body.String()})
		}
		body.Reset()
		source = ""
	}

	for _, line := range strings.Split(manifest, "\n") {
		trimmed := strings.TrimRight(line, " \t\r")
		if trimmed == "---" || strings.HasPrefix(trimmed, "--- ") {
			flush()
			continue
		}
		if after, ok := strings.CutPrefix(strings.TrimSpace(trimmed), "# Source:"); ok && source == "" {
			source = strings.TrimSpace(after)
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	flush()

	for index := range documents {
		if documents[index].source == "" {
			documents[index].source = "the recorded manifest"
		}
	}
	return documents
}

/* ------------------------------------------------------ reading one back --- */

// ParseRelease decodes a stored release.
//
// This is what makes a values edit able to *render* for a release nobody
// installed through this console: Helm stores the whole chart on the release, so
// the templates are already here and no repository has to be reachable, or even
// configured, to re-render a release that is already installed. `helm upgrade
// --reuse-values` reads the chart back out of exactly this field for the same
// reason.
func ParseRelease(document []byte) (*Release, error) {
	var release Release
	if err := json.Unmarshal(document, &release); err != nil {
		return nil, fmt.Errorf("this release could not be read")
	}
	if release.Name == "" {
		return nil, fmt.Errorf("this release names nothing")
	}
	return &release, nil
}

// LoadedChart rebuilds the chart a release carries.
//
// Dependencies are re-attached rather than unmarshalled, because `chart.Chart`
// keeps them in an unexported field with an accessor: a subchart tree that is
// merely present in the JSON but never attached renders as though every subchart
// were disabled, which is the kind of failure that produces a valid-looking
// release with half the objects missing.
func (r *Release) LoadedChart() (*chart.Chart, error) {
	if r.Chart == nil || r.Chart.Metadata == nil {
		return nil, fmt.Errorf("this release does not carry its chart, so it cannot be re-rendered — " +
			"it was written by something that stripped the chart from the release")
	}
	return r.Chart.chart(), nil
}

// chart converts one stored chart, recursively, attaching subcharts as Helm's
// own loader would.
func (c *StoredChart) chart() *chart.Chart {
	loaded := &chart.Chart{
		Metadata:  c.Metadata,
		Lock:      c.Lock,
		Templates: c.Templates,
		Values:    c.Values,
		Schema:    c.Schema,
		Files:     c.Files,
	}
	for _, dependency := range c.Dependencies {
		loaded.AddDependency(dependency.chart())
	}
	return loaded
}

// Encode renders a release back to the JSON that goes in the Secret.
func (r *Release) Encode() ([]byte, error) {
	document, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("this release could not be encoded")
	}
	return document, nil
}

// cloneChart copies a chart's tree so a render cannot change what a caller will
// store. It is a copy of the *structure* rather than of the files: templates and
// files are immutable once loaded, and duplicating a chart's bytes to render it
// would double the memory cost of every install for nothing.
func cloneChart(loaded *chart.Chart) *chart.Chart {
	if loaded == nil {
		return nil
	}
	return storedChartOf(loaded).chart()
}
