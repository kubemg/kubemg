package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

/*
 * The pod that has no shell: `kubectl exec` needs a shell in the target
 * container to attach a terminal to, and a distroless or scratch image has
 * none. `kubectl debug` solves this by writing a second, throwaway container
 * onto the running pod — sharing the target container's process namespace —
 * and exec'ing into that instead. This is the same trick, in the same shape
 * every other workload write here already uses: a read-modify-write onto the
 * pod's own ephemeralcontainers subresource, conditional on the
 * resourceVersion the read returned, so a concurrent change is the API
 * server's own 409 rather than a blind overwrite.
 *
 * It is a new container, not a new permission — the write goes down the same
 * impersonated, audited tunnel as scaleWorkload and the others, and a caller
 * without the RBAC to update a pod's ephemeralcontainers subresource is
 * refused by the cluster itself.
 *
 * An ephemeral container cannot be removed once it exists: the API server has
 * no delete for it, and it shares the target's process (and from there,
 * network and IPC) namespace for as long as the pod lives. Both facts are the
 * console's to disclose before this route is ever called — see the debug
 * Sheet — not this handler's, which only does the write it was asked for.
 */

// podResourcePath is the fixed API path for a Pod, the same shape
// workloadAction.path is for a workload.
var podResourcePath = resourceListPath{"/api/v1", "pods"}

// podObjectPath renders the address of one pod.
func podObjectPath(namespace, name string) string {
	return podResourcePath.namespaced(namespace) + "/" + url.PathEscape(name)
}

// debugContainerRequest is what the Debug action accepts.
type debugContainerRequest struct {
	Pod       string `json:"pod"`
	Namespace string `json:"namespace"`
	// Container is the pod's own container to share a process namespace
	// with — Kubernetes' targetContainerName. It is not the name of the
	// container this request creates.
	Container string `json:"container"`
}

// debugContainerResult is what comes back: enough for the console to open a
// terminal against the container it just created without asking again.
type debugContainerResult struct {
	Pod       string `json:"pod"`
	Namespace string `json:"namespace"`
	// Container is the generated name of the ephemeral container itself. The
	// exec that follows addresses this, never TargetContainer, so a debug
	// session can never land in the application container by accident.
	Container string `json:"container"`
	// TargetContainer is the existing container whose namespaces are shared.
	TargetContainer string `json:"target_container"`
	Image           string `json:"image"`
	Message         string `json:"message"`
}

// debugPodContainer adds an ephemeral debug container to a running pod.
func (s *server) debugPodContainer(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	var req debugContainerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the request could not be read"})
		return
	}
	req.Pod = strings.TrimSpace(req.Pod)
	if req.Pod == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a pod name is required"})
		return
	}
	req.Container = strings.TrimSpace(req.Container)
	if req.Container == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a target container is required"})
		return
	}

	namespace, ok := s.scopedNamespace(c, grant, req.Namespace)
	if !ok {
		return
	}
	req.Namespace = namespace

	image := strings.TrimSpace(s.settings(c.Request.Context()).DebugImage)
	if image == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "no debug image is configured on this server"})
		return
	}

	path := podObjectPath(req.Namespace, req.Pod)

	// Read the pod first: it settles that it exists, that the chosen target is
	// really one of its containers, and its resourceVersion is what makes the
	// write conditional.
	resp, callOK := s.callResource(c, user, cluster, grant, path)
	if !callOK {
		return
	}
	var pod map[string]any
	if !s.decodeResource(c, resp, &pod) {
		return
	}

	spec, _ := pod["spec"].(map[string]any)
	if spec == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "the cluster returned a pod with no spec"})
		return
	}
	if !hasContainerNamed(spec, req.Container) {
		c.JSON(http.StatusBadRequest, gin.H{"error": req.Pod + " has no container named " + req.Container})
		return
	}

	// Short and unique per request: two debug sessions against the same pod
	// are two containers, never a name to collide on.
	name := "debug-" + uuid.NewString()[:8]

	existing, _ := spec["ephemeralContainers"].([]any)
	spec["ephemeralContainers"] = append(existing, map[string]any{
		"name":                name,
		"image":               image,
		"targetContainerName": req.Container,
		"stdin":               true,
		"tty":                 true,
	})
	stripManagedFields(pod)

	body, err := json.Marshal(pod)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "the debug request could not be encoded"})
		return
	}

	resp, callOK = s.callResourceWith(c, user, cluster, grant,
		http.MethodPut, path+"/ephemeralcontainers", body, "could not write to the cluster")
	if !callOK {
		return
	}
	if resp.Status < 200 || resp.Status >= 300 {
		c.JSON(resp.Status, gin.H{"error": kubeErrorMessage(resp.Body, resp.Status)})
		return
	}

	c.JSON(http.StatusOK, debugContainerResult{
		Pod:             req.Pod,
		Namespace:       req.Namespace,
		Container:       name,
		TargetContainer: req.Container,
		Image:           image,
		Message:         name + " is starting on " + req.Pod + " — connecting once it is running",
	})
}

// hasContainerNamed reports whether a pod spec already names the given
// container among its own — deliberately not initContainers or an existing
// ephemeral container, since debugging a running pod means the workload
// container running in it right now.
func hasContainerNamed(spec map[string]any, name string) bool {
	containers, _ := spec["containers"].([]any)
	for _, entry := range containers {
		container, _ := entry.(map[string]any)
		if container == nil {
			continue
		}
		if containerName, _ := container["name"].(string); containerName == name {
			return true
		}
	}
	return false
}
