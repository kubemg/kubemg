package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * The two workload controls an operator reaches for before reaching for a
 * manifest: change how many replicas there are, and roll the pods.
 *
 * Both are already possible through the YAML editor, and that is exactly why
 * they are here. Scaling a Deployment by hand-editing `spec.replicas` in a
 * thousand-line manifest is a write of the whole object to change one integer,
 * and a rollout restart by hand means knowing that the way you ask for one is an
 * annotation on the pod template with a timestamp nobody reads. Neither is a new
 * permission — both go down the tunnel impersonated like every other call, so a
 * `view` grant is refused by the cluster's own RBAC — they are the same two
 * writes with the fifteen-hundred bytes of unrelated object left out.
 *
 * They are `POST`s rather than patches because the tunnel sends one content
 * type: `Proxy.Call` sets `application/json` on any body, and a JSON or strategic
 * merge patch has to arrive as `application/json-patch+json` or
 * `application/strategic-merge-patch+json` or the API server answers 415. So
 * each action is a read-modify-write instead, and the `resourceVersion` from the
 * read travels back with the write — which makes the update conditional on
 * nothing else having changed the object in between, and a concurrent write comes
 * back as the API server's own 409 rather than silently winning.
 *
 * Scale goes through the `scale` subresource rather than the object, so the body
 * that gets written is four fields and a number and there is no way for it to
 * disturb a pod template. Restart has no subresource — the annotation *is* the
 * API — so it writes the object, and strips `managedFields` on the way out for
 * the same reason the manifest editor does.
 */

const (
	// maxReplicas bounds what this route will ask for. The cluster has its own
	// opinion (quotas, and the scheduler), but a mistyped replica count is worth
	// catching before it becomes a thousand pending pods.
	maxReplicas = 1000
	// restartedAtAnnotation is how a rollout restart is requested: kubectl sets
	// this on the pod template, which changes the template hash, which is what
	// actually makes the controller roll the pods. The value is never read.
	restartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"
)

// workloadAction is a workload kind these controls can address. It is its own
// table rather than a flag on objectKinds because the two sets differ: a
// DaemonSet has no replica count to set, and a ReplicaSet has one but rolling it
// is the Deployment's job, not something to ask a ReplicaSet for.
type workloadAction struct {
	path        resourceListPath
	scalable    bool
	restartable bool
}

var workloadActions = map[string]workloadAction{
	"deployments":  {path: resourceListPath{"/apis/apps/v1", "deployments"}, scalable: true, restartable: true},
	"statefulsets": {path: resourceListPath{"/apis/apps/v1", "statefulsets"}, scalable: true, restartable: true},
	"daemonsets":   {path: resourceListPath{"/apis/apps/v1", "daemonsets"}, restartable: true},
	"replicasets":  {path: resourceListPath{"/apis/apps/v1", "replicasets"}, scalable: true},
}

// workloadActionRequest is what both routes accept. `kind` is the same key the
// Explore sidebar uses, so a caller names a resource the way it does everywhere
// else rather than an API path.
type workloadActionRequest struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// Replicas is a pointer because zero is a real and deliberate answer —
	// scaling to none is how a workload is stopped without deleting it.
	Replicas *int32 `json:"replicas"`
}

// workloadActionResult is what comes back: enough for the UI to say what it did
// without re-reading the list, which it refreshes anyway.
type workloadActionResult struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Replicas  *int32 `json:"replicas,omitempty"`
	// RestartedAt is the timestamp written onto the pod template.
	RestartedAt string `json:"restarted_at,omitempty"`
	Message     string `json:"message"`
}

// workloadTarget resolves what an action addresses: the kind from the fixed
// table and the namespace checked against the grant.
func (s *server) workloadTarget(c *gin.Context, grant db.UserClusterAccess,
	req *workloadActionRequest,
) (workloadAction, bool) {
	var none workloadAction

	if err := c.ShouldBindJSON(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the request could not be read"})
		return none, false
	}

	key := strings.TrimSpace(req.Kind)
	action, known := workloadActions[key]
	if !known {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kubemg does not run workload actions on " + key})
		return none, false
	}
	req.Kind = key

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a resource name is required"})
		return none, false
	}

	namespace, ok := s.scopedNamespace(c, grant, req.Namespace)
	if !ok {
		return none, false
	}
	req.Namespace = namespace
	return action, true
}

// objectPath renders the API path for one workload.
func (a workloadAction) objectPath(namespace, name string) string {
	return a.path.namespaced(namespace) + "/" + url.PathEscape(name)
}

