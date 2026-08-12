package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// Resource reads are normalised here rather than in the browser: the UI should
// not have to know the shape of six Kubernetes list types, and normalising
// server-side keeps the payload small over a slow link.

type namespaceView struct {
	Name    string    `json:"name"`
	Status  string    `json:"status"`
	Created time.Time `json:"created_at"`
	// Granted marks a namespace the caller's grant actually covers. An
	// unscoped grant marks them all.
	Granted bool `json:"granted"`
}

type workloadView struct {
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	Ready     int32     `json:"ready"`
	Desired   int32     `json:"desired"`
	Images    []string  `json:"images"`
	Created   time.Time `json:"created_at"`
}

type containerView struct {
	Name     string `json:"name"`
	Image    string `json:"image"`
	Ready    bool   `json:"ready"`
	Restarts int32  `json:"restarts"`
	State    string `json:"state"`
	// The container's own resource contract, normalised into the same units as
	// the metrics endpoints. Live usage means very little on its own — the
	// question is always "how close to its limit" — so the denominator travels
	// with the container rather than being looked up separately. Zero means the
	// container declares none, which is a real and common answer.
	CPURequestMillicores int64 `json:"cpu_request_millicores"`
	CPULimitMillicores   int64 `json:"cpu_limit_millicores"`
	MemoryRequestBytes   int64 `json:"memory_request_bytes"`
	MemoryLimitBytes     int64 `json:"memory_limit_bytes"`
}

type podView struct {
	Name       string          `json:"name"`
	Namespace  string          `json:"namespace"`
	Phase      string          `json:"phase"`
	Node       string          `json:"node"`
	PodIP      string          `json:"pod_ip,omitempty"`
	Ready      int             `json:"ready"`
	Total      int             `json:"total"`
	Restarts   int32           `json:"restarts"`
	Created    time.Time       `json:"created_at"`
	Containers []containerView `json:"containers"`
}

// resourceCluster resolves the cluster and the caller's grant for a resource
// read, refusing anything that is not reachable through an agent tunnel.
func (s *server) resourceCluster(c *gin.Context) (*db.User, *db.Cluster, db.UserClusterAccess, bool) {
	var noGrant db.UserClusterAccess

	user, cluster, grant, _, ok := s.loadAuthorizedCluster(c)
	if !ok {
		return nil, nil, noGrant, false
	}
	if connectionMode(*cluster) != db.ModeAgent {
		c.JSON(http.StatusConflict, gin.H{
			"error": "live resources are only available for agent-based clusters",
		})
		return nil, nil, noGrant, false
	}
	// An admin has no stored grant; give them the cluster-admin identity the
	// rest of KubeMG already assumes for them.
	if user.IsAdmin() {
		grant = db.UserClusterAccess{
			UserID:    user.ID,
			ClusterID: cluster.ID,
			K8sRole:   db.K8sRoleClusterAdmin,
		}
	}
	return user, cluster, grant, true
}

// callResource performs a proxied GET, writing a transport failure or a refusal
// from the bastion itself to the client. The cluster's own response is handed
// back untouched, so a caller can decide what a given status means to it.
func (s *server) callResource(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, path string,
) (*bastion.Response, bool) {
	return s.callResourceWith(c, user, cluster, grant,
		http.MethodGet, path, nil, "could not read from the cluster")
}

// decodeResource turns a successful cluster response into a Go value, and any
// other status into the HTTP response.
func (s *server) decodeResource(c *gin.Context, resp *bastion.Response, out any) bool {
	if resp.Status < 200 || resp.Status >= 300 {
		// Hand the API server's own explanation back: "forbidden: pods is
		// forbidden for user X" is far more useful than anything we'd invent.
		c.JSON(resp.Status, gin.H{"error": kubeErrorMessage(resp.Body, resp.Status)})
		return false
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "the cluster returned an unreadable response"})
		return false
	}
	return true
}

// fetch performs a proxied GET and decodes it, translating a proxy refusal into
// the HTTP response itself.
func (s *server) fetch(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, path string, out any,
) bool {
	resp, ok := s.callResource(c, user, cluster, grant, path)
	if !ok {
		return false
	}
	return s.decodeResource(c, resp, out)
}

