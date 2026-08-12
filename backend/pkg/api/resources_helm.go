package api

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"sigs.k8s.io/yaml"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Helm releases, read from where Helm actually keeps them.
 *
 * Helm 3 has no API of its own and no controller in the cluster: a release is a
 * Secret in the release's namespace, labelled `owner=helm`, holding the whole
 * release object as base64(base64(gzip(JSON))). So this is not a new kind of
 * access — it is the ordinary secrets list, read through the same impersonated
 * tunnel, with the payload decoded server-side because a browser has no business
 * gunzipping a release to render a table.
 *
 * Two consequences worth stating plainly:
 *
 *   - The cluster's RBAC decides. Reading a release means reading a Secret, and
 *     the built-in `view` role deliberately excludes Secrets — a `view` grant is
 *     refused here in the cluster's own words, which is the correct answer.
 *   - Nothing but the release's *values* ever leaves. The release object also
 *     carries the rendered manifest, which for many charts contains generated
 *     passwords; it is never put in a response. The list carries chart metadata,
 *     and the values endpoint carries `config` — the values the operator
 *     supplied, which is exactly what `helm get values` shows.
 *
 * Writing values is deliberately narrow, and its limit is the important part:
 * appending a revision changes what Helm *believes* the values are, and nothing
 * else. It does not re-render the chart and it does not touch a single running
 * object — the next `helm upgrade` starts from the new values, and until then
 * the cluster runs what the previous revision rendered. Every write says so.
 *
 * Rollback is that same write with its values read out of an older revision, and
 * it is deliberately *less* than `helm rollback`. Helm's own rollback restores a
 * revision's values, its chart and its rendered manifest, and then applies that
 * manifest to the cluster; the applying is the whole point of it and is also the
 * one thing this cannot do, because KubeMG has no chart to render and applying a
 * stored manifest means a three-way merge and a deletion pass — Helm's hardest
 * code, reimplemented against objects it does not own. So what is restored here
 * is the target revision's **values, and only its values**: the chart metadata
 * and the manifest are carried forward from the current revision, because that
 * is what is actually running, and a recorded revision that disagrees with the
 * cluster would make the *next* `helm upgrade` diff against a state that was
 * never there. What this buys is real and worth having — the next upgrade
 * renders from the restored values and converges on the rolled-back state — and
 * what it does not buy is said on the response and on the surface before the
 * click, not discovered afterwards.
 */

const (
	// helmOwnerSelector is the label every Helm 3 storage Secret carries.
	helmOwnerSelector = "owner=helm"
	// helmSecretType is the Secret type Helm 3 writes its releases as.
	helmSecretType = "helm.sh/release.v1"
	// helmSecretPrefix is how Helm 3 names a release Secret: the prefix, the
	// release name, then `.v{revision}`.
	helmSecretPrefix = "sh.helm.release.v1."

	// maxHelmPayload bounds a decompressed release. A Secret is capped at a
	// megabyte by etcd, but gzip decompresses on the order of a thousand to one,
	// so the bound has to be here rather than on what came off the wire.
	maxHelmPayload = 32 << 20
	// maxHelmValues caps a submitted values document, on the same reasoning as a
	// submitted manifest: values nobody can hand-edit are not values.
	maxHelmValues = 1 << 20
)

// magicGzip is the gzip header Helm checks for. Releases written before Helm
// compressed them are stored as plain JSON, and Helm still reads those, so this
// reader does too.
var magicGzip = []byte{0x1f, 0x8b, 0x08}

