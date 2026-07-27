package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Live utilisation, read through the same audited tunnel as everything else.
 *
 * The source is the cluster's own Metrics API (metrics.k8s.io), which is what
 * `kubectl top` reads: it is an aggregated APIService that is either installed
 * or not, and asking for it costs one call rather than a scrape pipeline. That
 * makes it the right thing for "what is this pod using *right now*" — the
 * question the pod drawer and the node summary ask. It is deliberately not a
 * history: metrics-server keeps a sliding window of a couple of minutes and
 * nothing older, so there is no series to chart here and none is pretended.
 *
 * metrics-server is optional, so a cluster without it answers with
 * `available: false` and a reason rather than an error — the same contract the
 * optional CRD lists use, because "this cluster does not serve that" is an
 * answer and not a failure.
 */

// metricsAPIGroup is the aggregated API `kubectl top` reads.
const metricsAPIGroup = "/apis/metrics.k8s.io/v1beta1"

// containerUsage is one container's current consumption.
type containerUsage struct {
	Name          string `json:"name"`
	CPUMillicores int64  `json:"cpu_millicores"`
	MemoryBytes   int64  `json:"memory_bytes"`
}

// podUsage is what a pod is using right now, with the totals the UI leads with
// summed server-side so the browser does not add up containers itself.
type podUsage struct {
	Name          string           `json:"name"`
	Namespace     string           `json:"namespace"`
	CPUMillicores int64            `json:"cpu_millicores"`
	MemoryBytes   int64            `json:"memory_bytes"`
	Containers    []containerUsage `json:"containers"`
}

func (p podUsage) sortKey() (string, string) { return p.Namespace, p.Name }

// nodeUsage pairs a node's consumption with its capacity, because a bare
// millicore count says nothing without the size of the thing it is running on.
type nodeUsage struct {
	Name                  string  `json:"name"`
	CPUMillicores         int64   `json:"cpu_millicores"`
	CPUCapacityMillicores int64   `json:"cpu_capacity_millicores"`
	CPUPercent            float64 `json:"cpu_percent"`
	MemoryBytes           int64   `json:"memory_bytes"`
	MemoryCapacityBytes   int64   `json:"memory_capacity_bytes"`
	MemoryPercent         float64 `json:"memory_percent"`
}

func (n nodeUsage) sortKey() (string, string) { return "", n.Name }

// usageSummary is the fleet-level headline: one cluster's total consumption
// against its total capacity.
type usageSummary struct {
	Nodes                 int     `json:"nodes"`
	CPUMillicores         int64   `json:"cpu_millicores"`
	CPUCapacityMillicores int64   `json:"cpu_capacity_millicores"`
	CPUPercent            float64 `json:"cpu_percent"`
	MemoryBytes           int64   `json:"memory_bytes"`
	MemoryCapacityBytes   int64   `json:"memory_capacity_bytes"`
	MemoryPercent         float64 `json:"memory_percent"`
}

// metricsContainer is one entry of a PodMetrics `containers` array, still in
// the cluster's own quantity strings.
type metricsContainer struct {
	Name  string            `json:"name"`
	Usage map[string]string `json:"usage"`
}

// metricsItem is one PodMetrics or NodeMetrics object. A node carries `usage`
// directly; a pod carries a container array.
type metricsItem struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Usage      map[string]string  `json:"usage"`
	Containers []metricsContainer `json:"containers"`
}

// metricsList is the shape of both metrics.k8s.io lists. Decoding both with one
// type keeps the two handlers from drifting apart.
type metricsList struct {
	Items []metricsItem `json:"items"`
}

