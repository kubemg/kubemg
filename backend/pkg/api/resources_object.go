package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
	"sigs.k8s.io/yaml"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/objdiff"
)

/*
 * One object, as YAML, read and written through the same tunnel as everything
 * else. The lists in resources.go and resources_inventory.go are normalised down
 * to the few columns a table shows; this is the opposite surface — the whole
 * object, in the form an operator already knows how to read.
 *
 * It is the first *write* path the resource API has, so it is deliberately
 * narrow: only the kinds the Explore sidebar browses can be addressed, the API
 * path is derived from a fixed table rather than from anything the caller sends,
 * and the manifest has to name the object it is being applied to. What the
 * caller may actually do is still the cluster's decision — the PUT goes down the
 * tunnel impersonated, so a `view` grant is refused by the cluster's own RBAC
 * rather than by a check here that would only duplicate it.
 */

// maxManifestBody caps a submitted manifest. A Kubernetes object that does not
// fit in a megabyte is not one somebody is hand-editing.
const maxManifestBody = 1 << 20

// lastAppliedAnnotation is kubectl's copy of the previous manifest. It is a
// duplicate of the object it is attached to and routinely longer than it, so it
// is stripped: an editor that opens on two copies of the same thing is unusable.
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// guardrailBlockedField mirrors bastion's own (unexported) constant of the same
// value. It cannot be imported — the streaming path's flag is package-private —
// so the value is duplicated rather than the coupling invented; both call sites
// exist to give a client the same one flag for "KubeMG's own policy said no".
const guardrailBlockedField = "guardrail_blocked"

// redactedValue replaces a Secret's data. It is not valid base64 on purpose —
// a placeholder that could be mistaken for the real value would be worse than
// no placeholder at all.
const redactedValue = "<redacted by KubeMG>"

// objectKind is how a browsable resource is addressed as a single object.
type objectKind struct {
	// versions are the API paths to try, in preference order. More than one
	// only for a CRD whose group has moved version between releases.
	versions []resourceListPath
	// namespaced mirrors the scope the Explore sidebar declares for this key.
	namespaced bool
	// writable is false for the kinds KubeMG will not send back. It is not a
	// permission — the cluster decides that — it is a statement that KubeMG
	// cannot faithfully round-trip the object it showed.
	writable bool
	// readOnlyReason explains that refusal to the operator.
	readOnlyReason string
	// redacted marks a kind whose values never leave the cluster as read —
	// today only Secrets. It used to be inferred at the call site by
	// comparing the key against the literal "secrets", which happened to be
	// correct only because secrets is also the sole non-writable kind; the
	// two facts are unrelated (a future read-only kind would not necessarily
	// be redacted) so this is its own field. It governs two things: whether
	// renderObject blanks values before the editor ever sees them, and
	// whether a stored audit diff is allowed to exist at all for a write to
	// this kind — a diff over redacted values would either store the
	// placeholder either side of a real change, which is useless, or, if
	// computed before redaction, store the very values redaction exists to
	// keep out of a database row.
	redacted bool
}

