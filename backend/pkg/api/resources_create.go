package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"sigs.k8s.io/yaml"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/objdiff"
)

/*
 * Creating one object.
 *
 * `kubectl create -f` is the one thing an operator could do through the tunnel
 * from a terminal and not from the console: the manifest editor could change an
 * object and delete it, but the object had to already exist for either to have
 * an address. So this is the editor's opening move rather than a new surface —
 * the same sidebar kind key, the same fixed path table, the same impersonated
 * call, the same guardrails, and `create` in the audit trail because that is
 * what `VerbFor` makes of a POST.
 *
 * Three things keep it as narrow as the write it mirrors. The **collection**
 * path is built from the kind table and the apiVersion the manifest itself
 * declares, so a caller still never names an API path and cannot post an object
 * into a different API from the one the list it is looking at is served by. The
 * namespace goes through the same grant check every namespaced read uses, and a
 * manifest naming a different one is refused rather than silently moved — a
 * create that lands somewhere the operator did not mean is worse than a refused
 * one. And **nothing here is a new permission**: the POST is impersonated, so a
 * `view` grant is refused by the cluster's own RBAC in the cluster's own words.
 *
 * There is no bulk create for the reason there is no bulk delete, and no
 * server-side templating: KubeMG has no chart to render, so what is posted is
 * exactly the document the operator typed.
 */

// notCreatable names the kinds KubeMG will not create from a manifest, with the
// reason an operator is told. It is a deny list rather than a `creatable` flag
// on every entry of objectKinds because all but a handful of kinds create
// exactly like they update, and the interesting content here is *why* the
// exceptions are exceptions.
//
// Note what is deliberately *absent*. Secrets are read-only in the editor
// because their values are redacted on the way out and writing back the
// placeholder would destroy them — a fact about a manifest KubeMG rendered, not
// about one an operator typed, so creating a Secret is allowed and comes back
// redacted like any other read of it. CRDs are creatable because the editor has
// been able to change one since the day it existed, and refusing to install one
// while allowing it to be rewritten would be an arbitrary line.
var notCreatable = map[string]string{
	// The rule the manifest editor's own comment states: KubeMG does not
	// author the cluster's RBAC. Updating a Role that somebody else created is
	// the generic editor doing what it does for every kind; creating the
	// bindings that decide who may do what, from a tool with its own separate
	// permission model, is how the two silently diverge.
	"roles":               rbacCreateRefusal,
	"rolebindings":        rbacCreateRefusal,
	"clusterroles":        rbacCreateRefusal,
	"clusterrolebindings": rbacCreateRefusal,

	// A Node is not created, it joins: the kubelet registers it. Posting one
	// makes an object no kubelet is behind, which reads as a cluster member
	// that is permanently NotReady.
	"nodes": "A node joins a cluster when its kubelet registers with the API server, " +
		"so there is no node to create here.",
}

const rbacCreateRefusal = "kubemg does not author a cluster's RBAC. Create Roles and bindings " +
	"with kubectl or whatever manages them, and read them back here."

// createResourceObject posts a new object to the collection its kind is served
// by. The response is the object the cluster stored, including everything it
// filled in itself — which is the answer to "did that do what I meant".
func (s *server) createResourceObject(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	kind, namespace, ok := s.resourceCreateTarget(c, grant)
	if !ok {
		return
	}

	object, ok := readManifestBody(c)
	if !ok {
		return
	}

	path, reason := kind.createPath(object, namespace)
	if reason != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": reason})
		return
	}

	// managedFields belong to whatever wrote a field last; a manifest copied
	// out of another object carries them and they mean nothing here. Stripped
	// rather than refused, exactly as the update path strips them.
	stripManagedFields(object)
	document, err := json.Marshal(object)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the manifest could not be encoded"})
		return
	}

	// A create's diff is the whole object appearing, so the pre-image is empty
	// and no read is needed to establish it. The two rules the update path
	// applies still apply: opt-in, and never for a redacted kind — a diff over
	// a Secret somebody just typed would put every value it holds in a
	// database row, which is the one thing redaction exists to prevent.
	var auditDiff []byte
	if runtime := s.settings(c.Request.Context()); runtime.RecordManifestDiffs && !kind.redacted {
		if encoded, err := json.Marshal(objdiff.Diff(map[string]any{}, object)); err == nil {
			auditDiff = encoded
		}
	}

	resp, callOK := s.callResourceWith(c, user, cluster, grant,
		http.MethodPost, path, document, "could not write to the cluster", auditDiff)
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
	// 201, because that is what happened, and because a client that has to
	// tell "created" from "already there" should not have to read the body.
	c.JSON(http.StatusCreated, view)
}

