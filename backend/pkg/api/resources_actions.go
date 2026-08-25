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
 * The workload controls an operator reaches for before reaching for a manifest:
 * change how many replicas there are, roll the pods, and stop a schedule from
 * firing.
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
 * the same reason the manifest editor does. Suspend is the same shape as
 * restart: `spec.suspend` is a field on the object, so the object is what is
 * written back.
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
	// suspendable is the CronJob's own control and nothing else's. A schedule
	// is the one workload property with an off switch that is not a replica
	// count: scaling a CronJob is meaningless (it has no pods of its own) and
	// deleting it to stop it loses the object, so `spec.suspend` is what an
	// operator reaches for when a nightly job has to stop firing tonight.
	suspendable bool
}

var workloadActions = map[string]workloadAction{
	"deployments":  {path: resourceListPath{"/apis/apps/v1", "deployments"}, scalable: true, restartable: true},
	"statefulsets": {path: resourceListPath{"/apis/apps/v1", "statefulsets"}, scalable: true, restartable: true},
	"daemonsets":   {path: resourceListPath{"/apis/apps/v1", "daemonsets"}, restartable: true},
	"replicasets":  {path: resourceListPath{"/apis/apps/v1", "replicasets"}, scalable: true},
	"cronjobs":     {path: resourceListPath{"/apis/batch/v1", "cronjobs"}, suspendable: true},
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
	// Suspend is a pointer for the same reason: false is the request to resume
	// a suspended schedule, and an absent field is neither.
	Suspend *bool `json:"suspend"`
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
	// Suspended is the state a schedule was left in — reported rather than
	// echoed, because a request to suspend something already suspended is
	// answered by saying so instead of by writing the object again.
	Suspended *bool  `json:"suspended,omitempty"`
	Message   string `json:"message"`
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

// suspendWorkload turns a schedule off or back on. It is the CronJob's own
// control and the only one it has: a CronJob owns Jobs rather than pods, so
// there is nothing to scale and nothing to roll, and the way an operator stops
// tonight's run without losing the object is `spec.suspend`.
//
// A request for the state the object is already in is answered rather than
// written. That matters here more than it would elsewhere because this is the
// one action reached over a whole selection: resuming eight CronJobs of which
// six are already running should be two writes and six sentences, not eight
// writes and eight audit records saying nothing happened.
func (s *server) suspendWorkload(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	var req workloadActionRequest
	action, ok := s.workloadTarget(c, grant, &req)
	if !ok {
		return
	}
	if !action.suspendable {
		c.JSON(http.StatusConflict, gin.H{
			"error": "a " + strings.TrimSuffix(req.Kind, "s") + " has no schedule to suspend",
		})
		return
	}
	if req.Suspend == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "say whether to suspend or resume"})
		return
	}
	suspend := *req.Suspend

	path := action.objectPath(req.Namespace, req.Name)

	resp, callOK := s.callResource(c, user, cluster, grant, path)
	if !callOK {
		return
	}
	var object map[string]any
	if !s.decodeResource(c, resp, &object) {
		return
	}

	if current, reason := suspendState(object); reason != "" {
		c.JSON(http.StatusConflict, gin.H{"error": reason})
		return
	} else if current == suspend {
		c.JSON(http.StatusOK, workloadActionResult{
			Kind:      req.Kind,
			Name:      req.Name,
			Namespace: req.Namespace,
			Suspended: &suspend,
			Message:   req.Name + alreadySuspended(suspend),
		})
		return
	}

	if reason := stampSuspend(object, suspend); reason != "" {
		c.JSON(http.StatusConflict, gin.H{"error": reason})
		return
	}
	stripManagedFields(object)

	body, err := json.Marshal(object)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the suspend request could not be encoded"})
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
		Suspended: &suspend,
		Message:   suspendedMessage(req.Name, suspend),
	})
}

// suspendState reads whether a schedule is currently suspended. An absent field
// is the API server's default and means running; a field of the wrong type is
// not something to guess about, for the same reason a missing pod template is
// not — the write that follows replaces the object.
func suspendState(object map[string]any) (bool, string) {
	spec, _ := object["spec"].(map[string]any)
	if spec == nil {
		return false, "the cluster returned a cronjob with no spec"
	}
	switch value := spec["suspend"].(type) {
	case nil:
		return false, ""
	case bool:
		return value, ""
	default:
		return false, "the cluster returned a cronjob whose suspend field is not a boolean"
	}
}

// stampSuspend writes the schedule's off switch. It changes one field and
// nothing else, which is the whole reason this route exists rather than an
// operator editing the manifest.
func stampSuspend(object map[string]any, suspend bool) string {
	spec, _ := object["spec"].(map[string]any)
	if spec == nil {
		return "the cluster returned a cronjob with no spec"
	}
	spec["suspend"] = suspend
	return ""
}

// alreadySuspended is what a no-op answers with. It reads as a sentence about
// the object rather than as an error, because it is not one.
func alreadySuspended(suspend bool) string {
	if suspend {
		return " is already suspended"
	}
	return " is already running its schedule"
}

// suspendedMessage says what happened in the words an operator would use.
func suspendedMessage(name string, suspend bool) string {
	if suspend {
		return name + " suspended — it will not fire again until it is resumed"
	}
	return name + " resumed — its schedule fires again"
}
