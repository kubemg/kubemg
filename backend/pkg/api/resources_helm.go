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

// latestHelmSecret finds the current revision of one release: the highest
// revision Helm has stored for that name. Asking for the highest rather than
// computing a Secret name means a release whose history has been pruned, or
// whose revisions do not start at one, still resolves.
func (s *server) latestHelmSecret(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, namespace, name string,
) (secret, release map[string]any, ok bool) {
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if !s.fetch(c, user, cluster, grant, helmSecretsPath(namespace, name), &list) {
		return nil, nil, false
	}

	best := -1
	for _, item := range list.Items {
		decoded, err := helmReleaseOf(item)
		if err != nil {
			continue
		}
		if revision := helmRevision(decoded); revision > best {
			best, secret, release = revision, item, decoded
		}
	}

	if secret == nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": fmt.Sprintf("no Helm release named %q in namespace %s", name, namespace),
		})
		return nil, nil, false
	}
	return secret, release, true
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
	revision := helmRevision(release)

	next, err := nextHelmSecret(secret, release, values, namespace, name, revision)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	document, err := json.Marshal(next)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the new revision could not be encoded"})
		return
	}

	// The write is impersonated like every other call, so a caller the cluster
	// will not let create a Secret in that namespace is refused by the cluster.
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

	warning := helmValuesWarning
	// The previous revision is superseded only once the new one exists. If this
	// fails the release is still correct — Helm reads the highest revision — but
	// `helm history` would show two deployed rows, which is worth saying.
	if err := s.supersedeHelmSecret(c, user, cluster, grant, namespace, secret, release); err != nil {
		warning += " The previous revision could not be marked superseded: " + err.Error()
	}

	view := helmView(release)
	view.Revision = revision + 1
	view.Status = "deployed"
	view.Description = helmUpdateDescription
	view.UpdatedAt = time.Now().UTC()

	c.JSON(http.StatusOK, gin.H{
		"release": view,
		"yaml":    helmValuesDocument(values),
		"warning": warning,
	})
}

// helmUpdateDescription is what `helm history` will show for a revision written
// from here. It names KubeMG on purpose: a revision nobody can account for is
// worse than one that says where it came from.
const helmUpdateDescription = "Values updated through KubeMG"

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
func nextHelmSecret(secret, release map[string]any, values map[string]any,
	namespace, name string, revision int,
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
	info["description"] = helmUpdateDescription
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
	resp, err := s.proxy.Call(c.Request.Context(), user, cluster, grant, http.MethodPut, path, body)
	if err != nil {
		return fmt.Errorf("the cluster could not be reached")
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return fmt.Errorf("%s", kubeErrorMessage(resp.Body, resp.Status))
	}
	return nil
}
