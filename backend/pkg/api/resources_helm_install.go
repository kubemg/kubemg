package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	helmchart "helm.sh/helm/v3/pkg/chart"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/helm"
)

/*
 * Installing and upgrading, which is where the caveat finally goes.
 *
 * Every write this file replaces travelled with the same sentence: it records
 * what Helm will start from and re-applies nothing. That was true because
 * KubeMG had no chart. It now has two ways to get one, and the second is the
 * more interesting:
 *
 *   - from a **repository**, for an install or an upgrade to a new version;
 *   - from the **release itself**, for everything else. Helm stores the whole
 *     chart on the release — templates, values, files, subcharts — which is how
 *     `helm upgrade --reuse-values` works. So a values edit on a release
 *     somebody installed from a laptop two years ago can be rendered and
 *     applied here, with no repository configured and nothing reachable.
 *
 * That second path is what actually removes `helmValuesWarning`, and it is worth
 * being precise about when it does not: a release whose Secret was written by
 * something that stripped the chart cannot be re-rendered, and that case keeps
 * the old behaviour and the old warning rather than pretending. The caveat is
 * gone for every release Helm itself wrote.
 */

// maxHelmValuesDocument bounds a submitted install. It is larger than the values
// bound on an edit because an install carries the values, a chart reference and
// a release name in one body, and smaller than anything that would let a values
// document become a payload.
const maxHelmValuesDocument = 1 << 20