// scaleWorkload sets a workload's replica count through the scale subresource.
func (s *server) scaleWorkload(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	var req workloadActionRequest
	action, ok := s.workloadTarget(c, grant, &req)
	if !ok {
		return
	}
	if !action.scalable {
		c.JSON(http.StatusConflict, gin.H{
			"error": "a " + strings.TrimSuffix(req.Kind, "s") + " has no replica count to set",
		})
		return
	}
	if req.Replicas == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a replica count is required"})
		return
	}
	replicas := *req.Replicas
	if replicas < 0 || replicas > maxReplicas {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "a replica count has to be between 0 and 1000",
		})
		return
	}

	path := action.objectPath(req.Namespace, req.Name) + "/scale"

	// Read the current scale first: it settles that the object exists, and its
	// resourceVersion is what makes the write conditional.
	resp, callOK := s.callResource(c, user, cluster, grant, path)
	if !callOK {
		return
	}
	var scale map[string]any
	if !s.decodeResource(c, resp, &scale) {
		return
	}

	spec, _ := scale["spec"].(map[string]any)
	if spec == nil {
		spec = map[string]any{}
		scale["spec"] = spec
	}
	spec["replicas"] = replicas

	body, err := json.Marshal(scale)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the scale request could not be encoded"})
		return
	}

	resp, callOK = s.callResourceWith(c, user, cluster, grant,
		http.MethodPut, path, body, "could not write to the cluster")
	if !callOK {
		return
	}
	if resp.Status < 200 || resp.Status >= 300 {
		c.JSON(resp.Status, gin.H{"error": kubeErrorMessage(resp.Body, resp.Status)})
		return
	}

	c.JSON(http.StatusOK, workloadActionResult{
		Kind:      req.Kind,
		Name:      req.Name,
		Namespace: req.Namespace,
		Replicas:  &replicas,
		Message:   scaledMessage(req.Name, replicas),
	})
}

// scaledMessage says what happened in the words an operator would use.
func scaledMessage(name string, replicas int32) string {
	if replicas == 0 {
		return name + " scaled to 0 — its pods are being removed"
	}
	if replicas == 1 {
		return name + " scaled to 1 replica"
	}
	return name + " scaled to " + strconv.FormatInt(int64(replicas), 10) + " replicas"
}

// restartWorkload rolls a workload's pods by stamping its pod template.
func (s *server) restartWorkload(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	var req workloadActionRequest
	action, ok := s.workloadTarget(c, grant, &req)
	if !ok {
		return
	}
	if !action.restartable {
		c.JSON(http.StatusConflict, gin.H{
			"error": "a " + strings.TrimSuffix(req.Kind, "s") + " is rolled by the controller that owns it, not on its own",
		})
		return
	}

	path := action.objectPath(req.Namespace, req.Name)

	resp, callOK := s.callResource(c, user, cluster, grant, path)
	if !callOK {
		return
	}
	var object map[string]any
	if !s.decodeResource(c, resp, &object) {
		return
	}

	stamp := time.Now().UTC().Format(time.RFC3339)
	if reason := stampRestart(object, stamp); reason != "" {
		c.JSON(http.StatusConflict, gin.H{"error": reason})
		return
	}
	stripManagedFields(object)

	body, err := json.Marshal(object)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the restart request could not be encoded"})
		return
	}

	resp, callOK = s.callResourceWith(c, user, cluster, grant,
		http.MethodPut, path, body, "could not write to the cluster")
	if !callOK {
		return
	}
	if resp.Status < 200 || resp.Status >= 300 {
		c.JSON(resp.Status, gin.H{"error": kubeErrorMessage(resp.Body, resp.Status)})
		return
	}

	c.JSON(http.StatusOK, workloadActionResult{
		Kind:        req.Kind,
		Name:        req.Name,
		Namespace:   req.Namespace,
		RestartedAt: stamp,
		Message:     req.Name + " is rolling out — its pods are being replaced",
	})
}

// stampRestart writes the restart annotation onto a workload's pod template,
// creating the maps it has to pass through. It returns why it could not rather
// than writing back an object it has changed the shape of: a workload whose
// template is not where it should be is not one to guess about.
func stampRestart(object map[string]any, stamp string) string {
	spec, _ := object["spec"].(map[string]any)
	if spec == nil {
		return "the cluster returned a workload with no spec"
	}
	template, _ := spec["template"].(map[string]any)
	if template == nil {
		return "the cluster returned a workload with no pod template to restart"
	}

	metadata, _ := template["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
		template["metadata"] = metadata
	}
	annotations, _ := metadata["annotations"].(map[string]any)
	if annotations == nil {
		annotations = map[string]any{}
		metadata["annotations"] = annotations
	}
	annotations[restartedAtAnnotation] = stamp
	return ""
}