// objectKinds is the addressable inventory: the same keys the Explore sidebar
// uses, mapped onto their API paths. A caller can only ever reach a path built
// from this table, so the manifest editor cannot be turned into an unrestricted
// API client.
var objectKinds = map[string]objectKind{
	"pods":                   {versions: []resourceListPath{{"/api/v1", "pods"}}, namespaced: true, writable: true},
	"deployments":            {versions: []resourceListPath{{"/apis/apps/v1", "deployments"}}, namespaced: true, writable: true},
	"statefulsets":           {versions: []resourceListPath{{"/apis/apps/v1", "statefulsets"}}, namespaced: true, writable: true},
	"daemonsets":             {versions: []resourceListPath{{"/apis/apps/v1", "daemonsets"}}, namespaced: true, writable: true},
	"jobs":                   {versions: []resourceListPath{{"/apis/batch/v1", "jobs"}}, namespaced: true, writable: true},
	"cronjobs":               {versions: []resourceListPath{{"/apis/batch/v1", "cronjobs"}}, namespaced: true, writable: true},
	"services":               {versions: []resourceListPath{{"/api/v1", "services"}}, namespaced: true, writable: true},
	"ingresses":              {versions: []resourceListPath{{"/apis/networking.k8s.io/v1", "ingresses"}}, namespaced: true, writable: true},
	"persistentvolumeclaims": {versions: []resourceListPath{{"/api/v1", "persistentvolumeclaims"}}, namespaced: true, writable: true},
	"configmaps":             {versions: []resourceListPath{{"/api/v1", "configmaps"}}, namespaced: true, writable: true},

	"httproutes": {
		versions: []resourceListPath{
			{"/apis/gateway.networking.k8s.io/v1", "httproutes"},
			{"/apis/gateway.networking.k8s.io/v1beta1", "httproutes"},
		},
		namespaced: true,
		writable:   true,
	},
	"virtualservices": {
		versions: []resourceListPath{
			{"/apis/networking.istio.io/v1", "virtualservices"},
			{"/apis/networking.istio.io/v1beta1", "virtualservices"},
		},
		namespaced: true,
		writable:   true,
	},

	// ServiceAccounts are writable like any other core object; the RBAC kinds
	// below are the deliberate exception. See the block after this one.
	"serviceaccounts": {versions: []resourceListPath{{"/api/v1", "serviceaccounts"}}, namespaced: true, writable: true},

	"persistentvolumes": {versions: []resourceListPath{{"/api/v1", "persistentvolumes"}}, writable: true},
	"storageclasses":    {versions: []resourceListPath{{"/apis/storage.k8s.io/v1", "storageclasses"}}, writable: true},
	"nodes":             {versions: []resourceListPath{{"/api/v1", "nodes"}}, writable: true},

	// The cluster's own RBAC, addressable as objects so the Access lists get the
	// same detail drawer, describe and manifest view every other list has.
	//
	// They are `writable` for one reason and it is worth stating: this is the
	// *generic* manifest editor, which has applied a Role for anyone whose grant
	// permitted it since the day it existed, and singling RBAC out here would
	// take away a capability rather than add safety — the write is impersonated
	// like every other, so the cluster refuses it unless the caller may
	// genuinely do it (and RBAC's `escalate` rule means it usually will). What
	// KubeMG deliberately does *not* build is an RBAC editor of its own: no
	// route here creates a Role or a Binding, because a tool with a separate
	// permission model authoring the cluster's is how the two silently diverge.
	"roles":               {versions: []resourceListPath{{rbacGroup, "roles"}}, namespaced: true, writable: true},
	"rolebindings":        {versions: []resourceListPath{{rbacGroup, "rolebindings"}}, namespaced: true, writable: true},
	"clusterroles":        {versions: []resourceListPath{{rbacGroup, "clusterroles"}}, writable: true},
	"clusterrolebindings": {versions: []resourceListPath{{rbacGroup, "clusterrolebindings"}}, writable: true},
	"namespaces":          {versions: []resourceListPath{{"/api/v1", "namespaces"}}, writable: true},
	"crds": {
		versions: []resourceListPath{{"/apis/apiextensions.k8s.io/v1", "customresourcedefinitions"}},
		writable: true,
	},

	// A Secret's values never enter a response, here as anywhere else — so what
	// KubeMG can show is not the object, and writing back what it showed would
	// overwrite every value with the placeholder standing in for it.
	"secrets": {
		versions:       []resourceListPath{{"/api/v1", "secrets"}},
		namespaced:     true,
		readOnlyReason: "A Secret's values are redacted before they leave the cluster, so this manifest is not the whole object and KubeMG will not write it back. Change a Secret with kubectl.",
		redacted:       true,
	},
}

