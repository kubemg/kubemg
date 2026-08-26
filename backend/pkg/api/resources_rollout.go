package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * A Helm release can be read, its values edited, its history listed and a
 * revision rolled back — and the *native* workload Helm installs onto has none
 * of that, even though the cluster already keeps its history. kube-controller-
 * manager numbers a Deployment's revisions by keeping one ReplicaSet per
 * revision (`deployment.kubernetes.io/revision`), and a StatefulSet or
 * DaemonSet keeps a ControllerRevision per revision the same way. So the history
 * route is that owned-ReplicaSet/ControllerRevision list, decoded and ordered
 * newest first — `kubectl rollout history` — and the rollback route is the
 * read-modify-write the scale/restart/suspend actions already are: the target
 * revision's pod template is written back onto the live object with its
 * resourceVersion, so a concurrent change is the API server's own 409 rather
 * than a silent overwrite, and the controller does the rollout from there.
 *
 * Ownership is resolved from `metadata.ownerReferences` plus the workload's own
 * `spec.selector` — never from a caller-supplied selector, for
 * resources_workload_logs.go's reason exactly: a caller-supplied selector turns
 * this into a general query that can reach objects the workload does not own.
 * The selector narrows the list read; the ownerReferences UID match is what
 * actually decides ownership, so a foreign ReplicaSet that happens to carry the
 * same labels is never mistaken for one of this Deployment's own.
 *
 * A ReplicaSet's revision is a real pod count; a ControllerRevision is not —
 * it is a strategic-merge patch of the object's pod template and nothing runs
 * against it directly — so `replicas`/`ready` are reported for the Deployment
 * shape and omitted for the StatefulSet/DaemonSet one, and a rollback for
 * either applies that revision's template onto the live object rather than
 * treating the two shapes as if they were the same thing with different names.
 */

// deploymentRevisionAnnotation is where the deployment controller writes both
// a ReplicaSet's revision number and the Deployment's own currently active
// one — the same annotation on both objects, which is what makes "revision 3
// is current" answerable without hashing a pod template by hand.
const deploymentRevisionAnnotation = "deployment.kubernetes.io/revision"

// changeCauseAnnotation is `kubectl --record`'s note of what a revision was
// for. It is optional everywhere it can appear, so its absence is reported as
// an empty string rather than as a gap.
const changeCauseAnnotation = "kubernetes.io/change-cause"

// podTemplateHashLabel is the label the deployment/statefulset/daemonset
// controllers stamp onto the pod template *they* generated. A rollback must
// never carry an old revision's copy of it forward: telling the controller a
// generation already has this hash when none of its pods do is how a rollout
// fails to converge rather than how it succeeds.
const podTemplateHashLabel = "pod-template-hash"

// revisionStyle is how one workload kind's revisions are stored. The two
// shapes are genuinely different objects with genuinely different fields, not
// one shape wearing two names.
type revisionStyle int

const (
	// replicaSetRevisions is the Deployment shape: one ReplicaSet per revision,
	// each a real, scalable object with its own pod count.
	replicaSetRevisions revisionStyle = iota
	// controllerRevisionRevisions is the StatefulSet/DaemonSet shape: a
	// ControllerRevision holding a strategic-merge patch of the pod template,
	// nothing more.
	controllerRevisionRevisions
)

// workloadRevisionKind is a workload kind this file keeps history for. It is
// its own table rather than an extension of workloadActions, because the
// question here — where a revision is stored — has nothing to do with whether
// a workload scales or restarts.
type workloadRevisionKind struct {
	workloadPath resourceListPath
	ownedPath    resourceListPath
	// kind is the name reported back, and also the owner kind every owned
	// object's ownerReferences must name to count as this workload's own.
	kind  string
	style revisionStyle
}

var workloadRevisionKinds = map[string]workloadRevisionKind{
	"deployments": {
		workloadPath: resourceListPath{"/apis/apps/v1", "deployments"},
		ownedPath:    resourceListPath{"/apis/apps/v1", "replicasets"},
		kind:         "Deployment",
		style:        replicaSetRevisions,
	},
	"statefulsets": {
		workloadPath: resourceListPath{"/apis/apps/v1", "statefulsets"},
		ownedPath:    resourceListPath{"/apis/apps/v1", "controllerrevisions"},
		kind:         "StatefulSet",
		style:        controllerRevisionRevisions,
	},
	"daemonsets": {
		workloadPath: resourceListPath{"/apis/apps/v1", "daemonsets"},
		ownedPath:    resourceListPath{"/apis/apps/v1", "controllerrevisions"},
		kind:         "DaemonSet",
		style:        controllerRevisionRevisions,
	},
}