// resourceCreateTarget resolves what a create addresses. It is
// resourceObjectTarget without a name — the name is the manifest's to declare,
// and may be absent entirely when it carries `generateName` instead — plus the
// one check the object routes do not need: whether this kind may be created at
// all.
func (s *server) resourceCreateTarget(c *gin.Context, grant db.UserClusterAccess) (objectKind, string, bool) {
	var none objectKind

	key := strings.TrimSpace(c.Query("kind"))
	kind, known := objectKinds[key]
	if !known {
		if kind, known = customObjectKind(key, c.Query("namespace")); !known {
			c.JSON(http.StatusBadRequest, gin.H{"error": "kubemg does not serve manifests for " + key})
			return none, "", false
		}
	}
	if reason, refused := notCreatable[key]; refused {
		c.JSON(http.StatusConflict, gin.H{"error": reason})
		return none, "", false
	}

	if !kind.namespaced {
		if !s.requireClusterScope(c, grant, key) {
			return none, "", false
		}
		return kind, "", true
	}

	// A create needs one namespace, not a scope: "all namespaces" is a reading
	// of a list and there is nothing for it to mean here.
	if c.Query("all_namespaces") == "true" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name the namespace to create this in"})
		return none, "", false
	}
	namespace, ok := s.resourceNamespace(c, grant)
	if !ok {
		return none, "", false
	}
	return kind, namespace, true
}

// readManifestBody decodes a submitted manifest, bounded and reported in the
// same words the update and diff paths use — one shape of request, one set of
// refusals, rather than three copies drifting apart.
func readManifestBody(c *gin.Context) (map[string]any, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxManifestBody)
	var payload struct {
		YAML string `json:"yaml"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the manifest could not be read"})
		return nil, false
	}
	if strings.TrimSpace(payload.YAML) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the manifest is empty"})
		return nil, false
	}

	document, err := yaml.YAMLToJSON([]byte(payload.YAML))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "this is not valid YAML: " + err.Error()})
		return nil, false
	}
	var object map[string]any
	if err := json.Unmarshal(document, &object); err != nil || object == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a manifest has to be a single YAML object"})
		return nil, false
	}
	return object, true
}

// createPath decides which collection a manifest is posted to. Like writePath,
// it is built from the kind table and the apiVersion the manifest declares, so
// this cannot be used to post to an arbitrary API path — and the manifest has
// to agree with the namespace the caller is creating in.
func (k objectKind) createPath(object map[string]any, namespace string) (string, string) {
	apiVersion, _ := object["apiVersion"].(string)
	if apiVersion == "" {
		return "", "the manifest has no apiVersion"
	}
	if kind, _ := object["kind"].(string); kind == "" {
		return "", "the manifest has no kind"
	}

	version, found := k.versionFor(groupPath(apiVersion))
	if !found {
		return "", fmt.Sprintf("apiVersion %s is not the API this resource is served by; "+
			"kubemg will not post an object to a different API from here", apiVersion)
	}

	metadata, _ := object["metadata"].(map[string]any)
	if metadata == nil {
		return "", "the manifest has no metadata"
	}
	name, _ := metadata["name"].(string)
	generated, _ := metadata["generateName"].(string)
	if strings.TrimSpace(name) == "" && strings.TrimSpace(generated) == "" {
		return "", "the manifest needs a metadata.name (or a metadata.generateName for the cluster to name it)"
	}

	if !k.namespaced {
		return version.clusterWide(), ""
	}
	// An absent namespace means the one being created in; a present one has to
	// match it, because a manifest quietly landing in another namespace is the
	// mistake this check exists to catch.
	if got, _ := metadata["namespace"].(string); got != "" && got != namespace {
		return "", fmt.Sprintf("the manifest is in namespace %q but you are creating in %q", got, namespace)
	}
	return version.namespaced(namespace), ""
}