// nodeMetrics reports what every node is using against what it has. It is a
// cluster-wide read, so a namespace-scoped grant is refused: node capacity says
// nothing about a namespace and reaches well past one.
func (s *server) nodeMetrics(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	if !s.requireClusterScope(c, grant, "node metrics") {
		return
	}

	var usage metricsList
	found, ok := s.fetchMetrics(c, user, cluster, grant, metricsAPIGroup+"/nodes", &usage)
	if !ok {
		return
	}
	if !found {
		c.JSON(http.StatusOK, unavailableMetrics(gin.H{
			"nodes":   []nodeUsage{},
			"summary": usageSummary{},
		}))
		return
	}

	// Capacity comes from the nodes themselves: metrics.k8s.io reports usage
	// and nothing else, and a percentage is the only form of this number an
	// operator can read at a glance.
	capacity, ok := s.nodeCapacity(c, user, cluster, grant)
	if !ok {
		return
	}

	out := make([]nodeUsage, 0, len(usage.Items))
	summary := usageSummary{}
	for _, item := range usage.Items {
		node := nodeUsage{
			Name:          item.Metadata.Name,
			CPUMillicores: parseCPUMillicores(item.Usage["cpu"]),
			MemoryBytes:   parseMemoryBytes(item.Usage["memory"]),
		}
		if size, known := capacity[node.Name]; known {
			node.CPUCapacityMillicores = size.cpu
			node.MemoryCapacityBytes = size.memory
		}
		node.CPUPercent = percent(node.CPUMillicores, node.CPUCapacityMillicores)
		node.MemoryPercent = percent(node.MemoryBytes, node.MemoryCapacityBytes)

		summary.Nodes++
		summary.CPUMillicores += node.CPUMillicores
		summary.CPUCapacityMillicores += node.CPUCapacityMillicores
		summary.MemoryBytes += node.MemoryBytes
		summary.MemoryCapacityBytes += node.MemoryCapacityBytes
		out = append(out, node)
	}
	summary.CPUPercent = percent(summary.CPUMillicores, summary.CPUCapacityMillicores)
	summary.MemoryPercent = percent(summary.MemoryBytes, summary.MemoryCapacityBytes)

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{"available": true, "nodes": out, "summary": summary})
}

// nodeSize is the allocatable size of one node.
type nodeSize struct {
	cpu    int64
	memory int64
}

// nodeCapacity reads what each node has to give. Allocatable is used rather
// than capacity because it is what a workload can actually be scheduled into —
// reserving for the kubelet and the system is not headroom anyone can use.
func (s *server) nodeCapacity(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess,
) (map[string]nodeSize, bool) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Allocatable map[string]string `json:"allocatable"`
				Capacity    map[string]string `json:"capacity"`
			} `json:"status"`
		} `json:"items"`
	}
	if !s.fetch(c, user, cluster, grant, "/api/v1/nodes", &list) {
		return nil, false
	}

	out := make(map[string]nodeSize, len(list.Items))
	for _, item := range list.Items {
		size := nodeSize{
			cpu:    parseCPUMillicores(item.Status.Allocatable["cpu"]),
			memory: parseMemoryBytes(item.Status.Allocatable["memory"]),
		}
		if size.cpu == 0 {
			size.cpu = parseCPUMillicores(item.Status.Capacity["cpu"])
		}
		if size.memory == 0 {
			size.memory = parseMemoryBytes(item.Status.Capacity["memory"])
		}
		out[item.Metadata.Name] = size
	}
	return out, true
}

// podMetrics reports what the pods in a scope are using. It follows the same
// scope rules as every other namespaced list: a scoped grant reads its granted
// namespaces one at a time rather than taking a cluster-wide path.
func (s *server) podMetrics(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	scope, ok := s.resourceScope(c, grant)
	if !ok {
		return
	}

	out := []podUsage{}
	available := false
	for _, path := range scope.paths(resourceListPath{metricsAPIGroup, "pods"}) {
		var usage metricsList
		found, callOK := s.fetchMetrics(c, user, cluster, grant, path, &usage)
		if !callOK {
			return
		}
		if !found {
			continue
		}
		available = true
		for _, item := range usage.Items {
			out = append(out, podUsageOf(item.Metadata.Name, item.Metadata.Namespace, item.Containers))
		}
	}

	if !available {
		c.JSON(http.StatusOK, unavailableMetrics(gin.H{
			"pods":           []podUsage{},
			"namespace":      scope.Namespace,
			"all_namespaces": scope.All,
		}))
		return
	}

	sortResources(out)
	c.JSON(http.StatusOK, gin.H{
		"available":      true,
		"pods":           out,
		"namespace":      scope.Namespace,
		"all_namespaces": scope.All,
	})
}