// listNamespaces returns the namespaces the caller may see. A namespace-scoped
// grant never triggers a cluster-wide list: the scope is answered from the
// grant itself, so a "view on team-a" user cannot enumerate the cluster.
func (s *server) listNamespaces(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}

	scoped := grant.NamespaceList()
	if len(scoped) > 0 {
		out := make([]namespaceView, 0, len(scoped))
		for _, name := range scoped {
			out = append(out, namespaceView{Name: name, Status: "Granted", Granted: true})
		}
		c.JSON(http.StatusOK, gin.H{"namespaces": out, "scoped": true})
		return
	}

	var list struct {
		Items []struct {
			Metadata struct {
				Name              string    `json:"name"`
				CreationTimestamp time.Time `json:"creationTimestamp"`
			} `json:"metadata"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if !s.fetch(c, user, cluster, grant, "/api/v1/namespaces", &list) {
		return
	}

	out := make([]namespaceView, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, namespaceView{
			Name:    item.Metadata.Name,
			Status:  item.Status.Phase,
			Created: item.Metadata.CreationTimestamp,
			Granted: true,
		})
	}
	slices.SortFunc(out, func(a, b namespaceView) int { return strings.Compare(a.Name, b.Name) })
	c.JSON(http.StatusOK, gin.H{"namespaces": out, "scoped": false})
}

// workloadKinds are the apps/v1 kinds that share the ready/desired shape. The
// sidebar lists them one at a time; the combined workloads route returns all of
// them.
var workloadKinds = []struct {
	kind     string
	resource string
}{
	{"Deployment", "deployments"},
	{"StatefulSet", "statefulsets"},
	{"DaemonSet", "daemonsets"},
}

// listWorkloads returns deployments, statefulsets and daemonsets in one shape.
func (s *server) listWorkloads(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out, ok := s.collectWorkloads(c, user, cluster, grant, scope, "")
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"workloads":      out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
	})
}

// collectWorkloads reads the apps/v1 kinds into one list. An empty kind reads
// all of them; naming one reads just that kind, which is what the per-kind
// routes want.
func (s *server) collectWorkloads(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, scope readScope, only string,
) ([]workloadView, bool) {
	out := []workloadView{}
	for _, kind := range workloadKinds {
		if only != "" && kind.kind != only {
			continue
		}
		for _, path := range scope.paths(resourceListPath{"/apis/apps/v1", kind.resource}) {
			var list struct {
				Items []struct {
					Metadata struct {
						Name              string    `json:"name"`
						Namespace         string    `json:"namespace"`
						CreationTimestamp time.Time `json:"creationTimestamp"`
					} `json:"metadata"`
					Spec struct {
						Replicas *int32 `json:"replicas"`
						Template struct {
							Spec struct {
								Containers []struct {
									Image string `json:"image"`
								} `json:"containers"`
							} `json:"spec"`
						} `json:"template"`
					} `json:"spec"`
					Status struct {
						ReadyReplicas          int32 `json:"readyReplicas"`
						Replicas               int32 `json:"replicas"`
						NumberReady            int32 `json:"numberReady"`
						DesiredNumberScheduled int32 `json:"desiredNumberScheduled"`
					} `json:"status"`
				} `json:"items"`
			}

			if !s.fetch(c, user, cluster, grant, path, &list) {
				return nil, false
			}

			for _, item := range list.Items {
				view := workloadView{
					Kind:      kind.kind,
					Name:      item.Metadata.Name,
					Namespace: item.Metadata.Namespace,
					Created:   item.Metadata.CreationTimestamp,
				}
				// A DaemonSet has no replica count; its scale is how many nodes
				// it is meant to be on.
				if kind.kind == "DaemonSet" {
					view.Ready = item.Status.NumberReady
					view.Desired = item.Status.DesiredNumberScheduled
				} else {
					view.Ready = item.Status.ReadyReplicas
					view.Desired = item.Status.Replicas
					if item.Spec.Replicas != nil {
						view.Desired = *item.Spec.Replicas
					}
				}
				for _, container := range item.Spec.Template.Spec.Containers {
					view.Images = append(view.Images, container.Image)
				}
				out = append(out, view)
			}
		}
	}

	sortResources(out)
	return out, true
}

// listPods returns the pods a scope covers, flattened to what a list view needs.
func (s *server) listPods(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []podView{}
	for _, path := range scope.paths(resourceListPath{"/api/v1", "pods"}) {
		var list struct {
			Items []podObject `json:"items"`
		}
		if !s.fetch(c, user, cluster, grant, path, &list) {
			return
		}
		for _, item := range list.Items {
			out = append(out, item.view())
		}
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{
		"pods":           out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
	})
}

// showPod returns one pod in full.
func (s *server) showPod(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	namespace, ok := s.resourceNamespace(c, grant)
	if !ok {
		return
	}
	name := c.Param("pod")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a pod name is required"})
		return
	}

	var pod podObject
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", url.PathEscape(namespace), url.PathEscape(name))
	if !s.fetch(c, user, cluster, grant, path, &pod) {
		return
	}
	c.JSON(http.StatusOK, pod.view())
}

// podLogs returns a bounded slice of a container's log. Following a log is a
// stream and goes through the proxy route instead.
func (s *server) podLogs(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	namespace, ok := s.resourceNamespace(c, grant)
	if !ok {
		return
	}
	name := c.Param("pod")
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a pod name is required"})
		return
	}

	tail := 200
	if raw := c.Query("tail"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 5000 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "tail must be between 1 and 5000"})
			return
		}
		tail = parsed
	}

	query := url.Values{}
	query.Set("tailLines", strconv.Itoa(tail))
	query.Set("timestamps", "true")
	if container := c.Query("container"); container != "" {
		query.Set("container", container)
	}
	if c.Query("previous") == "true" {
		query.Set("previous", "true")
	}

	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log?%s",
		url.PathEscape(namespace), url.PathEscape(name), query.Encode())

	resp, err := s.proxy.Call(c.Request.Context(), user, cluster, grant, http.MethodGet, path, nil, nil)
	if err != nil {
		var callErr *bastion.CallError
		if errors.As(err, &callErr) {
			c.JSON(callErr.Status, gin.H{"error": callErr.Message})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not read the log"})
		return
	}
	if resp.Status < 200 || resp.Status >= 300 {
		c.JSON(resp.Status, gin.H{"error": kubeErrorMessage(resp.Body, resp.Status)})
		return
	}

	// Logs are plain text, not JSON, so they are handed back as-is.
	c.JSON(http.StatusOK, gin.H{"log": string(resp.Body), "tail": tail})
}

/*
 * A list read covers either one namespace or every namespace the caller may see.
 * "Every" means two different reads: an unscoped grant takes one cluster-wide
 * path, and a namespace-scoped grant reads its granted namespaces one at a time
 * and the results are merged. The second shape is what keeps the scope intact —
 * a cluster-wide list would return objects from namespaces the grant does not
 * cover, and the proxy refuses it for exactly that reason.
 */

// maxFanOut caps how many namespaces one all-namespaces read will visit. Each is
// a real call down the tunnel, so a grant covering half a cluster is asked to
// pick a namespace instead of paying for fifty round trips per list.
const maxFanOut = 25

type readScope struct {
	// Namespaces are read one at a time and merged. Empty means a single
	// cluster-wide read.
	Namespaces []string
	// Namespace is the one namespace being read, empty when the read covers all.
	Namespace string
	// All reports a read across every namespace the caller may see.
	All bool
}

// paths renders the API paths this scope reads for a given resource.
func (r readScope) paths(path resourceListPath) []string {
	if len(r.Namespaces) == 0 {
		return []string{path.clusterWide()}
	}

	out := make([]string, 0, len(r.Namespaces))
	for _, namespace := range r.Namespaces {
		out = append(out, path.namespaced(namespace))
	}
	return out
}

// resourceScope resolves what a list read covers: `all_namespaces=true` for
// everything the caller may see, otherwise the single namespace rules in
// resourceNamespace.
func (s *server) resourceScope(c *gin.Context, grant db.UserClusterAccess) (readScope, bool) {
	if c.Query("all_namespaces") != "true" {
		namespace, ok := s.resourceNamespace(c, grant)
		if !ok {
			return readScope{}, false
		}
		return readScope{Namespaces: []string{namespace}, Namespace: namespace}, true
	}

	allowed := grant.NamespaceList()
	if len(allowed) == 0 {
		return readScope{All: true}, true
	}
	if len(allowed) > maxFanOut {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf(
				"your grant covers %d namespaces, which is more than this view reads at once; pick one namespace",
				len(allowed)),
		})
		return readScope{}, false
	}
	return readScope{Namespaces: allowed, All: true}, true
}

// namedResource is a normalised list entry that knows where it sorts.
type namedResource interface {
	sortKey() (namespace, name string)
}

// sortResources orders a list by namespace then name — how a multi-namespace
// list reads, and identical to sorting by name inside a single one.
func sortResources[T namedResource](items []T) {
	slices.SortFunc(items, func(a, b T) int {
		aNamespace, aName := a.sortKey()
		bNamespace, bName := b.sortKey()
		if order := strings.Compare(aNamespace, bNamespace); order != 0 {
			return order
		}
		return strings.Compare(aName, bName)
	})
}

func (v podView) sortKey() (string, string)      { return v.Namespace, v.Name }
func (v workloadView) sortKey() (string, string) { return v.Namespace, v.Name }

// resourceNamespace reads the requested namespace and checks it against the
// caller's grant. A scoped grant with no namespace given defaults to its first
// one rather than erroring, so the UI can open on something useful.
func (s *server) resourceNamespace(c *gin.Context, grant db.UserClusterAccess) (string, bool) {
	return s.scopedNamespace(c, grant, c.Query("namespace"))
}

// scopedNamespace is that same rule for a namespace that arrived somewhere other
// than the query string — an action route carries it in its body — so the two
// cannot drift apart.
func (s *server) scopedNamespace(c *gin.Context, grant db.UserClusterAccess, named string) (string, bool) {
	requested := strings.TrimSpace(named)
	allowed := grant.NamespaceList()

	if requested == "" {
		if len(allowed) > 0 {
			return allowed[0], true
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "a namespace is required"})
		return "", false
	}
	if len(allowed) > 0 && !slices.Contains(allowed, requested) {
		c.JSON(http.StatusForbidden, gin.H{"error": "namespace is outside your granted scope"})
		return "", false
	}
	return requested, true
}

// podObject is the slice of a Kubernetes Pod the UI needs.
type podObject struct {
	Metadata struct {
		Name              string    `json:"name"`
		Namespace         string    `json:"namespace"`
		CreationTimestamp time.Time `json:"creationTimestamp"`
	} `json:"metadata"`
	Spec struct {
		NodeName   string `json:"nodeName"`
		Containers []struct {
			Name      string `json:"name"`
			Image     string `json:"image"`
			Resources struct {
				Requests map[string]string `json:"requests"`
				Limits   map[string]string `json:"limits"`
			} `json:"resources"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase             string `json:"phase"`
		PodIP             string `json:"podIP"`
		ContainerStatuses []struct {
			Name         string `json:"name"`
			Image        string `json:"image"`
			Ready        bool   `json:"ready"`
			RestartCount int32  `json:"restartCount"`
			State        map[string]struct {
				Reason string `json:"reason"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

func (p podObject) view() podView {
	out := podView{
		Name:      p.Metadata.Name,
		Namespace: p.Metadata.Namespace,
		Phase:     p.Status.Phase,
		Node:      p.Spec.NodeName,
		PodIP:     p.Status.PodIP,
		Created:   p.Metadata.CreationTimestamp,
	}

	// Containers come from the spec so a pod that has not started yet still
	// lists them; status fills in what is known.
	states := map[string]containerView{}
	for _, status := range p.Status.ContainerStatuses {
		state := "unknown"
		for name, detail := range status.State {
			state = name
			if detail.Reason != "" {
				state = detail.Reason
			}
			break
		}
		states[status.Name] = containerView{
			Name:     status.Name,
			Image:    status.Image,
			Ready:    status.Ready,
			Restarts: status.RestartCount,
			State:    state,
		}
		out.Restarts += status.RestartCount
		if status.Ready {
			out.Ready++
		}
	}

	for _, container := range p.Spec.Containers {
		view, ok := states[container.Name]
		if !ok {
			view = containerView{Name: container.Name, Image: container.Image, State: "pending"}
		}
		if view.Image == "" {
			view.Image = container.Image
		}
		// Requests and limits come from the spec, which is where they are
		// declared; a container that has not started yet still has them.
		view.CPURequestMillicores = parseCPUMillicores(container.Resources.Requests["cpu"])
		view.CPULimitMillicores = parseCPUMillicores(container.Resources.Limits["cpu"])
		view.MemoryRequestBytes = parseMemoryBytes(container.Resources.Requests["memory"])
		view.MemoryLimitBytes = parseMemoryBytes(container.Resources.Limits["memory"])
		out.Containers = append(out.Containers, view)
	}
	out.Total = len(out.Containers)
	return out
}

// kubeErrorMessage pulls the message out of a Kubernetes Status object, so a
// refusal from the cluster reaches the user in the cluster's own words.
func kubeErrorMessage(body []byte, status int) string {
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Message != "" {
		return payload.Message
	}
	return fmt.Sprintf("the cluster returned %d", status)
}