// helmReleaseName matches a Helm release name. It is anchored because the name
// is used in a label selector and in a Secret name, and neither may be steered
// by the caller: a comma would add a selector term, a slash a path segment.
var helmReleaseName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// helmReleaseView is one release as a list shows it: what was installed, at what
// version, and how it ended up.
type helmReleaseView struct {
	Name         string    `json:"name"`
	Namespace    string    `json:"namespace"`
	ChartName    string    `json:"chart_name"`
	ChartVersion string    `json:"chart_version"`
	AppVersion   string    `json:"app_version"`
	Revision     int       `json:"revision"`
	Status       string    `json:"status"`
	Description  string    `json:"description,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (v helmReleaseView) sortKey() (string, string) { return v.Namespace, v.Name }

// helmValuesWarning is what an operator has to know before editing values here,
// and it travels with the response rather than living only in the UI: a client
// that skips it is still told.
const helmValuesWarning = "Saved as a new Helm revision. This records the values Helm will start " +
	"from — it does not re-render the chart, so nothing running changes until the next helm upgrade."

/* ------------------------------------------------------------- decoding --- */

// decodeHelmPayload turns a release Secret's `release` value into the release
// JSON. It is encoded twice: Kubernetes base64s every Secret value, and Helm's
// own value is itself base64 — of gzipped JSON since Helm 3, of plain JSON in
// releases old enough to predate that.
func decodeHelmPayload(encoded string) ([]byte, error) {
	outer, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("the release secret is not valid base64")
	}

	inner, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(outer)))
	if err != nil {
		// Not everything that writes a release Secret double-encodes; if the
		// outer bytes are not themselves base64 they are the payload.
		inner = outer
	}
	if !bytes.HasPrefix(inner, magicGzip) {
		return inner, nil
	}

	reader, err := gzip.NewReader(bytes.NewReader(inner))
	if err != nil {
		return nil, fmt.Errorf("the release payload is not readable")
	}
	defer reader.Close()

	// Bounded, because what is being decompressed came from the cluster and a
	// gzip bomb would otherwise be a bastion out of memory rather than a bad row.
	document, err := io.ReadAll(io.LimitReader(reader, maxHelmPayload))
	if err != nil {
		return nil, fmt.Errorf("the release payload is not readable")
	}
	if len(document) == maxHelmPayload {
		return nil, fmt.Errorf("the release payload is too large to read")
	}
	return document, nil
}

// encodeHelmPayload renders a release back into what goes in the Secret's
// `release` key: gzip, base64 as Helm writes it, base64 again as Kubernetes
// expects every Secret value. Reading it back has to give the same object, which
// is what the round-trip test pins.
func encodeHelmPayload(document []byte) (string, error) {
	var buffer bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
	if err != nil {
		return "", fmt.Errorf("the release could not be compressed")
	}
	if _, err := writer.Write(document); err != nil {
		return "", fmt.Errorf("the release could not be compressed")
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("the release could not be compressed")
	}

	inner := base64.StdEncoding.EncodeToString(buffer.Bytes())
	return base64.StdEncoding.EncodeToString([]byte(inner)), nil
}

// helmReleaseOf pulls the release object out of a Secret as the cluster returned
// it. A Secret carrying the label but not a readable release is skipped by the
// caller rather than failing a list — one unreadable row is not a broken cluster.
func helmReleaseOf(secret map[string]any) (map[string]any, error) {
	data, _ := secret["data"].(map[string]any)
	encoded, _ := data["release"].(string)
	if encoded == "" {
		return nil, fmt.Errorf("the secret holds no release")
	}

	document, err := decodeHelmPayload(encoded)
	if err != nil {
		return nil, err
	}

	var release map[string]any
	if err := json.Unmarshal(document, &release); err != nil || release == nil {
		return nil, fmt.Errorf("the release payload is not a release")
	}
	return release, nil
}

// secretNamespace reads where a Secret lives, for a release payload that does
// not say so itself.
func secretNamespace(secret map[string]any) string {
	metadata, _ := secret["metadata"].(map[string]any)
	namespace, _ := metadata["namespace"].(string)
	return namespace
}

// helmRevision reads a release's revision. JSON numbers arrive as float64, and a
// release with no version is revision zero, which sorts below every real one.
func helmRevision(release map[string]any) int {
	version, _ := release["version"].(float64)
	return int(version)
}

// helmView flattens a release into the columns a list shows.
func helmView(release map[string]any) helmReleaseView {
	view := helmReleaseView{Revision: helmRevision(release)}
	view.Name, _ = release["name"].(string)
	view.Namespace, _ = release["namespace"].(string)

	if info, ok := release["info"].(map[string]any); ok {
		view.Status, _ = info["status"].(string)
		view.Description, _ = info["description"].(string)
		if deployed, ok := info["last_deployed"].(string); ok {
			// A release written by an older Helm may carry a timestamp this does
			// not parse; an unset time reads as "unknown" rather than as an error.
			if at, err := time.Parse(time.RFC3339, deployed); err == nil {
				view.UpdatedAt = at
			}
		}
	}

	if chart, ok := release["chart"].(map[string]any); ok {
		if metadata, ok := chart["metadata"].(map[string]any); ok {
			view.ChartName, _ = metadata["name"].(string)
			view.ChartVersion, _ = metadata["version"].(string)
			view.AppVersion, _ = metadata["appVersion"].(string)
		}
	}
	return view
}

/* --------------------------------------------------------------- reading --- */

// helmSecretsPath renders the release-Secret list path. An empty namespace reads
// cluster-wide, which is the shape an unscoped grant's all-namespaces read
// takes; a scoped grant never gets here with one, because readScope has already
// turned its "all" into one path per granted namespace.
//
// The selector is what makes this a Helm read rather than a secrets read: only
// Secrets Helm owns come back, and nothing else in the namespace is listed.
func helmSecretsPath(namespace, release string) string {
	selector := helmOwnerSelector
	if release != "" {
		selector += ",name=" + release
	}

	query := url.Values{}
	query.Set("labelSelector", selector)
	if namespace == "" {
		return "/api/v1/secrets?" + query.Encode()
	}
	return fmt.Sprintf("/api/v1/namespaces/%s/secrets?%s", url.PathEscape(namespace), query.Encode())
}

// helmScopePaths renders the reads one scope covers, mirroring readScope.paths
// for a list that carries a label selector.
func helmScopePaths(scope readScope) []string {
	if len(scope.Namespaces) == 0 {
		return []string{helmSecretsPath("", "")}
	}

	out := make([]string, 0, len(scope.Namespaces))
	for _, namespace := range scope.Namespaces {
		out = append(out, helmSecretsPath(namespace, ""))
	}
	return out
}

// helmReleaseTarget resolves the release a single-release call addresses: the
// namespace checked against the grant, and a name that is a name.
func (s *server) helmReleaseTarget(c *gin.Context, grant db.UserClusterAccess) (string, string, bool) {
	namespace, ok := s.resourceNamespace(c, grant)
	if !ok {
		return "", "", false
	}

	name := strings.TrimSpace(c.Param("name"))
	if !helmReleaseName.MatchString(name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "that is not a Helm release name"})
		return "", "", false
	}
	return namespace, name, true
}

// helmStoredRevision is one revision as Helm stores it: the Secret exactly as
// the cluster returned it — its resourceVersion is what makes a later write
// conditional — and the release decoded out of it.
type helmStoredRevision struct {
	secret   map[string]any
	release  map[string]any
	revision int
}

// helmRevisions reads every stored revision of one release, newest first.
//
// One list is the whole history: Helm keeps a Secret per revision under the same
// `name` label, so nothing here computes a Secret name from a number. That
// matters beyond tidiness — a revision is the one value a caller supplies that
// could otherwise address a Secret, and resolving it against what came back
// means a caller can only ever name a revision that exists.
func (s *server) helmRevisions(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, namespace, name string,
) ([]helmStoredRevision, bool) {
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if !s.fetch(c, user, cluster, grant, helmSecretsPath(namespace, name), &list) {
		return nil, false
	}

	revisions := helmStoredRevisionsOf(list.Items)
	if len(revisions) == 0 {
		c.JSON(http.StatusNotFound, gin.H{
			"error": fmt.Sprintf("no Helm release named %q in namespace %s", name, namespace),
		})
		return nil, false
	}
	return revisions, true
}

// helmStoredRevisionsOf decodes a release-Secret list into its revisions, newest
// first — which is both what a history reads as and what makes the first entry
// the current revision for everything else here.
func helmStoredRevisionsOf(items []map[string]any) []helmStoredRevision {
	revisions := make([]helmStoredRevision, 0, len(items))
	for _, item := range items {
		decoded, err := helmReleaseOf(item)
		if err != nil {
			// A Secret carrying Helm's labels that holds no readable release is
			// somebody else's; one bad row is not a broken history.
			continue
		}
		revisions = append(revisions, helmStoredRevision{
			secret:   item,
			release:  decoded,
			revision: helmRevision(decoded),
		})
	}

	slices.SortFunc(revisions, func(a, b helmStoredRevision) int { return b.revision - a.revision })
	return revisions
}

// latestHelmSecret finds the current revision of one release: the highest
// revision Helm has stored for that name. Asking for the highest rather than
// computing a Secret name means a release whose history has been pruned, or
// whose revisions do not start at one, still resolves.
func (s *server) latestHelmSecret(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, namespace, name string,
) (secret, release map[string]any, ok bool) {
	revisions, ok := s.helmRevisions(c, user, cluster, grant, namespace, name)
	if !ok {
		return nil, nil, false
	}
	return revisions[0].secret, revisions[0].release, true
}

// listHelmReleases returns the current revision of every release in scope. Helm
// keeps every revision as a Secret of its own, so the list is deduplicated down
// to the highest revision per release — the one that describes what is installed.
func (s *server) listHelmReleases(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	latest := map[string]helmReleaseView{}
	for _, path := range helmScopePaths(scope) {
		var list struct {
			Items []map[string]any `json:"items"`
		}
		if !s.fetch(c, user, cluster, grant, path, &list) {
			return
		}

		for _, item := range list.Items {
			release, err := helmReleaseOf(item)
			if err != nil {
				// A Secret labelled as Helm's that does not hold a readable
				// release is somebody else's; one bad row is not a failed list.
				continue
			}
			view := helmView(release)
			if view.Name == "" {
				continue
			}
			// A release names its own namespace; the Secret holding it is the
			// fallback for a payload old enough not to.
			if view.Namespace == "" {
				view.Namespace = secretNamespace(item)
			}

			key := view.Namespace + "/" + view.Name
			if current, seen := latest[key]; seen && current.Revision >= view.Revision {
				continue
			}
			latest[key] = view
		}
	}

	out := make([]helmReleaseView, 0, len(latest))
	for _, view := range latest {
		out = append(out, view)
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{
		"releases":       out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
	})
}

// showHelmReleaseValues returns the values a release was installed with, as the
// YAML they were written as. This is `helm get values`: the operator's own
// input, not the chart's defaults merged into it.
func (s *server) showHelmReleaseValues(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	namespace, name, ok := s.helmReleaseTarget(c, grant)
	if !ok {
		return
	}

	_, release, ok := s.latestHelmSecret(c, user, cluster, grant, namespace, name)
	if !ok {
		return
	}

	document, err := helmValuesYAML(release)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	view := helmView(release)
	c.JSON(http.StatusOK, gin.H{
		"release": view,
		"yaml":    document,
		"warning": helmValuesWarning,
	})
}

// showHelmReleaseHistory returns every revision Helm has stored for one release,
// newest first — `helm history`.
//
// The list route deliberately shows one row per release, deduplicated down to
// the highest revision, because that is what answers "what is installed". This
// is the other half of the same data and a different question: what this release
// has been, when it changed, and what an operator would be going back to.
//
// It is a read of the same labelled Secrets under the same impersonation, so a
// `view` grant is refused here in the cluster's own words exactly as it is on
// the values read.
func (s *server) showHelmReleaseHistory(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	namespace, name, ok := s.helmReleaseTarget(c, grant)
	if !ok {
		return
	}

	revisions, ok := s.helmRevisions(c, user, cluster, grant, namespace, name)
	if !ok {
		return
	}

	history := make([]helmReleaseView, 0, len(revisions))
	for _, stored := range revisions {
		view := helmView(stored.release)
		// A release names its own namespace; the Secret holding it is the
		// fallback for a payload old enough not to.
		if view.Namespace == "" {
			view.Namespace = secretNamespace(stored.secret)
		}
		if view.Name == "" {
			view.Name = name
		}
		history = append(history, view)
	}

	c.JSON(http.StatusOK, gin.H{
		"release": history[0],
		"history": history,
		"warning": helmRollbackWarning,
	})
}

// helmValuesYAML renders a release's `config` — the supplied values — as YAML. A
// release installed with no values at all renders as an empty document rather
// than as `null`, so the editor opens on something an operator can type into.
func helmValuesYAML(release map[string]any) (string, error) {
	config, _ := release["config"].(map[string]any)
	if len(config) == 0 {
		return "{}\n", nil
	}

	document, err := yaml.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("the release values could not be rendered as YAML")
	}
	return string(document), nil
}

/* --------------------------------------------------------------- writing --- */

// updateHelmReleaseValues records new values as the next Helm revision.
//
// Helm's storage is append-only: an upgrade writes a new Secret at `v{n+1}` and
// leaves the previous one marked `superseded`. This does the same thing, which
// is what keeps `helm history` and `helm rollback` meaningful afterwards — the
// alternative, editing the current revision in place, would rewrite history.
//
// What it does *not* do is render anything. The manifest carried in the new
// revision is the previous revision's, unchanged, because KubeMG has no chart to
// template from; the cluster keeps running exactly what it was running. The
// response says so, and so does the UI.
func (s *server) updateHelmReleaseValues(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	namespace, name, ok := s.helmReleaseTarget(c, grant)
	if !ok {
		return
	}

	values, ok := helmValuesFrom(c)
	if !ok {
		return
	}

	secret, release, ok := s.latestHelmSecret(c, user, cluster, grant, namespace, name)
	if !ok {
		return
	}

	s.appendHelmRevision(c, user, cluster, grant, namespace, name,
		helmStoredRevision{secret: secret, release: release, revision: helmRevision(release)},
		values, helmUpdateDescription, helmValuesWarning)
}

// rollbackHelmRelease restores an earlier revision's values as a new revision.
//
// It is `updateHelmReleaseValues` with its values read out of the history rather
// than off the wire, which is exactly what it should be: the same append, the
// same impersonated write, the same audit record. The revision the caller names
// is resolved against the revisions the cluster returned rather than turned into
// a Secret name, so the only revisions reachable are the ones that exist.
//
// What is restored is the target's `config` and nothing else. See the package
// comment: carrying its chart and manifest forward as well would record a
// revision that disagrees with what is running, and the next `helm upgrade`
// would then merge against a state the cluster was never in.
func (s *server) rollbackHelmRelease(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	namespace, name, ok := s.helmReleaseTarget(c, grant)
	if !ok {
		return
	}

	wanted, ok := helmRollbackTargetFrom(c)
	if !ok {
		return
	}

	revisions, ok := s.helmRevisions(c, user, cluster, grant, namespace, name)
	if !ok {
		return
	}
	current := revisions[0]

	index := slices.IndexFunc(revisions, func(stored helmStoredRevision) bool {
		return stored.revision == wanted
	})
	if index < 0 {
		c.JSON(http.StatusNotFound, gin.H{
			"error": fmt.Sprintf("%s has no revision %d — Helm may have pruned it", name, wanted),
		})
		return
	}
	if wanted == current.revision {
		// Not an error the cluster would raise, but appending a copy of the
		// current revision is a write that changes nothing and hides what it did.
		c.JSON(http.StatusConflict, gin.H{
			"error": fmt.Sprintf("revision %d is already the current one", wanted),
		})
		return
	}

	values, _ := revisions[index].release["config"].(map[string]any)
	if values == nil {
		// A revision installed with no values at all is a real state, and
		// restoring it means restoring emptiness rather than refusing.
		values = map[string]any{}
	}

	s.appendHelmRevision(c, user, cluster, grant, namespace, name, current, values,
		fmt.Sprintf(helmRollbackDescription, wanted), fmt.Sprintf(helmRollbackWarningFor, wanted))
}

// appendHelmRevision writes the next revision and supersedes the one it
// replaces. Both writes go down the impersonated tunnel, so a caller the cluster
// will not let create a Secret in that namespace is refused by the cluster.
func (s *server) appendHelmRevision(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, namespace, name string, current helmStoredRevision,
	values map[string]any, description, warning string,
) {
	next, err := nextHelmSecret(current.secret, current.release, values,
		namespace, name, current.revision, description)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	document, err := json.Marshal(next)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the new revision could not be encoded"})
		return
	}

	path := fmt.Sprintf("/api/v1/namespaces/%s/secrets", url.PathEscape(namespace))
	resp, callOK := s.callResourceWith(c, user, cluster, grant,
		http.MethodPost, path, document, "could not write the new revision to the cluster")
	if !callOK {
		return
	}
	if resp.Status < 200 || resp.Status >= 300 {
		c.JSON(resp.Status, gin.H{"error": kubeErrorMessage(resp.Body, resp.Status)})
		return
	}

	// The previous revision is superseded only once the new one exists. If this
	// fails the release is still correct — Helm reads the highest revision — but
	// `helm history` would show two deployed rows, which is worth saying.
	if err := s.supersedeHelmSecret(c, user, cluster, grant,
		namespace, current.secret, current.release); err != nil {
		warning += " The previous revision could not be marked superseded: " + err.Error()
	}

	view := helmView(current.release)
	view.Revision = current.revision + 1
	view.Status = "deployed"
	view.Description = description
	view.UpdatedAt = time.Now().UTC()

	c.JSON(http.StatusOK, gin.H{
		"release": view,
		"yaml":    helmValuesDocument(values),
		"warning": warning,
	})
}

// helmRollbackTargetFrom reads the revision a rollback names. It has to be a
// positive integer — Helm numbers revisions from one — and everything else about
// whether it is reachable is decided by resolving it against the stored history.
func helmRollbackTargetFrom(c *gin.Context) (int, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxHelmValues)

	var payload struct {
		Revision int `json:"revision"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the revision could not be read"})
		return 0, false
	}
	if payload.Revision < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name the revision to roll back to"})
		return 0, false
	}
	return payload.Revision, true
}