// objectView is one object rendered for the editor.
type objectView struct {
	YAML            string `json:"yaml"`
	Kind            string `json:"kind"`
	APIVersion      string `json:"api_version"`
	Name            string `json:"name"`
	Namespace       string `json:"namespace,omitempty"`
	ResourceVersion string `json:"resource_version,omitempty"`
	// Editable reports whether KubeMG will accept this manifest back. It says
	// nothing about the caller's cluster RBAC, which is only settled by trying.
	Editable bool `json:"editable"`
	// Reason explains a manifest that cannot be written back.
	Reason string `json:"reason,omitempty"`
}

// resourceObjectTarget resolves what a single-object call addresses: the kind
// from the fixed table, the name, and the namespace checked against the grant.
func (s *server) resourceObjectTarget(c *gin.Context, grant db.UserClusterAccess) (objectKind, string, string, bool) {
	var none objectKind

	key := strings.TrimSpace(c.Query("kind"))
	kind, known := objectKinds[key]
	if !known {
		// A CRD-served kind is not in the table and cannot be: which CRDs exist
		// is a property of the cluster. Its path is built from the same three
		// validated components the custom list route uses, so it is still a path
		// KubeMG constructs rather than one the caller supplies, and the call is
		// still impersonated — the cluster's RBAC decides whether it is allowed.
		if kind, known = customObjectKind(key, c.Query("namespace")); !known {
			c.JSON(http.StatusBadRequest, gin.H{"error": "kubemg does not serve manifests for " + key})
			return none, "", "", false
		}
	}

	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a resource name is required"})
		return none, "", "", false
	}

	if !kind.namespaced {
		if !s.requireClusterScope(c, grant, key) {
			return none, "", "", false
		}
		return kind, name, "", true
	}

	namespace, ok := s.resourceNamespace(c, grant)
	if !ok {
		return none, "", "", false
	}
	return kind, name, namespace, true
}

// objectPaths renders the candidate API paths for one object.
func (k objectKind) objectPaths(namespace, name string) []string {
	out := make([]string, 0, len(k.versions))
	for _, version := range k.versions {
		base := version.clusterWide()
		if k.namespaced {
			base = version.namespaced(namespace)
		}
		out = append(out, base+"/"+url.PathEscape(name))
	}
	return out
}

// readObject fetches one object, walking the kind's candidate API versions. A
// 404 on anything but the last candidate means the cluster serves an older
// version of an optional CRD; on the last one it means what it says. Any other
// refusal is written to the client in the cluster's own words.
func (s *server) readObject(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, kind objectKind, namespace, name string,
) ([]byte, bool) {
	paths := kind.objectPaths(namespace, name)
	for i, path := range paths {
		resp, callOK := s.callResource(c, user, cluster, grant, path)
		if !callOK {
			return nil, false
		}
		if resp.Status == http.StatusNotFound && i < len(paths)-1 {
			continue
		}
		if resp.Status < 200 || resp.Status >= 300 {
			c.JSON(resp.Status, gin.H{"error": kubeErrorMessage(resp.Body, resp.Status)})
			return nil, false
		}
		return resp.Body, true
	}
	return nil, false
}

// showResourceObject returns one object as YAML.
func (s *server) showResourceObject(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	kind, name, namespace, ok := s.resourceObjectTarget(c, grant)
	if !ok {
		return
	}

	body, ok := s.readObject(c, user, cluster, grant, kind, namespace, name)
	if !ok {
		return
	}

	view, err := renderObject(body, kind)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, view)
}