// showPodMetrics reads one pod's usage. The drawer wants exactly this and would
// otherwise pull a whole namespace's metrics to find one row.
func (s *server) showPodMetrics(c *gin.Context) {
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

	var pod metricsItem

	path := fmt.Sprintf("%s/namespaces/%s/pods/%s",
		metricsAPIGroup, url.PathEscape(namespace), url.PathEscape(name))
	found, ok := s.fetchMetrics(c, user, cluster, grant, path, &pod)
	if !ok {
		return
	}
	if !found {
		c.JSON(http.StatusOK, unavailableMetrics(gin.H{
			"pod": podUsage{Name: name, Namespace: namespace, Containers: []containerUsage{}},
		}))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"available": true,
		"pod":       podUsageOf(pod.Metadata.Name, pod.Metadata.Namespace, pod.Containers),
	})
}

// podUsageOf sums a pod's containers into the totals a list row shows while
// keeping the per-container breakdown the drawer needs.
func podUsageOf(name, namespace string, containers []metricsContainer) podUsage {
	out := podUsage{Name: name, Namespace: namespace, Containers: []containerUsage{}}
	for _, container := range containers {
		entry := containerUsage{
			Name:          container.Name,
			CPUMillicores: parseCPUMillicores(container.Usage["cpu"]),
			MemoryBytes:   parseMemoryBytes(container.Usage["memory"]),
		}
		out.CPUMillicores += entry.CPUMillicores
		out.MemoryBytes += entry.MemoryBytes
		out.Containers = append(out.Containers, entry)
	}
	return out
}

// metricsUnavailableReason is what the UI shows in place of a chart.
const metricsUnavailableReason = "This cluster does not serve the Kubernetes Metrics API. " +
	"Install metrics-server to see live CPU and memory usage."

// unavailableMetrics decorates an empty payload with why it is empty.
func unavailableMetrics(payload gin.H) gin.H {
	payload["available"] = false
	payload["reason"] = metricsUnavailableReason
	return payload
}

// fetchMetrics reads a metrics.k8s.io path, treating "this cluster does not
// serve it" as an answer. 404 means the APIService is not registered at all;
// 503 means it is registered but its backend is down — from the caller's side
// both are "no metrics right now", and neither is worth failing a page over.
// Anything else, a refusal in particular, is passed through as itself.
func (s *server) fetchMetrics(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess, path string, out any,
) (found bool, ok bool) {
	resp, callOK := s.callResource(c, user, cluster, grant, path)
	if !callOK {
		return false, false
	}
	if resp.Status == http.StatusNotFound || resp.Status == http.StatusServiceUnavailable {
		return false, true
	}
	if !s.decodeResource(c, resp, out) {
		return false, false
	}
	return true, true
}

// percent renders a usage against a capacity, rounded to one decimal. An
// unknown capacity reads as zero rather than as a divide by zero.
func percent(used, capacity int64) float64 {
	if capacity <= 0 {
		return 0
	}
	return float64(int64(float64(used)/float64(capacity)*1000+0.5)) / 10
}

/*
 * Kubernetes quantities. These go through apimachinery's own parser rather than
 * a hand-rolled one: the format has more corners than it looks like it does —
 * "1Gi" and "1G" differ by 7%, CPU arrives from metrics-server in nanocores and
 * from a pod spec in millicores or whole cores — and this package is already a
 * dependency of pkg/k8s, so there is nothing to save by guessing.
 *
 * An unparseable or absent quantity answers 0, which every caller reads as
 * "unknown" and renders as a missing bar. A wrong bar is worse than no bar.
 */

// parseCPUMillicores turns "250m", "1", "0.5" or "123456789n" into millicores.
func parseCPUMillicores(raw string) int64 {
	quantity, err := resource.ParseQuantity(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return quantity.MilliValue()
}

// parseMemoryBytes turns "128974848", "129e6", "1Gi" or "1024Ki" into bytes.
func parseMemoryBytes(raw string) int64 {
	quantity, err := resource.ParseQuantity(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return quantity.Value()
}