// helmUpdateDescription is what `helm history` will show for a revision written
// from here. It names KubeMG on purpose: a revision nobody can account for is
// worse than one that says where it came from.
const helmUpdateDescription = "Values updated through KubeMG"

// helmRollbackDescription names the revision that was restored, in the same
// place `helm rollback` writes "Rollback to 3" — so `helm history` reads the way
// an operator expects even though KubeMG wrote the row.
const helmRollbackDescription = "Rolled back to revision %d through KubeMG"

// helmRollbackWarningFor is the limit of a rollback here, and it is deliberately
// blunt: "roll back" is the most load-bearing word in this file, and an operator
// who reads it as "undo the deployment" has been misled by the name rather than
// by the product. It travels with the history read as well as with the write, so
// it is on screen before the click rather than in the receipt.
const helmRollbackWarningFor = "Restores revision %d's values as a new Helm revision. Unlike helm " +
	"rollback it re-applies nothing: the chart, the rendered manifest and everything running are " +
	"unchanged until the next helm upgrade, which will then render from these values."

// helmRollbackWarning is the same statement with no revision to name, for the
// history read — the surface offering the action has to carry the caveat.
const helmRollbackWarning = "Rolling back here restores a revision's values as a new Helm revision. " +
	"Unlike helm rollback it re-applies nothing: the chart, the rendered manifest and everything " +
	"running are unchanged until the next helm upgrade, which will then render from those values."