// updateResourceObject writes an edited manifest back to the cluster. The write
// is impersonated like every other call, so a caller whose role does not allow
// it is refused by the cluster in the cluster's own words.
func (s *server) updateResourceObject(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	kind, name, namespace, ok := s.resourceObjectTarget(c, grant)
	if !ok {
		return
	}
	if !kind.writable {
		c.JSON(http.StatusConflict, gin.H{"error": kind.readOnlyReason})
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxManifestBody)
	var payload struct {
		YAML string `json:"yaml"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the manifest could not be read"})
		return
	}
	if strings.TrimSpace(payload.YAML) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the manifest is empty"})
		return
	}

	document, err := yaml.YAMLToJSON([]byte(payload.YAML))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "this is not valid YAML: " + err.Error()})
		return
	}

	var object map[string]any
	if err := json.Unmarshal(document, &object); err != nil || object == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a manifest has to be a single YAML object"})
		return
	}

	// The manifest has to name the object it is being applied to. The API server
	// would refuse a mismatch too, but only after the round trip, and a rename
	// silently landing somewhere else is worth catching here.
	path, reason := kind.writePath(object, namespace, name)
	if reason != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": reason})
		return
	}

	// managedFields describe who last wrote each field; sending them back is
	// meaningless and the field is stripped from what the editor showed anyway.
	stripManagedFields(object)
	document, err = json.Marshal(object)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the manifest could not be encoded"})
		return
	}

	// The stored audit diff is opt-in, off by default, and never computed for
	// a redacted kind — this is the one place all three rules are enforced
	// together, since the setting is only worth reading if the kind does not
	// already rule the whole feature out. Fetching the pre-image is
	// deliberately best-effort: a diff is a nice-to-have layered on top of a
	// write that must still happen when the extra read fails (the object is
	// being created for the first time, or a concurrent delete raced this
	// request), so beforeObjectForDiff never writes to the response on its
	// own account.
	var auditDiff []byte
	if runtime := s.settings(c.Request.Context()); runtime.RecordManifestDiffs && !kind.redacted {
		if before, ok := s.beforeObjectForDiff(c, user, cluster, grant, kind, namespace, name); ok {
			stripManagedFields(before)
			if encoded, err := json.Marshal(objdiff.Diff(before, object)); err == nil {
				auditDiff = encoded
			}
		}
	}

	resp, callOK := s.callResourceWith(c, user, cluster, grant,
		http.MethodPut, path, document, "could not write to the cluster", auditDiff)
	if !callOK {
		return
	}
	if resp.Status < 200 || resp.Status >= 300 {
		c.JSON(resp.Status, gin.H{"error": kubeErrorMessage(resp.Body, resp.Status)})
		return
	}

	view, err := renderObject(resp.Body, kind)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, view)
}

// writePath decides where an edited manifest goes. The path is built from the
// kind table and the apiVersion the manifest itself declares, and the manifest
// has to agree with the object being replaced — so this cannot be used to write
// to an arbitrary API path.
func (k objectKind) writePath(object map[string]any, namespace, name string) (string, string) {
	apiVersion, _ := object["apiVersion"].(string)
	if apiVersion == "" {
		return "", "the manifest has no apiVersion"
	}
	if kind, _ := object["kind"].(string); kind == "" {
		return "", "the manifest has no kind"
	}

	group := groupPath(apiVersion)
	version, found := k.versionFor(group)
	if !found {
		return "", fmt.Sprintf("apiVersion %s is not the API this resource is served by; "+
			"KubeMG will not move an object to a different API from here", apiVersion)
	}

	metadata, _ := object["metadata"].(map[string]any)
	if metadata == nil {
		return "", "the manifest has no metadata"
	}
	if got, _ := metadata["name"].(string); got != name {
		return "", fmt.Sprintf("the manifest names %q but this is %q; renaming an object creates a new one, "+
			"which this editor does not do", got, name)
	}

	if !k.namespaced {
		return version.clusterWide() + "/" + url.PathEscape(name), ""
	}
	// An absent namespace is taken to mean the one being edited; a present one
	// has to match, because a moved object is a new object.
	if got, _ := metadata["namespace"].(string); got != "" && got != namespace {
		return "", fmt.Sprintf("the manifest is in namespace %q but this object is in %q; "+
			"moving an object between namespaces creates a new one", got, namespace)
	}
	return version.namespaced(namespace) + "/" + url.PathEscape(name), ""
}

// versionFor finds the candidate matching an API group path.
func (k objectKind) versionFor(group string) (resourceListPath, bool) {
	i := slices.IndexFunc(k.versions, func(version resourceListPath) bool {
		return version.group == group
	})
	if i < 0 {
		return resourceListPath{}, false
	}
	return k.versions[i], true
}

// groupPath turns an apiVersion into the path prefix it is served under. The
// core group is the one without a slash, and it lives at /api rather than /apis.
func groupPath(apiVersion string) string {
	if strings.Contains(apiVersion, "/") {
		return "/apis/" + apiVersion
	}
	return "/api/" + apiVersion
}

// renderObject turns an object from the cluster into the manifest the editor
// shows: stripped of bookkeeping, with a Secret's values redacted. Which kind
// this is comes entirely off kind.redacted now — the caller no longer has to
// hand in the key string just so this function can compare it to "secrets".
func renderObject(body []byte, kind objectKind) (objectView, error) {
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return objectView{}, fmt.Errorf("the cluster returned an unreadable object")
	}

	stripManagedFields(object)
	if kind.redacted {
		redactValues(object)
	}

	document, err := yaml.Marshal(object)
	if err != nil {
		return objectView{}, fmt.Errorf("the object could not be rendered as YAML")
	}

	view := objectView{
		YAML:     string(document),
		Editable: kind.writable,
		Reason:   kind.readOnlyReason,
	}
	view.Kind, _ = object["kind"].(string)
	view.APIVersion, _ = object["apiVersion"].(string)
	if metadata, ok := object["metadata"].(map[string]any); ok {
		view.Name, _ = metadata["name"].(string)
		view.Namespace, _ = metadata["namespace"].(string)
		view.ResourceVersion, _ = metadata["resourceVersion"].(string)
	}
	return view, nil
}

// stripManagedFields removes the server-side-apply bookkeeping and kubectl's
// copy of the last applied manifest. Neither is part of the object an operator
// is reading, and together they are usually most of the bytes.
func stripManagedFields(object map[string]any) {
	metadata, ok := object["metadata"].(map[string]any)
	if !ok {
		return
	}
	delete(metadata, "managedFields")

	annotations, ok := metadata["annotations"].(map[string]any)
	if !ok {
		return
	}
	delete(annotations, lastAppliedAnnotation)
	if len(annotations) == 0 {
		delete(metadata, "annotations")
	}
}

// redactValues replaces a Secret's values, keeping its keys. This is the same
// rule the secrets list follows: a key name is inventory, a value is the secret.
func redactValues(object map[string]any) {
	for _, field := range []string{"data", "stringData"} {
		values, ok := object[field].(map[string]any)
		if !ok {
			continue
		}
		for key := range values {
			values[key] = redactedValue
		}
	}
}

// callResourceWith performs a proxied call with a method and a body. The
// cluster's own response is handed back untouched; only a failure to reach it,
// or a refusal from the bastion itself, is answered here.
//
// auditDiff is optional and forwarded to Proxy.Call unchanged; every caller
// but the manifest write omits it. Making it variadic rather than a required
// parameter keeps the five other call sites — scale, restart, helm values,
// the RBAC access review — exactly as they were, since a diff only exists to
// be computed where there is a manifest to diff.
func (s *server) callResourceWith(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, method, path string, body []byte, fallback string,
	auditDiff ...[]byte,
) (*bastion.Response, bool) {
	var diff []byte
	if len(auditDiff) > 0 {
		diff = auditDiff[0]
	}
	resp, err := s.proxy.Call(c.Request.Context(), user, cluster, grant, method, path, body, diff)
	if err != nil {
		var callErr *bastion.CallError
		if errors.As(err, &callErr) {
			// A guardrail refusal carries the policy that fired, which the
			// streaming path has always surfaced (bastion.failGuardrail) and this
			// path did not — a client meeting a bare 403 here could not tell "the
			// cluster's RBAC said no" from "KubeMG's own guardrail said no", and the
			// manifest editor's confirmation step needs the second one to explain
			// itself next to the field that triggered it. The plain "error" field is
			// left untouched so nothing that already reads it breaks.
			body := gin.H{"error": callErr.Message}
			if callErr.Policy != "" {
				body[guardrailBlockedField] = true
				body["policy"] = callErr.Policy
				body["scope"] = callErr.Scope
			}
			c.JSON(callErr.Status, body)
			return nil, false
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": fallback})
		return nil, false
	}
	return resp, true
}

// beforeObjectForDiff reads the object as it stood immediately before a
// write, purely so the stored audit diff has a pre-image to compare against.
// It is readObject's twin with one difference that matters: readObject
// writes the cluster's own refusal to the response, which is correct for a
// call whose entire purpose is that read, but wrong here, where the read is a
// side channel feeding an optional column and the write it sits beside must
// still go ahead even when this fails. So a refusal at any candidate version
// is swallowed and the next is tried; if none answer, the caller gets ok=false
// and proceeds without a diff rather than without a write.
func (s *server) beforeObjectForDiff(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, kind objectKind, namespace, name string,
) (map[string]any, bool) {
	for _, path := range kind.objectPaths(namespace, name) {
		resp, err := s.proxy.Call(c.Request.Context(), user, cluster, grant, http.MethodGet, path, nil, nil)
		if err != nil || resp.Status < 200 || resp.Status >= 300 {
			continue
		}
		var object map[string]any
		if json.Unmarshal(resp.Body, &object) == nil && object != nil {
			return object, true
		}
	}
	return nil, false
}

// prepareForDiff makes a decoded object fit to compare: the same
// managedFields/last-applied strip renderObject already applies, and the same
// redaction for a redacted kind, so a diff never surfaces a value the rest of
// the object's own read path already keeps out of the response.
func prepareForDiff(object map[string]any, kind objectKind) {
	stripManagedFields(object)
	if kind.redacted {
		redactValues(object)
	}
}

// previewResourceObjectDiff answers, before anything is written, what a
// submitted manifest would actually change against the object currently on
// the cluster. It is the confirmation step's one dependency: rather than the
// editor computing its own diff in TypeScript — which would let the diff an
// operator approves quietly disagree with whatever gets stored on the audit
// row afterwards — both go through objdiff.Diff over the same two decoded
// objects, on this one endpoint and inside updateResourceObject.
//
// It performs a fresh read rather than reusing whatever the editor opened on:
// time passes between opening the editor and clicking Apply, and a diff
// against a stale pre-image would be answering a question about a cluster
// state that no longer exists. Nothing is written here — the object handed
// in is only ever compared, never marshalled onto a path — so a grant that
// may not write can still preview one, right up until the PUT itself is
// refused by the cluster's own RBAC.
func (s *server) previewResourceObjectDiff(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	kind, name, namespace, ok := s.resourceObjectTarget(c, grant)
	if !ok {
		return
	}
	if !kind.writable {
		c.JSON(http.StatusConflict, gin.H{"error": kind.readOnlyReason})
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxManifestBody)
	var payload struct {
		YAML string `json:"yaml"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the manifest could not be read"})
		return
	}
	if strings.TrimSpace(payload.YAML) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the manifest is empty"})
		return
	}

	document, err := yaml.YAMLToJSON([]byte(payload.YAML))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "this is not valid YAML: " + err.Error()})
		return
	}
	var after map[string]any
	if err := json.Unmarshal(document, &after); err != nil || after == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a manifest has to be a single YAML object"})
		return
	}
	if _, reason := kind.writePath(after, namespace, name); reason != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": reason})
		return
	}

	body, ok := s.readObject(c, user, cluster, grant, kind, namespace, name)
	if !ok {
		return
	}
	var before map[string]any
	if err := json.Unmarshal(body, &before); err != nil || before == nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "the cluster returned an unreadable object"})
		return
	}

	prepareForDiff(before, kind)
	prepareForDiff(after, kind)
	c.JSON(http.StatusOK, objdiff.Diff(before, after))
}