// rolloutRevision is one entry in a workload's history.
type rolloutRevision struct {
	Revision    int       `json:"revision"`
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"created_at"`
	Images      []string  `json:"images"`
	ChangeCause string    `json:"change_cause"`
	// Replicas and Ready are only ever set for the ReplicaSet shape — a
	// ControllerRevision carries no pod count of its own, and reporting one
	// would be inventing a number nothing on the cluster holds.
	Replicas *int32 `json:"replicas,omitempty"`
	Ready    *int32 `json:"ready,omitempty"`
	Current  bool   `json:"current"`
}

/* -------------------------------------------------------------- reading --- */

// showWorkloadHistory answers `kubectl rollout history`.
func (s *server) showWorkloadHistory(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	key := strings.TrimSpace(c.Query("kind"))
	revKind, known := workloadRevisionKinds[key]
	if !known {
		c.JSON(http.StatusConflict, gin.H{"error": "kubemg keeps no rollout history for " + key})
		return
	}

	name := strings.TrimSpace(c.Query("name"))
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a resource name is required"})
		return
	}

	namespace, ok := s.resourceNamespace(c, grant)
	if !ok {
		return
	}

	revisions, _, _, ok := s.workloadRevisions(c, user, cluster, grant, revKind, namespace, name)
	if !ok {
		return
	}

	listResponse(c, gin.H{
		"kind":      revKind.kind,
		"name":      name,
		"namespace": namespace,
		"revisions": revisions,
		"truncated": false,
	})
}

// workloadRevisions resolves a workload to the revisions it owns: the live
// object (needed again by a rollback, so it is handed back rather than
// re-read), the raw owned objects (same reason) and the rendered history.
func (s *server) workloadRevisions(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, revKind workloadRevisionKind, namespace, name string,
) (revisions []rolloutRevision, live map[string]any, owned []map[string]any, ok bool) {
	kind := objectKind{versions: []resourceListPath{revKind.workloadPath}, namespaced: true}
	body, readOK := s.readObject(c, user, cluster, grant, kind, namespace, name)
	if !readOK {
		return nil, nil, nil, false
	}

	if err := json.Unmarshal(body, &live); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "the cluster returned an unreadable response"})
		return nil, nil, nil, false
	}

	owned, ok = s.ownedRevisionsOf(c, user, cluster, grant, revKind, namespace, live)
	if !ok {
		return nil, live, nil, false
	}

	return buildRolloutRevisions(revKind, live, owned), live, owned, true
}

// ownedRevisionsOf lists the workload's revision objects, narrowed to the ones
// its own ownerReferences name. The label selector is read off the workload —
// never taken from a caller — purely to keep the list read from pulling every
// ReplicaSet or ControllerRevision in the namespace; ownership itself is
// decided by ownerReferences, so a foreign object sharing those labels does
// not survive the filter that follows.
func (s *server) ownedRevisionsOf(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, revKind workloadRevisionKind, namespace string, live map[string]any,
) ([]map[string]any, bool) {
	selector, err := workloadSelectorFrom(live)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return nil, false
	}

	query := url.Values{}
	query.Set("labelSelector", selector)
	listPath := fmt.Sprintf("%s?%s", revKind.ownedPath.namespaced(namespace), query.Encode())

	var items []map[string]any
	if !fetchList(s, c, user, cluster, grant, listPath, &items) {
		return nil, false
	}

	uid := stringField(live, "metadata", "uid")
	return ownedRevisionObjects(uid, revKind.kind, items), true
}

// workloadSelectorFrom reads a workload's own pod selector and renders it the
// way the API server's labelSelector query parameter takes it, reusing
// encodeLabelSelector rather than inventing a second renderer that could
// disagree with the one resources_workload_logs.go already pins.
func workloadSelectorFrom(live map[string]any) (string, error) {
	raw, ok := mapField(live, "spec")["selector"].(map[string]any)
	if !ok || len(raw) == 0 {
		return "", fmt.Errorf(
			"this workload declares no pod selector, so kubemg cannot tell which revisions are its own")
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("this workload's selector could not be read")
	}
	var selector labelSelector
	if err := json.Unmarshal(data, &selector); err != nil {
		return "", fmt.Errorf("this workload's selector could not be read")
	}
	return encodeLabelSelector(selector)
}

// ownedRevisionObjects picks the items whose ownerReferences name this
// workload's own kind and UID — the jobsOwnedByCronJob rule exactly, and for
// the same reason: a label match alone is not proof of ownership, only
// ownerReferences is.
func ownedRevisionObjects(parentUID, parentKind string, items []map[string]any) []map[string]any {
	var out []map[string]any
	if parentUID == "" {
		return out
	}
	for _, item := range items {
		metadata, _ := item["metadata"].(map[string]any)
		if metadata == nil {
			continue
		}
		refs, _ := metadata["ownerReferences"].([]any)
		for _, ref := range refs {
			owner, ok := ref.(map[string]any)
			if !ok {
				continue
			}
			kind, _ := owner["kind"].(string)
			uid, _ := owner["uid"].(string)
			if kind == parentKind && uid == parentUID {
				out = append(out, item)
				break
			}
		}
	}
	return out
}

// buildRolloutRevisions renders the owned objects as history, newest first,
// with exactly one marked current.
func buildRolloutRevisions(revKind workloadRevisionKind, live map[string]any, owned []map[string]any) []rolloutRevision {
	currentNumber, currentName := currentRevisionOf(revKind.style, live)

	out := make([]rolloutRevision, 0, len(owned))
	for _, item := range owned {
		number, ok := revisionNumberOf(revKind.style, item)
		if !ok {
			// An object the selector matched but that carries no revision at
			// all is not one to guess a number for.
			continue
		}
		rev := rolloutRevision{
			Revision:    number,
			Name:        stringField(item, "metadata", "name"),
			CreatedAt:   creationTimestampOf(item),
			Images:      imagesOf(revKind.style, item),
			ChangeCause: changeCauseOf(revKind.style, item),
		}
		if revKind.style == replicaSetRevisions {
			rev.Replicas = int32Field(item, "spec", "replicas")
			rev.Ready = int32Field(item, "status", "readyReplicas")
		}
		switch {
		case currentName != "":
			rev.Current = rev.Name == currentName
		case currentNumber != 0:
			rev.Current = number == currentNumber
		}
		out = append(out, rev)
	}

	slices.SortFunc(out, func(a, b rolloutRevision) int { return b.Revision - a.Revision })

	// Neither a revision annotation nor a status name resolved to anything —
	// a DaemonSet has no status field naming one at all — so the newest
	// revision is the one currently running, because both controllers only
	// ever move forward: a rollback appends a new, higher-numbered revision
	// rather than reusing an old one.
	if currentName == "" && currentNumber == 0 && len(out) > 0 &&
		!slices.ContainsFunc(out, func(r rolloutRevision) bool { return r.Current }) {
		out[0].Current = true
	}
	return out
}

// currentRevisionOf reads which revision a live workload is running. A
// Deployment and its current ReplicaSet share the exact same annotation, so
// the number is read straight off the Deployment rather than hashed from its
// pod template. A StatefulSet names its current ControllerRevision by name in
// its own status; a DaemonSet's status carries neither, which
// buildRolloutRevisions' fallback covers.
func currentRevisionOf(style revisionStyle, live map[string]any) (number int, name string) {
	if style == replicaSetRevisions {
		v := stringField(live, "metadata", "annotations", deploymentRevisionAnnotation)
		n, _ := strconv.Atoi(v)
		return n, ""
	}
	return 0, stringField(live, "status", "currentRevision")
}

// revisionNumberOf reads the number a revision is addressed by.
func revisionNumberOf(style revisionStyle, item map[string]any) (int, bool) {
	if style == replicaSetRevisions {
		v := stringField(item, "metadata", "annotations", deploymentRevisionAnnotation)
		if v == "" {
			return 0, false
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	n, ok := float64Field(item, "revision")
	if !ok {
		return 0, false
	}
	return int(n), true
}

// changeCauseOf reads `kubectl --record`'s note, checked in every place it is
// known to land: the object's own annotations first, then the pod template's
// — kubectl has written it in both places over the product's lifetime.
func changeCauseOf(style revisionStyle, item map[string]any) string {
	if v := stringField(item, "metadata", "annotations", changeCauseAnnotation); v != "" {
		return v
	}
	if style == replicaSetRevisions {
		return stringField(item, "spec", "template", "metadata", "annotations", changeCauseAnnotation)
	}
	if template, ok := controllerRevisionTemplate(item); ok {
		return stringField(template, "metadata", "annotations", changeCauseAnnotation)
	}
	return ""
}

// imagesOf reads the container images a revision's pod template names.
func imagesOf(style revisionStyle, item map[string]any) []string {
	var template map[string]any
	if style == replicaSetRevisions {
		template = mapField(item, "spec", "template")
	} else {
		template, _ = controllerRevisionTemplate(item)
	}

	images := []string{}
	if template == nil {
		return images
	}
	containers, _ := mapField(template, "spec")["containers"].([]any)
	for _, entry := range containers {
		container, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if image, ok := container["image"].(string); ok && image != "" {
			images = append(images, image)
		}
	}
	return images
}

// controllerRevisionTemplate reads the pod template out of a
// ControllerRevision's `data`. Both the StatefulSet and DaemonSet controllers
// build that field as a strategic-merge patch holding exactly one thing —
// `{"spec":{"template": ...}}` — which is what lets a rollback treat it as the
// template to restore rather than as a patch needing a merge engine.
func controllerRevisionTemplate(item map[string]any) (map[string]any, bool) {
	template := mapField(item, "data", "spec", "template")
	return template, template != nil
}

// creationTimestampOf parses metadata.creationTimestamp. A zero time is what
// this reports for a malformed one, rather than guessing at a date.
func creationTimestampOf(item map[string]any) time.Time {
	t, _ := time.Parse(time.RFC3339, stringField(item, "metadata", "creationTimestamp"))
	return t
}

/* -------------------------------------------------------------- writing --- */

// rolloutRollbackRequest is what the rollback route accepts. Kind is the
// sidebar's own key, the same address as every other resource route.
type rolloutRollbackRequest struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Revision  int    `json:"revision"`
}

// rollbackWorkload restores an earlier revision's pod template onto the live
// object — `kubectl rollout undo`, and the same read-modify-write the other
// workload actions already are. The revision is resolved against what the
// cluster just returned rather than turned into an object name, so the only
// revisions reachable are the ones that exist right now.
func (s *server) rollbackWorkload(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	var req rolloutRollbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the request could not be read"})
		return
	}

	key := strings.TrimSpace(req.Kind)
	revKind, known := workloadRevisionKinds[key]
	if !known {
		c.JSON(http.StatusConflict, gin.H{"error": "kubemg does not roll back " + key})
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a resource name is required"})
		return
	}
	if req.Revision < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name the revision to roll back to"})
		return
	}

	namespace, ok := s.scopedNamespace(c, grant, req.Namespace)
	if !ok {
		return
	}

	revisions, live, owned, ok := s.workloadRevisions(c, user, cluster, grant, revKind, namespace, req.Name)
	if !ok {
		return
	}

	target, status, message := resolveRollbackTarget(revisions, owned, revKind.style, req.Revision)
	if status != 0 {
		c.JSON(status, gin.H{"error": fmt.Sprintf("%s %s", req.Name, message)})
		return
	}

	if reason := applyRolloutTarget(revKind.style, live, target); reason != "" {
		c.JSON(http.StatusConflict, gin.H{"error": reason})
		return
	}
	stripManagedFields(live)

	doc, err := json.Marshal(live)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the rollback request could not be encoded"})
		return
	}

	path := revKind.workloadPath.namespaced(namespace) + "/" + url.PathEscape(req.Name)
	resp, callOK := s.callResourceWith(c, user, cluster, grant,
		http.MethodPut, path, doc, "could not write to the cluster")
	if !callOK {
		return
	}
	if resp.Status < 200 || resp.Status >= 300 {
		c.JSON(resp.Status, gin.H{"error": kubeErrorMessage(resp.Body, resp.Status)})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":   fmt.Sprintf("%s rolled back to revision %d", req.Name, req.Revision),
		"revision":  req.Revision,
		"kind":      req.Kind,
		"name":      req.Name,
		"namespace": namespace,
	})
}

// findOwnedRevision picks the owned object addressed by a revision number.
func findOwnedRevision(style revisionStyle, owned []map[string]any, revision int) map[string]any {
	for _, item := range owned {
		if number, ok := revisionNumberOf(style, item); ok && number == revision {
			return item
		}
	}
	return nil
}

// resolveRollbackTarget decides what a rollback request resolves to, given the
// history a fresh read already produced: the revision object to write back,
// or the exact refusal a caller sees. It is a pure function over that
// history rather than inline in the handler so the two refusals — a revision
// that does not exist, and one that is already current — are testable without
// a cluster behind them.
func resolveRollbackTarget(revisions []rolloutRevision, owned []map[string]any,
	style revisionStyle, revision int,
) (target map[string]any, status int, message string) {
	index := slices.IndexFunc(revisions, func(r rolloutRevision) bool { return r.Revision == revision })
	if index < 0 {
		return nil, http.StatusNotFound,
			fmt.Sprintf("has no revision %d — it may have been pruned", revision)
	}
	if revisions[index].Current {
		// Not an error the cluster would raise, but writing back the revision
		// already running is a write that changes nothing and hides that it
		// did — the same rule the Helm rollback follows.
		return nil, http.StatusConflict, fmt.Sprintf("is already running revision %d", revision)
	}

	target = findOwnedRevision(style, owned, revision)
	if target == nil {
		return nil, http.StatusNotFound,
			fmt.Sprintf("has no revision %d — it may have been pruned", revision)
	}
	return target, 0, ""
}

// applyRolloutTarget writes a revision's pod template onto the live object,
// stripping the pod-template-hash label the controller owns. It changes one
// field and nothing else, the same discipline stampRestart and stampSuspend
// hold to for their own one field.
func applyRolloutTarget(style revisionStyle, live map[string]any, target map[string]any) string {
	var template map[string]any
	if style == replicaSetRevisions {
		template = mapField(target, "spec", "template")
	} else {
		template, _ = controllerRevisionTemplate(target)
	}
	if template == nil {
		return "that revision carries no pod template to restore"
	}

	spec, _ := live["spec"].(map[string]any)
	if spec == nil {
		return "the cluster returned a workload with no spec"
	}

	restored := deepCopyMap(template)
	stripPodTemplateHash(restored)
	spec["template"] = restored
	return ""
}

// stripPodTemplateHash removes the label a controller stamps onto the
// generation it manages. A restored template carrying an old generation's
// copy of it would tell the controller a generation already exists that none
// of its pods do, and the rollout would never converge.
func stripPodTemplateHash(template map[string]any) {
	metadata, _ := template["metadata"].(map[string]any)
	if metadata == nil {
		return
	}
	labels, _ := metadata["labels"].(map[string]any)
	if labels == nil {
		return
	}
	delete(labels, podTemplateHashLabel)
	if len(labels) == 0 {
		delete(metadata, "labels")
	}
}

// deepCopyMap clones a decoded JSON value through a round trip, so mutating
// the result (stripping a label) never reaches back into the value it was
// read from.
func deepCopyMap(v map[string]any) map[string]any {
	data, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return v
	}
	return out
}

/* --------------------------------------------------------- map helpers --- */

// mapField walks a chain of map keys, stopping and returning nil the moment
// any link is missing or is not itself a map. It is the one place this file
// reads a nested field out of a decoded object, so a short-circuiting nil is
// never a nil pointer somewhere else.
func mapField(v map[string]any, path ...string) map[string]any {
	cur := v
	for _, key := range path {
		next, ok := cur[key].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

// stringField reads a string at the end of a field path, or "" for a missing
// or wrongly-typed one — a value nobody has written is not a value to guess.
func stringField(v map[string]any, path ...string) string {
	if len(path) == 0 {
		return ""
	}
	parent := mapField(v, path[:len(path)-1]...)
	if parent == nil {
		return ""
	}
	s, _ := parent[path[len(path)-1]].(string)
	return s
}

// float64Field reads the JSON number at the end of a field path — every
// number in a decoded map[string]any is a float64, ControllerRevision's own
// `revision` field included.
func float64Field(v map[string]any, path ...string) (float64, bool) {
	if len(path) == 0 {
		return 0, false
	}
	parent := mapField(v, path[:len(path)-1]...)
	if parent == nil {
		return 0, false
	}
	n, ok := parent[path[len(path)-1]].(float64)
	return n, ok
}

// int32Field reads a field as *int32, nil when it is absent or not a number —
// the pointer is what lets a ReplicaSet's real zero read differently from a
// ControllerRevision that has no such field at all.
func int32Field(v map[string]any, path ...string) *int32 {
	n, ok := float64Field(v, path...)
	if !ok {
		return nil
	}
	value := int32(n)
	return &value
}