// helmValuesFrom reads the submitted values document. Values are a mapping or
// they are nothing — a bare scalar or a list is not something Helm can merge
// into a chart, and the API server would only refuse it much later.
func helmValuesFrom(c *gin.Context) (map[string]any, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxHelmValues)

	var payload struct {
		YAML string `json:"yaml"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the values could not be read"})
		return nil, false
	}

	// An empty document means a release with no values, which is a real state:
	// `helm upgrade --reset-values` leaves exactly that.
	if strings.TrimSpace(payload.YAML) == "" {
		return map[string]any{}, true
	}

	document, err := yaml.YAMLToJSON([]byte(payload.YAML))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "this is not valid YAML: " + err.Error()})
		return nil, false
	}

	var values map[string]any
	if err := json.Unmarshal(document, &values); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Helm values have to be a mapping of keys to values",
		})
		return nil, false
	}
	if values == nil {
		// `null`, or a document that is only comments. Same meaning as empty.
		values = map[string]any{}
	}
	return values, true
}

// helmValuesDocument renders values back for the response, so the editor shows
// what was actually stored rather than what was typed.
func helmValuesDocument(values map[string]any) string {
	if len(values) == 0 {
		return "{}\n"
	}
	document, err := yaml.Marshal(values)
	if err != nil {
		return "{}\n"
	}
	return string(document)
}

// nextHelmSecret builds the Secret for the next revision. The release object is
// the previous one with its values, revision and status replaced — everything
// else, the chart and the rendered manifest above all, is carried across
// untouched, because it is still what is deployed.
//
// `description` is what `helm history` will show for the row, and it is a
// parameter because a rollback and a values edit are the same append with
// different provenance, and a revision nobody can account for is worse than one
// that says where it came from.
func nextHelmSecret(secret, release map[string]any, values map[string]any,
	namespace, name string, revision int, description string,
) (map[string]any, error) {
	next := revision + 1

	updated := make(map[string]any, len(release)+1)
	for key, value := range release {
		updated[key] = value
	}
	updated["config"] = values
	updated["version"] = next

	info := map[string]any{}
	if previous, ok := release["info"].(map[string]any); ok {
		for key, value := range previous {
			info[key] = value
		}
	}
	info["status"] = "deployed"
	info["description"] = description
	info["last_deployed"] = time.Now().UTC().Format(time.RFC3339Nano)
	updated["info"] = info

	document, err := json.Marshal(updated)
	if err != nil {
		return nil, fmt.Errorf("the new revision could not be encoded")
	}
	payload, err := encodeHelmPayload(document)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"type":       helmSecretType,
		"metadata": map[string]any{
			"name":      helmSecretPrefix + name + ".v" + strconv.Itoa(next),
			"namespace": namespace,
			"labels":    helmLabels(secret, name, next, "deployed"),
		},
		"data": map[string]any{"release": payload},
	}, nil
}

// helmLabels carries the previous revision's labels forward with the four Helm
// sets from the release itself replaced. Copying rather than writing a fresh set
// keeps whatever else a given Helm version labels a release with.
func helmLabels(secret map[string]any, name string, revision int, status string) map[string]any {
	labels := map[string]any{}
	if metadata, ok := secret["metadata"].(map[string]any); ok {
		if previous, ok := metadata["labels"].(map[string]any); ok {
			for key, value := range previous {
				labels[key] = value
			}
		}
	}

	labels["owner"] = "helm"
	labels["name"] = name
	labels["status"] = status
	labels["version"] = strconv.Itoa(revision)
	return labels
}

// supersedeHelmSecret marks the revision that was current before this write.
// Both copies of the status have to move — the label Helm queries by and the
// status inside the payload Helm reads — or `helm history` and `helm list`
// disagree with each other.
//
// It is a full replace rather than a patch because the tunnel sends one content
// type, and a PATCH the API server reads as JSON is not a patch it accepts. The
// object is the one just read, so its resourceVersion travels with it: a release
// upgraded by someone else in between makes this conflict rather than clobber.
func (s *server) supersedeHelmSecret(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, namespace string, secret, release map[string]any,
) error {
	metadata, _ := secret["metadata"].(map[string]any)
	previousName, _ := metadata["name"].(string)
	if previousName == "" {
		return fmt.Errorf("the previous revision has no name")
	}

	info := map[string]any{}
	if current, ok := release["info"].(map[string]any); ok {
		for key, value := range current {
			info[key] = value
		}
	}
	info["status"] = "superseded"
	release["info"] = info

	document, err := json.Marshal(release)
	if err != nil {
		return fmt.Errorf("the previous revision could not be encoded")
	}
	payload, err := encodeHelmPayload(document)
	if err != nil {
		return err
	}

	if labels, ok := metadata["labels"].(map[string]any); ok {
		labels["status"] = "superseded"
	}
	secret["data"] = map[string]any{"release": payload}

	body, err := json.Marshal(secret)
	if err != nil {
		return fmt.Errorf("the previous revision could not be encoded")
	}

	path := fmt.Sprintf("/api/v1/namespaces/%s/secrets/%s",
		url.PathEscape(namespace), url.PathEscape(previousName))
	resp, err := s.proxy.Call(c.Request.Context(), user, cluster, grant, http.MethodPut, path, body, nil)
	if err != nil {
		return fmt.Errorf("the cluster could not be reached")
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return fmt.Errorf("%s", kubeErrorMessage(resp.Body, resp.Status))
	}
	return nil
}