// installRequest is what the install form submits.
type installRequest struct {
	Repository string `json:"repository"`
	Chart      string `json:"chart"`
	Version    string `json:"version"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	YAML       string `json:"yaml"`
}

// upgradeRequest is an install's second act. Every chart field is optional:
// omitting them all is "re-render what is installed with these values", which is
// what the values editor asks for.
type upgradeRequest struct {
	Repository string `json:"repository"`
	Chart      string `json:"chart"`
	Version    string `json:"version"`
	YAML       string `json:"yaml"`
	// ReuseValues renders with the values the current revision holds rather than
	// with a submitted document. It is what a version bump wants, and it is
	// explicit rather than inferred from an empty `yaml` — an empty document is
	// a real values set, and guessing between the two is how an upgrade silently
	// resets a release's configuration.
	ReuseValues bool `json:"reuse_values"`
}

// installHelmRelease renders a chart and writes it into the cluster.
func (s *server) installHelmRelease(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxHelmValuesDocument)
	var request installRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the install could not be read"})
		return
	}

	name := strings.TrimSpace(request.Name)
	if !helmReleaseName.MatchString(name) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "a release name is lowercase letters, digits, dashes and dots",
		})
		return
	}
	namespace, ok := s.scopedNamespace(c, grant, request.Namespace)
	if !ok {
		return
	}
	values, ok := helmValuesDocumentFrom(c, request.YAML)
	if !ok {
		return
	}

	loaded, version, ok := s.chartFromRepository(c, request.Repository, request.Chart, request.Version)
	if !ok {
		return
	}

	// A release that already exists is an upgrade, and answering an install with
	// one would be this console deciding on the operator's behalf that they
	// meant something other than what they pressed.
	if s.helmReleaseExists(c, user, cluster, grant, namespace, name) {
		c.JSON(http.StatusConflict, gin.H{
			"error": fmt.Sprintf("a release named %q already exists in %s — upgrade it instead",
				name, namespace),
		})
		return
	}

	discovery, ok := s.discoverCluster(c, user, cluster, grant)
	if !ok {
		return
	}

	rendered, err := helm.Render(loaded, values, helm.ReleaseMeta{
		Name: name, Namespace: namespace, Revision: 1, IsInstall: true,
	}, discovery.capabilities)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	plan, ok := s.planApply(c, discovery, grant, rendered, nil, name, namespace)
	if !ok {
		return
	}
	result := s.apply(c, user, cluster, grant, plan, nil)

	status := helm.StatusDeployed
	description := "Install complete"
	if !result.ok() {
		status = helm.StatusFailed
		description = "Install failed: " + result.Failed.Kind + "/" + result.Failed.Name +
			" — " + result.Failed.Message
	}

	release := helm.NewRelease(name, namespace, 1, loaded, values, rendered, status, description)
	if !s.writeHelmRevision(c, user, cluster, grant, namespace, name, nil, release, status) {
		return
	}

	c.JSON(http.StatusOK, helmWriteResponse(c, release, result, version.Version, rendered))
}

// upgradeHelmRelease re-renders an installed release and applies the difference.
func (s *server) upgradeHelmRelease(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	namespace, name, ok := s.helmReleaseTarget(c, grant)
	if !ok {
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxHelmValuesDocument)
	var request upgradeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the upgrade could not be read"})
		return
	}

	revisions, ok := s.helmRevisions(c, user, cluster, grant, namespace, name)
	if !ok {
		return
	}
	current := revisions[0]
	previous, ok := parsedRelease(c, current)
	if !ok {
		return
	}

	// The chart comes from the repository when one is named, and from the
	// release itself when one is not. Both are real requests: the first is a
	// version bump, the second is "render what is installed with these values".
	var loaded *helmchart.Chart
	chartVersion := ""
	if strings.TrimSpace(request.Repository) != "" {
		fetched, version, fetchOK := s.chartFromRepository(c, request.Repository, request.Chart, request.Version)
		if !fetchOK {
			return
		}
		loaded, chartVersion = fetched, version.Version
	} else {
		stored, err := previous.LoadedChart()
		if err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		loaded = stored
		if stored.Metadata != nil {
			chartVersion = stored.Metadata.Version
		}
	}

	values := previous.Config
	if !request.ReuseValues {
		submitted, valuesOK := helmValuesDocumentFrom(c, request.YAML)
		if !valuesOK {
			return
		}
		values = submitted
	}

	s.renderAndApply(c, user, cluster, grant, namespace, name, current, previous,
		loaded, values, chartVersion, helmUpgradeDescription)
}

// renderAndApply is the shared body of every write that has a chart: an upgrade,
// a values edit that can render, and nothing else. It exists so those two cannot
// drift apart in what they write to the cluster or record on the release.
func (s *server) renderAndApply(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, namespace, name string, current helmStoredRevision,
	previous *helm.Release, loaded *helmchart.Chart, values map[string]any,
	chartVersion, description string,
) {
	discovery, ok := s.discoverCluster(c, user, cluster, grant)
	if !ok {
		return
	}

	rendered, err := helm.Render(loaded, values, helm.ReleaseMeta{
		Name: name, Namespace: namespace, Revision: current.revision + 1, IsUpgrade: true,
	}, discovery.capabilities)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// What the *previous* revision recorded, which is the third document of the
	// three-way merge and the only source of what has to be removed.
	installed, err := helm.ManifestObjects(previous.Manifest, namespace)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "the previous revision's manifest could not be read",
		})
		return
	}

	plan, ok := s.planApply(c, discovery, grant, rendered, installed, name, namespace)
	if !ok {
		return
	}
	result := s.apply(c, user, cluster, grant, plan, originalOf(installed))

	status := helm.StatusDeployed
	if !result.ok() {
		status = helm.StatusFailed
		description = "Upgrade failed: " + result.Failed.Kind + "/" + result.Failed.Name +
			" — " + result.Failed.Message
	}

	next := helm.NextRelease(previous, loaded, values, rendered, status, description)
	if !s.writeHelmRevision(c, user, cluster, grant, namespace, name, &current, next, status) {
		return
	}
	c.JSON(http.StatusOK, helmWriteResponse(c, next, result, chartVersion, rendered))
}

/* ----------------------------------------------------------- the chart --- */

// chartFromRepository resolves a chart reference to a downloaded chart.
//
// The version is resolved against the **stored catalogue** rather than against
// the repository, which is what makes this refusable without a network call and
// what keeps an install from being steered at an arbitrary URL: the archive it
// fetches is one the last sync recorded, at the URL that sync recorded for it.
func (s *server) chartFromRepository(c *gin.Context, repository, chart, version string) (
	*helmchart.Chart, helm.ChartVersion, bool,
) {
	ctx := c.Request.Context()

	repository = strings.TrimSpace(repository)
	stored, err := s.store.HelmRepository(ctx, repository)
	switch {
	case errors.Is(err, db.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "no chart repository named " + repository})
		return nil, helm.ChartVersion{}, false
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the repository could not be read"})
		return nil, helm.ChartVersion{}, false
	}

	row, err := s.store.HelmChart(ctx, stored.ID, strings.TrimSpace(chart))
	switch {
	case errors.Is(err, db.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{
			"error": fmt.Sprintf("%s does not publish a chart named %q — "+
				"if it was added recently, sync the repository", repository, chart),
		})
		return nil, helm.ChartVersion{}, false
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the chart could not be read"})
		return nil, helm.ChartVersion{}, false
	}

	catalogue := row.Chart()
	wanted := strings.TrimSpace(version)
	if wanted == "" {
		wanted = catalogue.LatestVersion()
	}
	published, ok := catalogue.Version(wanted)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{
			"error": fmt.Sprintf("%s %s is not one of the versions KubeMG holds for this chart",
				chart, wanted),
		})
		return nil, helm.ChartVersion{}, false
	}

	loaded, err := helm.FetchChart(ctx, s.helmClient(), stored.Repository(), published)
	if err != nil {
		// The repository was reachable when it was synced and is not now, or it
		// is serving something other than what it indexed. Both are the
		// repository's problem and both are reported as one.
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return nil, helm.ChartVersion{}, false
	}
	if err := helm.Installable(loaded); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, helm.ChartVersion{}, false
	}
	return loaded, published, true
}

/* ------------------------------------------------------- writing it down --- */

// helmReleaseExists reports whether a release of that name is already stored. It
// is a read that tolerates a refusal: a caller the cluster will not let list
// Secrets is about to be refused by the write anyway, in the cluster's own
// words, which is a better message than one this could produce.
func (s *server) helmReleaseExists(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, namespace, name string,
) bool {
	resp, err := s.proxy.Call(c.Request.Context(), user, cluster, grant,
		http.MethodGet, helmSecretsPath(namespace, name), nil, nil)
	if err != nil || resp.Status < 200 || resp.Status >= 300 {
		return false
	}

	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		return false
	}
	return len(helmStoredRevisionsOf(list.Items)) > 0
}

// writeHelmRevision stores a release as the next revision's Secret, and marks
// the one it replaces superseded.
//
// A **failed** release is still written, and that is deliberate. Helm records a
// failed install too, and it has to: the objects that *were* created are real,
// and a release nobody recorded is a set of orphans with no name. The recorded
// status is what tells the next `helm upgrade` — and this console — that the
// release is not in the state its manifest claims.
func (s *server) writeHelmRevision(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, namespace, name string, current *helmStoredRevision,
	next *helm.Release, status helm.Status,
) bool {
	document, err := next.Encode()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return false
	}
	payload, err := encodeHelmPayload(document)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return false
	}

	labels := map[string]any{}
	for key, value := range helm.Labels(name, next.Version, status) {
		labels[key] = value
	}
	secret := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"type":       helmSecretType,
		"metadata": map[string]any{
			"name":      helm.SecretName(name, next.Version),
			"namespace": namespace,
			"labels":    labels,
		},
		"data": map[string]any{"release": payload},
	}

	body, err := json.Marshal(secret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the release could not be encoded"})
		return false
	}

	path := fmt.Sprintf("/api/v1/namespaces/%s/secrets", url.PathEscape(namespace))
	resp, ok := s.callResourceWith(c, user, cluster, grant,
		http.MethodPost, path, body, "the release could not be written to the cluster")
	if !ok {
		return false
	}
	if resp.Status < 200 || resp.Status >= 300 {
		c.JSON(resp.Status, gin.H{"error": kubeErrorMessage(resp.Body, resp.Status)})
		return false
	}

	if current != nil {
		// Only once the new revision exists. If this fails the release is still
		// correct — Helm reads the highest revision — but `helm history` shows
		// two deployed rows, which the response says rather than swallows.
		if err := s.supersedeHelmSecret(c, user, cluster, grant,
			namespace, current.secret, current.release); err != nil {
			c.Set(helmSupersedeWarning, err.Error())
		}
	}
	return true
}

// helmSupersedeWarning is the context key the supersede failure travels on, so
// the response builder can report it without every caller threading it through.
const helmSupersedeWarning = "helm_supersede_warning"

// parsedRelease decodes a stored revision into the typed release the render
// path works with.
func parsedRelease(c *gin.Context, stored helmStoredRevision) (*helm.Release, bool) {
	document, err := json.Marshal(stored.release)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "this release could not be read"})
		return nil, false
	}
	release, err := helm.ParseRelease(document)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return nil, false
	}
	return release, true
}

/* ------------------------------------------------------------- responses --- */

// helmUpgradeDescription is what `helm history` shows for a revision written
// from here. It is Helm's own wording with the provenance added.
const helmUpgradeDescription = "Upgrade complete, through KubeMG"

// helmHookNotice is the one limit an install here still has, and it is stated on
// every response that rendered a hook rather than left to be discovered.
const helmHookNotice = "This chart declares hooks. They were written in weight order and " +
	"not waited on: unlike helm, KubeMG does not hold the request open until a pre-install Job " +
	"completes, and it does not honour hook-delete-policy. Test hooks were not run."

// helmWriteResponse builds what an install or an upgrade answers with.
func helmWriteResponse(c *gin.Context, release *helm.Release, result applyResult,
	chartVersion string, rendered *helm.Rendered,
) gin.H {
	view := helmReleaseView{
		Name:        release.Name,
		Namespace:   release.Namespace,
		Revision:    release.Version,
		Status:      string(release.Info.Status),
		Description: release.Info.Description,
	}
	if release.Chart != nil && release.Chart.Metadata != nil {
		view.ChartName = release.Chart.Metadata.Name
		view.ChartVersion = release.Chart.Metadata.Version
		view.AppVersion = release.Chart.Metadata.AppVersion
	}
	if chartVersion != "" {
		view.ChartVersion = chartVersion
	}

	body := gin.H{
		"release": view,
		"objects": result.Reports,
		"yaml":    helmValuesDocument(release.Config),
		"applied": result.ok(),
	}
	if rendered.Notes != "" {
		body["notes"] = rendered.Notes
	}
	if len(rendered.PreInstall) > 0 || len(rendered.PostInstall) > 0 {
		body["hook_notice"] = helmHookNotice
	}
	if !result.ok() {
		body["error"] = fmt.Sprintf("%s/%s was refused: %s",
			result.Failed.Kind, result.Failed.Name, result.Failed.Message)
	}
	// The supersede is the one write here that can fail without making the
	// release wrong — Helm reads the highest revision regardless — but it leaves
	// `helm history` showing two deployed rows, which is worth saying rather
	// than swallowing.
	if warning, ok := c.Get(helmSupersedeWarning); ok {
		body["warning"] = "The previous revision could not be marked superseded: " + fmt.Sprint(warning)
	}
	return body
}

// helmValuesDocumentFrom parses a submitted values document. It is
// `helmValuesFrom`'s body without the request read, so an install — which
// carries its values inside a larger payload — validates them by exactly the
// same rules an edit does.
func helmValuesDocumentFrom(c *gin.Context, document string) (map[string]any, bool) {
	values, err := helmValuesOf(document)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, false
	}
	return values, true
}
