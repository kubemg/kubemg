package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Node capacity, and the three numbers that are never the same number.
 *
 * `kubectl top` answers one of them — what a node is *using* — and the Capacity
 * panel on the cluster page has shown that since Phase 3. It is the number that
 * explains the least. A node at 30% CPU can be one the scheduler will not place
 * another pod on, because scheduling is decided on **requests**, and requests
 * are a reservation nobody is obliged to spend. The complaint this answers is
 * "there is plenty of room and nothing will schedule", and no view built on
 * usage alone can answer it.
 *
 * So this reports all three against allocatable, per node:
 *
 *   requested  what the scheduler has already promised away
 *   limited    the ceiling the node would have to honour if everything spent it
 *   used       what is actually being spent right now
 *
 * Requests come from the pod specs and are exact — the same arithmetic the
 * scheduler does, sidecars and pod overhead included (see podDemand). Usage
 * comes from metrics.k8s.io and is optional: a cluster with no metrics-server
 * still gets the two numbers that matter most for scheduling, and says so,
 * rather than failing a page over a component that is not installed.
 *
 * Beside the three numbers, each node says three more things the heatmap needs:
 *
 *   qos        how its pods split across Guaranteed, Burstable and BestEffort —
 *              the class the API server wrote, never re-derived here
 *   headroom   what is still unreserved, and whether an ordinary pod could be
 *              placed here at all (ready, not cordoned, no NoSchedule/NoExecute
 *              taint), which is what turns a free core into a usable one
 *   borrowers  pods using more than they reserved, from the per-pod Metrics API
 *              — the noisy neighbours, and under memory pressure the first pods
 *              the kubelet evicts
 *
 * Placement is deliberately the scheduler's simplest question only: requests,
 * slots, cordons and taints. Node selectors, affinity, topology spread and
 * tolerations are not read, and the page says so — a "this pod would fit"
 * answer that ignored them would be wrong exactly when somebody trusts it.
 *
 * The read is cluster-wide by nature — node capacity says nothing about a
 * namespace and reaches well past one — so a namespace-scoped grant is refused
 * here exactly as it is on node metrics.
 *
 * What this deliberately does not do: it does not cost anything, it does not
 * recommend a size, and it does not touch the cluster. Every number is read
 * through the same impersonated, audited tunnel as every other list, and the
 * verdicts below are arithmetic over what the manifests already declare.
 */

// maxUnscheduledPodsListed caps the sample of unplaceable pods carried back.
// The count is exact; the list is a sample, because a cluster with a stuck
// controller can have thousands and the operator needs the first few, not all.
const maxUnscheduledPodsListed = 10

// topRequestersPerNode is how many pods a node names as the reason it is full.
// Five is what makes a bar explain itself without turning the payload into a
// second pod list.
const topRequestersPerNode = 5

/*
 * Thresholds. These are constants rather than settings, and the reason is the
 * same one guardrails are not per-user: a number an operator can raise until
 * the warning stops is a number that stops meaning anything. They are chosen
 * to fire where an operator would already agree something is wrong.
 */
const (
	// committedPercent is where a node stops being able to take ordinary work.
	// At 90% reserved, what remains fits a sidecar and very little else.
	committedPercent = 90

	// cpuOvercommitPercent is where CPU limits stop being headroom and start
	// being contention. CPU is compressible — a container over its share is
	// throttled, not killed — so this sits well above 100%, where memory does
	// not: memoryOvercommitPercent is 100 because a node whose limits exceed
	// its memory is a node that answers a spike by evicting somebody.
	cpuOvercommitPercent    = 200
	memoryOvercommitPercent = 100

	// reservedIdle names the FinOps shape: a node most of whose capacity is
	// reserved and half of whose reservation is unspent. It is a note rather
	// than a warning — it is money, not an outage.
	reservedIdleFloorPercent = 50
	reservedIdleSpentPercent = 50

	// usagePressurePercent is where live consumption, rather than reservation,
	// becomes the node's problem. It needs the Metrics API, so a cluster without
	// one never sees it.
	usagePressurePercent = 90
)

// topBorrowersPerNode is how many pods a node names as using more than they
// reserved. Five for the same reason as topRequestersPerNode.
const topBorrowersPerNode = 5

// restartAlways marks a native sidecar: an init container that starts during
// initialisation and then keeps running for the life of the pod.
const restartAlways = "Always"

/* ------------------------------------------------------------ the payload -- */

// capacityDimension is one resource on one node, in the unit that resource is
// counted in: millicores for CPU, bytes for memory.
type capacityDimension struct {
	Allocatable int64 `json:"allocatable"`
	Requested   int64 `json:"requested"`
	Limited     int64 `json:"limited"`
	Used        int64 `json:"used"`

	RequestedPercent float64 `json:"requested_percent"`
	LimitedPercent   float64 `json:"limited_percent"`
	UsedPercent      float64 `json:"used_percent"`

	// Unlimited counts the containers running here that declare no limit for
	// this resource. It travels with the limit because it is what the limit
	// figure means: a node whose containers mostly declare nothing has a
	// limited percentage that describes a minority of what runs on it.
	Unlimited int `json:"unlimited_containers"`
}

// podSlots is the third kind of capacity, and the one nobody remembers until a
// node with idle CPU refuses to take another pod: the kubelet's own ceiling.
type podSlots struct {
	Allocatable     int64   `json:"allocatable"`
	Scheduled       int64   `json:"scheduled"`
	Percent         float64 `json:"percent"`
	WithoutRequests int     `json:"without_requests"`
}

// capacityConcern is one thing this node's numbers say. The sentence is written
// here rather than in the browser for the reason the posture rules are: it is a
// claim about the cluster, and a claim assembled client-side is one that can
// drift from the arithmetic it describes.
type capacityConcern struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
}

// podRequest is one pod's share of the node it sits on.
type podRequest struct {
	Name          string  `json:"name"`
	Namespace     string  `json:"namespace"`
	CPUMillicores int64   `json:"cpu_millicores"`
	MemoryBytes   int64   `json:"memory_bytes"`
	SharePercent  float64 `json:"share_percent"`
}

// qosCounts is how the pods on a node split across the three QoS classes. The
// class is the one the API server wrote on the pod (`status.qosClass`); a pod
// that carries none is left out rather than guessed into one.
type qosCounts struct {
	Guaranteed int `json:"guaranteed"`
	Burstable  int `json:"burstable"`
	BestEffort int `json:"best_effort"`
}

func (q *qosCounts) add(class string) {
	switch class {
	case "Guaranteed":
		q.Guaranteed++
	case "Burstable":
		q.Burstable++
	case "BestEffort":
		q.BestEffort++
	}
}

// nodeHeadroom is what a node has not yet promised away: allocatable less
// requested, never below zero, and the pod slots still free.
type nodeHeadroom struct {
	CPU    int64 `json:"cpu"`
	Memory int64 `json:"memory"`
	Pods   int64 `json:"pods"`
}

// podBorrow is one pod using more than it reserved. Excess is measured against
// the node's allocatable, so pods on different nodes compare on one scale.
type podBorrow struct {
	Name          string  `json:"name"`
	Namespace     string  `json:"namespace"`
	QOS           string  `json:"qos"`
	CPUUsed       int64   `json:"cpu_used"`
	CPURequest    int64   `json:"cpu_request"`
	MemoryUsed    int64   `json:"memory_used"`
	MemoryRequest int64   `json:"memory_request"`
	ExcessPercent float64 `json:"excess_percent"`
}

// nodeCapacityRow is one row of the heatmap.
type nodeCapacityRow struct {
	Name        string   `json:"name"`
	Roles       []string `json:"roles"`
	Ready       bool     `json:"ready"`
	Schedulable bool     `json:"schedulable"`

	// Taints are written as kubectl writes them (`key=value:Effect`), and
	// Placeable is whether an ordinary pod — one with no tolerations — could
	// land here at all: ready, not cordoned, and no NoSchedule/NoExecute taint.
	Taints    []string `json:"taints"`
	Placeable bool     `json:"placeable"`

	CPU    capacityDimension `json:"cpu"`
	Memory capacityDimension `json:"memory"`
	Pods   podSlots          `json:"pods"`

	QOS      qosCounts    `json:"qos"`
	Headroom nodeHeadroom `json:"headroom"`

	Concerns []capacityConcern `json:"concerns"`
	Severity string            `json:"severity"`

	// TopRequests is why this node reads the way it does, in one hop.
	TopRequests []podRequest `json:"top_requests"`

	// Borrowing counts the pods here using more CPU or memory than they
	// reserved; TopBorrowers names the largest of them. Both stay empty when
	// per-pod usage could not be read, which the payload reports separately.
	Borrowing    int         `json:"borrowing_pods"`
	TopBorrowers []podBorrow `json:"top_borrowers"`
}

func (n nodeCapacityRow) sortKey() (string, string) { return "", n.Name }

// capacitySummary is the fleet-level reading of the same three numbers, plus
// the counts that say where to look first.
type capacitySummary struct {
	Nodes       int `json:"nodes"`
	Ready       int `json:"ready"`
	Schedulable int `json:"schedulable"`

	CPU    capacityDimension `json:"cpu"`
	Memory capacityDimension `json:"memory"`
	Pods   podSlots          `json:"pods"`

	QOS qosCounts `json:"qos"`

	// SeverityCounts is how many nodes landed in each verdict, so the page can
	// lead with "two nodes need attention" rather than with a wall of bars.
	SeverityCounts map[string]int `json:"severity_counts"`

	Placement placementSummary `json:"placement"`
}

// placementSlot is the most one node can still take of one resource, with what
// it has of the other beside it: a node with four free cores and 200 MiB free
// takes a four-core pod only if that pod asks for almost no memory.
type placementSlot struct {
	Node   string `json:"node"`
	CPU    int64  `json:"cpu"`
	Memory int64  `json:"memory"`
}

/*
 * placementSummary is the fragmentation reading: the unreserved capacity added
 * up across every node an ordinary pod could land on, beside the most any one
 * of them can take. Twelve free cores spread as twelve single cores do not
 * schedule a two-core pod, and the total alone would say they do.
 *
 * Only placeable nodes with a free pod slot count. The largest slots are nil
 * when there is no such node, which is a different answer from a slot of zero.
 */
type placementSummary struct {
	PlaceableNodes int            `json:"placeable_nodes"`
	Free           nodeHeadroom   `json:"free"`
	LargestCPU     *placementSlot `json:"largest_cpu"`
	LargestMemory  *placementSlot `json:"largest_memory"`
}

// unscheduledPod is a pod the scheduler has not placed. It is the other half of
// an oversubscription report: a cluster with no room says so here.
type unscheduledPod struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Reason    string `json:"reason,omitempty"`
}

/* -------------------------------------------------------------- the read -- */

// clusterCapacity reports allocation against capacity for every node.
func (s *server) clusterCapacity(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	if !s.requireClusterScope(c, grant, "node capacity") {
		return
	}

	nodes, ok := s.fetchNodes(c, user, cluster, grant)
	if !ok {
		return
	}

	pods, ok := s.fetchSchedulablePods(c, user, cluster, grant)
	if !ok {
		return
	}

	// Usage is the optional third number. A cluster with no metrics-server
	// answers the other two rather than nothing at all, which is the same
	// contract the metrics routes already keep.
	usage, metricsAvailable, ok := s.fetchNodeUsage(c, user, cluster, grant)
	if !ok {
		return
	}

	// Per-pod usage only means anything beside node usage, so it is not asked
	// for when the Metrics API is absent.
	var podUsage map[podKey]nodeSize
	podUsageReason := capacityUsageUnavailableReason
	if metricsAvailable {
		podUsage, podUsageReason, ok = s.fetchPodUsage(c, user, cluster, grant)
		if !ok {
			return
		}
	}

	rows, summary, unscheduled := buildCapacity(nodes, pods, usage, podUsage)

	payload := gin.H{
		"available":           metricsAvailable,
		"pod_usage_available": podUsage != nil,
		"nodes":               rows,
		"summary":             summary,
		"unscheduled":         unscheduled.sample,
		"unscheduled_pods":    unscheduled.count,
	}
	if !metricsAvailable {
		// The word "available" means the same thing here as on the metrics
		// routes — the Metrics API answered — and the page is still whole
		// without it, so the reason says which part is missing.
		payload["reason"] = capacityUsageUnavailableReason
	}
	if podUsage == nil {
		payload["pod_usage_reason"] = podUsageReason
	}
	listResponse(c, payload)
}

// podKey addresses one pod across the spec list and the metrics list.
type podKey struct{ namespace, name string }

/*
 * fetchPodUsage reads live consumption per pod, cluster-wide, for the borrower
 * reading. It is the one read on this page whose refusal does not fail it: a
 * role can be granted node metrics and not pod metrics, and the rest of the
 * report is whole without borrowers. So a 403 answers "unavailable, because"
 * in the cluster's own words, exactly as an absent Metrics API does. Anything
 * else — a tunnel failure, an unreadable body — still fails the request.
 */
func (s *server) fetchPodUsage(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess,
) (map[podKey]nodeSize, string, bool) {
	resp, ok := s.callResource(c, user, cluster, grant, metricsAPIGroup+"/pods")
	if !ok {
		return nil, "", false
	}
	usage, reason, answered := podUsageAnswer(resp)
	if !answered {
		// decodeResource writes the cluster's own error, or a 502 for a body
		// that does not decode — the same answer every other read gives.
		s.decodeResource(c, resp, &metricsList{})
		return nil, "", false
	}
	return usage, reason, true
}

// podUsageAnswer reads the per-pod Metrics API response. answered is false
// only for a response that should fail the request; an absent or refused read
// is an answer with a reason and no usage.
func podUsageAnswer(resp *bastion.Response) (usage map[podKey]nodeSize, reason string, answered bool) {
	switch resp.Status {
	case http.StatusNotFound, http.StatusServiceUnavailable:
		return nil, capacityPodUsageUnavailableReason, true
	case http.StatusForbidden:
		return nil, "The cluster refused the per-pod Metrics API read: " +
			kubeErrorMessage(resp.Body, resp.Status), true
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, "", false
	}

	var list metricsList
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		return nil, "", false
	}
	out := make(map[podKey]nodeSize, len(list.Items))
	for _, item := range list.Items {
		pod := podUsageOf(item.Metadata.Name, item.Metadata.Namespace, item.Containers)
		out[podKey{pod.Namespace, pod.Name}] = nodeSize{cpu: pod.CPUMillicores, memory: pod.MemoryBytes}
	}
	return out, "", true
}

const capacityUsageUnavailableReason = "This cluster does not serve the Kubernetes Metrics API, " +
	"so live usage is missing. Requests and limits are read from the pod specs and are unaffected."

// capacityPodUsageUnavailableReason is the narrower absence: node usage
// answered and per-pod usage did not.
const capacityPodUsageUnavailableReason = "The Metrics API did not answer for pods, so which pods " +
	"use more than they reserved cannot be shown. Node usage is unaffected."

// nodeRecord is a node reduced to what capacity arithmetic needs.
type nodeRecord struct {
	Name          string
	Roles         []string
	Ready         bool
	Unschedulable bool
	Taints        []nodeTaint
	Allocatable   nodeSize
	PodSlots      int64
}

// nodeTaint is one entry of `spec.taints`.
type nodeTaint struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Effect string `json:"effect"`
}

// String writes the taint the way kubectl does: `key=value:Effect`, with the
// `=value` dropped when there is none.
func (t nodeTaint) String() string {
	out := t.Key
	if t.Value != "" {
		out += "=" + t.Value
	}
	return out + ":" + t.Effect
}

// repels says whether this taint keeps a pod with no tolerations off the node.
// PreferNoSchedule is a preference the scheduler may override, so it does not.
func (t nodeTaint) repels() bool {
	return t.Effect == "NoSchedule" || t.Effect == "NoExecute"
}

// placeable is whether an ordinary pod could be scheduled onto this node.
func (n nodeRecord) placeable() bool {
	if !n.Ready || n.Unschedulable {
		return false
	}
	return !slices.ContainsFunc(n.Taints, nodeTaint.repels)
}

// nodeList is the API server's node list in the fields capacity arithmetic
// needs. It is a named type rather than an anonymous one inside the fetch so
// that the decode — which is the part that meets a real API server, and the
// part a hand-built struct in a test cannot exercise — can be run against a
// captured response of its own.
type nodeList struct {
	Items []struct {
		Metadata struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			Unschedulable bool        `json:"unschedulable"`
			Taints        []nodeTaint `json:"taints"`
		} `json:"spec"`
		Status struct {
			Allocatable map[string]string `json:"allocatable"`
			Capacity    map[string]string `json:"capacity"`
			Conditions  []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

// records reduces the list to what the report and the metrics route both read.
// Allocatable is used rather than capacity because it is what a workload can
// actually be scheduled into — what the kubelet and the system reserve is not
// headroom anyone can use.
func (l nodeList) records() []nodeRecord {
	out := make([]nodeRecord, 0, len(l.Items))
	for _, item := range l.Items {
		record := nodeRecord{
			Name:          item.Metadata.Name,
			Roles:         nodeRoles(item.Metadata.Labels),
			Unschedulable: item.Spec.Unschedulable,
			Taints:        item.Spec.Taints,
			Allocatable: nodeSize{
				cpu:    parseCPUMillicores(item.Status.Allocatable["cpu"]),
				memory: parseMemoryBytes(item.Status.Allocatable["memory"]),
			},
			PodSlots: parsePodSlots(item.Status.Allocatable["pods"]),
		}
		// A node reporting no allocatable at all is old enough not to; its
		// capacity is the closest true thing to fall back on.
		if record.Allocatable.cpu == 0 {
			record.Allocatable.cpu = parseCPUMillicores(item.Status.Capacity["cpu"])
		}
		if record.Allocatable.memory == 0 {
			record.Allocatable.memory = parseMemoryBytes(item.Status.Capacity["memory"])
		}
		if record.PodSlots == 0 {
			record.PodSlots = parsePodSlots(item.Status.Capacity["pods"])
		}
		for _, condition := range item.Status.Conditions {
			if condition.Type == "Ready" {
				record.Ready = condition.Status == "True"
			}
		}
		out = append(out, record)
	}
	return out
}

// fetchNodes reads the node list once, for both this report and the node
// metrics route beside it.
func (s *server) fetchNodes(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess,
) ([]nodeRecord, bool) {
	var list nodeList
	if !fetchList(s, c, user, cluster, grant, "/api/v1/nodes", &list.Items) {
		return nil, false
	}
	return list.records(), true
}

// parsePodSlots reads the `pods` quantity, which is a plain count rather than a
// CPU or memory quantity but arrives through the same field.
func parsePodSlots(raw string) int64 {
	return parseCPUMillicores(raw) / 1000
}

/* ------------------------------------------------------------ pod demand -- */

// capacityContainer is one container's declared resources.
type capacityContainer struct {
	Name      string `json:"name"`
	Resources struct {
		Requests map[string]string `json:"requests"`
		Limits   map[string]string `json:"limits"`
	} `json:"resources"`
}

// capacityInitContainer adds the field that decides whether an init container
// is a step that finishes or a sidecar that does not.
type capacityInitContainer struct {
	capacityContainer
	RestartPolicy string `json:"restartPolicy"`
}

// capacityPod is one pod reduced to what it takes out of a node.
type capacityPod struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		NodeName       string                  `json:"nodeName"`
		Overhead       map[string]string       `json:"overhead"`
		Containers     []capacityContainer     `json:"containers"`
		InitContainers []capacityInitContainer `json:"initContainers"`
	} `json:"spec"`
	Status struct {
		Phase      string `json:"phase"`
		QOSClass   string `json:"qosClass"`
		Conditions []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"conditions"`
	} `json:"status"`
}

// podDemand is what one pod costs the node it is placed on.
type podDemand struct {
	cpuRequest    int64
	memoryRequest int64
	cpuLimit      int64
	memoryLimit   int64

	cpuUnlimited    int
	memoryUnlimited int

	// requested is false for a BestEffort pod — one that asks for nothing.
	// Those are invisible to the scheduler's arithmetic while being entirely
	// visible in the usage figure, which is exactly the discrepancy that makes
	// a node read as idle right up until it is not.
	requested bool
}

/*
 * demandOf is the scheduler's own arithmetic, not a sum of containers.
 *
 * A pod's requirement is the larger of two things: what it needs once it is
 * running — every regular container, plus every native sidecar, since a sidecar
 * starts during initialisation and never exits — and the most any single init
 * step needed on the way there, which it needed *on top of* the sidecars
 * already started before it. Pod overhead, which a sandboxed runtime charges
 * for the sandbox itself, is added to both.
 *
 * Getting this wrong understates a node that runs sidecars, which is most of
 * them once a service mesh is installed.
 *
 * Limits are summed over what keeps running — regular containers and sidecars —
 * and a plain init container's limit is deliberately ignored: it constrains a
 * step that has finished, not the steady state this report is about.
 */
func demandOf(pod capacityPod) podDemand {
	var out podDemand

	var runningCPU, runningMemory int64
	add := func(container capacityContainer) {
		runningCPU += parseCPUMillicores(container.Resources.Requests["cpu"])
		runningMemory += parseMemoryBytes(container.Resources.Requests["memory"])

		if _, declared := container.Resources.Limits["cpu"]; declared {
			out.cpuLimit += parseCPUMillicores(container.Resources.Limits["cpu"])
		} else {
			out.cpuUnlimited++
		}
		if _, declared := container.Resources.Limits["memory"]; declared {
			out.memoryLimit += parseMemoryBytes(container.Resources.Limits["memory"])
		} else {
			out.memoryUnlimited++
		}
	}

	for _, container := range pod.Spec.Containers {
		add(container)
	}

	// Init containers, in order: a sidecar joins the running set and stays in
	// it for every later step; a plain one peaks alone on top of whatever
	// sidecars are already up.
	var sidecarCPU, sidecarMemory, peakCPU, peakMemory int64
	for _, init := range pod.Spec.InitContainers {
		cpu := parseCPUMillicores(init.Resources.Requests["cpu"])
		memory := parseMemoryBytes(init.Resources.Requests["memory"])
		if init.RestartPolicy == restartAlways {
			sidecarCPU += cpu
			sidecarMemory += memory
			add(init.capacityContainer)
			continue
		}
		peakCPU = max(peakCPU, cpu+sidecarCPU)
		peakMemory = max(peakMemory, memory+sidecarMemory)
	}

	out.cpuRequest = max(runningCPU, peakCPU) + parseCPUMillicores(pod.Spec.Overhead["cpu"])
	out.memoryRequest = max(runningMemory, peakMemory) + parseMemoryBytes(pod.Spec.Overhead["memory"])
	out.cpuLimit += parseCPUMillicores(pod.Spec.Overhead["cpu"])
	out.memoryLimit += parseMemoryBytes(pod.Spec.Overhead["memory"])
	out.requested = out.cpuRequest > 0 || out.memoryRequest > 0
	return out
}

// schedulablePodsPath asks for the pods that still hold capacity. A pod that
// has Succeeded or Failed is finished: it still exists as an object, and it
// still shows in `kubectl get pods`, but the scheduler has released everything
// it reserved. Filtering server-side also keeps a completed CronJob's backlog
// out of a cluster-wide list, which on a busy cluster is most of it.
func schedulablePodsPath() string {
	query := url.Values{}
	query.Set("fieldSelector", "status.phase!=Succeeded,status.phase!=Failed")
	return "/api/v1/pods?" + query.Encode()
}

// capacityPodList is the pod list, named for the same reason nodeList is.
type capacityPodList struct {
	Items []capacityPod `json:"items"`
}

// fetchSchedulablePods reads every pod that still holds capacity, cluster-wide.
func (s *server) fetchSchedulablePods(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess,
) ([]capacityPod, bool) {
	var list capacityPodList
	if !fetchList(s, c, user, cluster, grant, schedulablePodsPath(), &list.Items) {
		return nil, false
	}
	return list.Items, true
}

// fetchNodeUsage reads live consumption per node, treating an absent Metrics
// API as an absent column rather than as a failure.
func (s *server) fetchNodeUsage(c *gin.Context, user *db.User, cluster *db.Cluster,
	grant db.UserClusterAccess,
) (map[string]nodeSize, bool, bool) {
	var usage metricsList
	found, ok := s.fetchMetrics(c, user, cluster, grant, metricsAPIGroup+"/nodes", &usage)
	if !ok {
		return nil, false, false
	}
	if !found {
		return map[string]nodeSize{}, false, true
	}

	out := make(map[string]nodeSize, len(usage.Items))
	for _, item := range usage.Items {
		out[item.Metadata.Name] = nodeSize{
			cpu:    parseCPUMillicores(item.Usage["cpu"]),
			memory: parseMemoryBytes(item.Usage["memory"]),
		}
	}
	return out, true, true
}

/* ----------------------------------------------------------- the reading -- */

// unscheduledReport is the count of pods with nowhere to go, and a sample.
type unscheduledReport struct {
	count  int
	sample []unscheduledPod
}

// buildCapacity turns four reads into the report. It is a pure function of
// them so the arithmetic — which is the whole feature — is testable without a
// cluster or an HTTP round trip. podUsage is nil when per-pod usage could not
// be read, which leaves every borrower reading empty rather than zero.
func buildCapacity(nodes []nodeRecord, pods []capacityPod, usage map[string]nodeSize,
	podUsage map[podKey]nodeSize,
) ([]nodeCapacityRow, capacitySummary, unscheduledReport) {
	rows := make(map[string]*nodeCapacityRow, len(nodes))
	requesters := make(map[string][]podRequest, len(nodes))
	borrowers := make(map[string][]podBorrow, len(nodes))

	order := make([]string, 0, len(nodes))
	for _, node := range nodes {
		row := &nodeCapacityRow{
			Name:        node.Name,
			Roles:       node.Roles,
			Ready:       node.Ready,
			Schedulable: !node.Unschedulable,
			CPU:         capacityDimension{Allocatable: node.Allocatable.cpu},
			Memory:      capacityDimension{Allocatable: node.Allocatable.memory},
			Pods:        podSlots{Allocatable: node.PodSlots},
			Taints:      taintStrings(node.Taints),
			Placeable:   node.placeable(),
			TopRequests: []podRequest{},
		}
		if used, known := usage[node.Name]; known {
			row.CPU.Used = used.cpu
			row.Memory.Used = used.memory
		}
		rows[node.Name] = row
		order = append(order, node.Name)
	}

	unscheduled := unscheduledReport{sample: []unscheduledPod{}}
	for _, pod := range pods {
		if pod.Spec.NodeName == "" {
			unscheduled.count++
			if len(unscheduled.sample) < maxUnscheduledPodsListed {
				unscheduled.sample = append(unscheduled.sample, unscheduledPod{
					Name:      pod.Metadata.Name,
					Namespace: pod.Metadata.Namespace,
					Reason:    unschedulableReason(pod),
				})
			}
			continue
		}
		row, known := rows[pod.Spec.NodeName]
		if !known {
			// A pod naming a node this list does not have — a node removed
			// between the two reads. Counting it against nothing is better
			// than inventing a row for a node that is gone.
			continue
		}

		demand := demandOf(pod)
		row.CPU.Requested += demand.cpuRequest
		row.CPU.Limited += demand.cpuLimit
		row.CPU.Unlimited += demand.cpuUnlimited
		row.Memory.Requested += demand.memoryRequest
		row.Memory.Limited += demand.memoryLimit
		row.Memory.Unlimited += demand.memoryUnlimited
		row.Pods.Scheduled++
		if !demand.requested {
			row.Pods.WithoutRequests++
		}
		row.QOS.add(pod.Status.QOSClass)

		if used, measured := podUsage[podKey{pod.Metadata.Namespace, pod.Metadata.Name}]; measured {
			if borrow, over := borrowOf(pod, demand, used, *row); over {
				borrowers[pod.Spec.NodeName] = append(borrowers[pod.Spec.NodeName], borrow)
			}
		}

		requesters[pod.Spec.NodeName] = append(requesters[pod.Spec.NodeName], podRequest{
			Name:          pod.Metadata.Name,
			Namespace:     pod.Metadata.Namespace,
			CPUMillicores: demand.cpuRequest,
			MemoryBytes:   demand.memoryRequest,
			SharePercent: max(
				percent(demand.cpuRequest, row.CPU.Allocatable),
				percent(demand.memoryRequest, row.Memory.Allocatable),
			),
		})
	}

	summary := capacitySummary{SeverityCounts: map[string]int{}}
	out := make([]nodeCapacityRow, 0, len(order))
	for _, name := range order {
		row := rows[name]
		finishDimension(&row.CPU)
		finishDimension(&row.Memory)
		row.Pods.Percent = percent(row.Pods.Scheduled, row.Pods.Allocatable)
		row.Headroom = headroomOf(*row)
		row.TopRequests = topRequesters(requesters[name])
		row.Borrowing = len(borrowers[name])
		row.TopBorrowers = topBorrowers(borrowers[name])
		row.Concerns = concernsFor(*row)
		row.Severity = highestSeverity(row.Concerns)

		summary.Nodes++
		if row.Ready {
			summary.Ready++
		}
		if row.Schedulable {
			summary.Schedulable++
		}
		summary.SeverityCounts[row.Severity]++
		accumulate(&summary.CPU, row.CPU)
		accumulate(&summary.Memory, row.Memory)
		summary.Pods.Allocatable += row.Pods.Allocatable
		summary.Pods.Scheduled += row.Pods.Scheduled
		summary.Pods.WithoutRequests += row.Pods.WithoutRequests
		summary.QOS.Guaranteed += row.QOS.Guaranteed
		summary.QOS.Burstable += row.QOS.Burstable
		summary.QOS.BestEffort += row.QOS.BestEffort
		if row.Placeable && row.Headroom.Pods > 0 {
			summary.Placement.add(*row)
		}

		out = append(out, *row)
	}
	finishDimension(&summary.CPU)
	finishDimension(&summary.Memory)
	summary.Pods.Percent = percent(summary.Pods.Scheduled, summary.Pods.Allocatable)

	sortResources(out)
	return out, summary, unscheduled
}

// taintStrings writes a node's taints for the payload, never nil.
func taintStrings(taints []nodeTaint) []string {
	out := make([]string, 0, len(taints))
	for _, taint := range taints {
		out = append(out, taint.String())
	}
	return out
}

// headroomOf is what a node has left to promise. An over-reserved node — which
// a static pod or a node shrunk under its pods can produce — has none, not a
// negative amount.
func headroomOf(row nodeCapacityRow) nodeHeadroom {
	return nodeHeadroom{
		CPU:    max(0, row.CPU.Allocatable-row.CPU.Requested),
		Memory: max(0, row.Memory.Allocatable-row.Memory.Requested),
		Pods:   max(0, row.Pods.Allocatable-row.Pods.Scheduled),
	}
}

// add folds one placeable node into the fragmentation reading. Ties keep the
// first node seen.
func (p *placementSummary) add(row nodeCapacityRow) {
	p.PlaceableNodes++
	p.Free.CPU += row.Headroom.CPU
	p.Free.Memory += row.Headroom.Memory
	p.Free.Pods += row.Headroom.Pods

	slot := placementSlot{Node: row.Name, CPU: row.Headroom.CPU, Memory: row.Headroom.Memory}
	if p.LargestCPU == nil || slot.CPU > p.LargestCPU.CPU {
		p.LargestCPU = &slot
	}
	if p.LargestMemory == nil || slot.Memory > p.LargestMemory.Memory {
		memorySlot := slot
		p.LargestMemory = &memorySlot
	}
}

/*
 * borrowOf reads one pod against what it reserved. A pod is borrowing when its
 * live use of either resource is above its request — a BestEffort pod, which
 * requests nothing, borrows everything it uses.
 *
 * Excess is measured against the node's allocatable rather than against the
 * pod's own request, because the question is what the pod takes from its
 * neighbours: a pod at ten times a 10m request is not a noisy neighbour, and
 * one 2 GiB over a 4 GiB request on an 8 GiB node is.
 */
func borrowOf(pod capacityPod, demand podDemand, used nodeSize, row nodeCapacityRow) (podBorrow, bool) {
	cpuOver := max(0, used.cpu-demand.cpuRequest)
	memoryOver := max(0, used.memory-demand.memoryRequest)
	if cpuOver == 0 && memoryOver == 0 {
		return podBorrow{}, false
	}
	return podBorrow{
		Name:          pod.Metadata.Name,
		Namespace:     pod.Metadata.Namespace,
		QOS:           pod.Status.QOSClass,
		CPUUsed:       used.cpu,
		CPURequest:    demand.cpuRequest,
		MemoryUsed:    used.memory,
		MemoryRequest: demand.memoryRequest,
		ExcessPercent: max(
			percent(cpuOver, row.CPU.Allocatable),
			percent(memoryOver, row.Memory.Allocatable),
		),
	}, true
}

// topBorrowers names the pods taking the most beyond their reservation,
// largest first. A borrow too small to register as a tenth of a percent of the
// node is still counted in Borrowing but is not worth a line.
func topBorrowers(all []podBorrow) []podBorrow {
	slices.SortFunc(all, func(a, b podBorrow) int {
		if a.ExcessPercent != b.ExcessPercent {
			if a.ExcessPercent > b.ExcessPercent {
				return -1
			}
			return 1
		}
		if order := strings.Compare(a.Namespace, b.Namespace); order != 0 {
			return order
		}
		return strings.Compare(a.Name, b.Name)
	})
	out := slices.DeleteFunc(slices.Clone(all), func(entry podBorrow) bool { return entry.ExcessPercent <= 0 })
	if len(out) > topBorrowersPerNode {
		out = out[:topBorrowersPerNode]
	}
	if out == nil {
		return []podBorrow{}
	}
	return out
}

// unschedulableReason lifts the scheduler's own sentence off the pod, which
// says what no arithmetic here could — "0/5 nodes are available: 5 Insufficient
// memory" names both the shortage and its size.
func unschedulableReason(pod capacityPod) string {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == "PodScheduled" && condition.Status == "False" {
			if condition.Message != "" {
				return condition.Message
			}
			return condition.Reason
		}
	}
	return ""
}

// accumulate folds one node's dimension into the cluster total. Percentages are
// left to finishDimension, because a total's percentage is computed from the
// totals and never from an average of percentages.
func accumulate(total *capacityDimension, node capacityDimension) {
	total.Allocatable += node.Allocatable
	total.Requested += node.Requested
	total.Limited += node.Limited
	total.Used += node.Used
	total.Unlimited += node.Unlimited
}

func finishDimension(d *capacityDimension) {
	d.RequestedPercent = percent(d.Requested, d.Allocatable)
	d.LimitedPercent = percent(d.Limited, d.Allocatable)
	d.UsedPercent = percent(d.Used, d.Allocatable)
}

// topRequesters names the pods holding the largest share of a node, biggest
// first. Share is the greater of a pod's two shares rather than the sum of
// them: a pod reserving 60% of the memory and 2% of the CPU is 60% of the
// reason that node is full, and adding the two would describe nothing.
func topRequesters(all []podRequest) []podRequest {
	slices.SortFunc(all, func(a, b podRequest) int {
		if a.SharePercent != b.SharePercent {
			if a.SharePercent > b.SharePercent {
				return -1
			}
			return 1
		}
		if order := strings.Compare(a.Namespace, b.Namespace); order != 0 {
			return order
		}
		return strings.Compare(a.Name, b.Name)
	})
	all = slices.DeleteFunc(all, func(entry podRequest) bool { return entry.SharePercent <= 0 })
	if len(all) > topRequestersPerNode {
		all = all[:topRequestersPerNode]
	}
	if all == nil {
		return []podRequest{}
	}
	return all
}

/* ---------------------------------------------------------- the verdicts -- */

const (
	severityOK     = "ok"
	severityNote   = "note"
	severityWarn   = "warn"
	severityDanger = "danger"
)

// severityRank orders the verdicts. `note` sits below `warn` deliberately:
// money and missing declarations are worth surfacing, and neither is an
// incident, so neither may outrank a node that cannot schedule.
var severityRank = map[string]int{
	severityOK:     0,
	severityNote:   1,
	severityWarn:   2,
	severityDanger: 3,
}

func highestSeverity(concerns []capacityConcern) string {
	highest := severityOK
	for _, concern := range concerns {
		if severityRank[concern.Severity] > severityRank[highest] {
			highest = concern.Severity
		}
	}
	return highest
}

// concernsFor reads one node's numbers, hardest first.
func concernsFor(node nodeCapacityRow) []capacityConcern {
	out := []capacityConcern{}

	if !node.Ready {
		out = append(out, capacityConcern{
			Code: "not-ready", Severity: severityDanger,
			Title: "Node is not Ready",
			Detail: "The kubelet is not reporting Ready, so whatever this node has is not " +
				"capacity anyone can use. Its pods are counted below as they stand.",
		})
	}
	if !node.Schedulable {
		out = append(out, capacityConcern{
			Code: "unschedulable", Severity: severityWarn,
			Title: "Node is cordoned",
			Detail: "Nothing new will be placed here. What is already running still holds its " +
				"reservation, so this node's allocatable capacity is not headroom for the cluster.",
		})
	}

	out = append(out, dimensionConcerns("cpu", "CPU", node.CPU, cpuOvercommitPercent)...)
	out = append(out, dimensionConcerns("memory", "Memory", node.Memory, memoryOvercommitPercent)...)

	switch {
	case node.Pods.Allocatable > 0 && node.Pods.Scheduled >= node.Pods.Allocatable:
		out = append(out, capacityConcern{
			Code: "pod-slots-exhausted", Severity: severityDanger,
			Title: "No pod slots left",
			Detail: "Every pod slot the kubelet allows is taken, so nothing more will be placed " +
				"here however much CPU and memory remain.",
		})
	case node.Pods.Percent >= committedPercent:
		out = append(out, capacityConcern{
			Code: "pod-slots-committed", Severity: severityWarn,
			Title: "Pod slots nearly full",
			Detail: "The kubelet's own pod ceiling is close, which limits this node before its " +
				"CPU or memory does.",
		})
	}

	out = append(out, pressureConcerns(node)...)

	if node.Pods.WithoutRequests > 0 {
		out = append(out, capacityConcern{
			Code: "requests-unset", Severity: severityNote,
			Title: "Pods that reserve nothing",
			Detail: "Some pods here declare no CPU or memory request at all. They consume the " +
				"node while being invisible to the scheduler, so the reserved figures below " +
				"understate what this node is really carrying.",
		})
	}

	slices.SortStableFunc(out, func(a, b capacityConcern) int {
		return severityRank[b.Severity] - severityRank[a.Severity]
	})
	return out
}

// dimensionConcerns reads one resource. The overcommit threshold differs by
// resource and is passed in rather than branched on here, because the reason it
// differs — CPU is throttled, memory is killed — belongs where it is named.
func dimensionConcerns(code, label string, d capacityDimension, overcommitPercent float64) []capacityConcern {
	out := []capacityConcern{}
	if d.Allocatable <= 0 {
		return out
	}

	switch {
	case d.RequestedPercent >= 100:
		out = append(out, capacityConcern{
			Code: code + "-exhausted", Severity: severityDanger,
			Title: label + " fully reserved",
			Detail: "Pods here have reserved every " + label + " this node can allocate. The " +
				"scheduler will place nothing further on it, whatever the live usage says.",
		})
	case d.RequestedPercent >= committedPercent:
		out = append(out, capacityConcern{
			Code: code + "-committed", Severity: severityWarn,
			Title: label + " nearly fully reserved",
			Detail: "Most of this node's " + label + " is already promised to the pods on it. " +
				"What remains fits very little, whatever the live usage says.",
		})
	}

	if d.LimitedPercent >= overcommitPercent {
		out = append(out, capacityConcern{
			Code: code + "-overcommitted", Severity: severityWarn,
			Title: label + " limits exceed the node",
			Detail: overcommitDetail(code, label),
		})
	}

	// Reserved and unspent: the shape a right-sizing pass is looking for. It
	// needs live usage to mean anything, so a cluster with no metrics-server
	// never sees it rather than seeing it wrongly.
	if d.Used > 0 &&
		d.RequestedPercent >= reservedIdleFloorPercent &&
		percent(d.Used, d.Requested) <= reservedIdleSpentPercent {
		out = append(out, capacityConcern{
			Code: code + "-reserved-idle", Severity: severityNote,
			Title: label + " reserved but unused",
			Detail: "Pods here reserve much more " + label + " than they are spending. The " +
				"reservation is what blocks other work from being scheduled, so this is " +
				"capacity paid for and not used.",
		})
	}

	if d.Unlimited > 0 {
		out = append(out, capacityConcern{
			Code: code + "-limits-unset", Severity: severityNote,
			Title: label + " limits not declared everywhere",
			Detail: "Some containers on this node declare no " + label + " limit, so the limit " +
				"figure describes only the containers that set one.",
		})
	}
	return out
}

/*
 * pressureConcerns read live usage against the node, which is the other half of
 * a noisy-neighbour reading: who is borrowing only matters once there is too
 * little left to lend. The two resources get different severities for the
 * reason the overcommit thresholds differ — a CPU shortage slows the pods above
 * their request and leaves every request honoured; a memory shortage evicts.
 */
func pressureConcerns(node nodeCapacityRow) []capacityConcern {
	out := []capacityConcern{}
	if node.Memory.Allocatable > 0 && node.Memory.Used > 0 && node.Memory.UsedPercent >= usagePressurePercent {
		out = append(out, capacityConcern{
			Code: "memory-pressure", Severity: severityWarn,
			Title: "Memory in use is near the node's size",
			Detail: "Live memory use is close to what this node can allocate. If it runs out, the " +
				"kubelet evicts BestEffort pods first, then the pods using the most above their " +
				"request — not necessarily the one that grew." + borrowerHint(node),
		})
	}
	if node.CPU.Allocatable > 0 && node.CPU.Used > 0 && node.CPU.UsedPercent >= usagePressurePercent {
		out = append(out, capacityConcern{
			Code: "cpu-contended", Severity: severityNote,
			Title: "CPU in use is near the node's size",
			Detail: "Live CPU use is close to what this node can allocate. Every pod still gets the " +
				"CPU it requested; what is above that is shared, so pods running above their " +
				"request slow each other down." + borrowerHint(node),
		})
	}
	return out
}

// borrowerHint points a pressure concern at the list that explains it, when
// there is one to point at.
func borrowerHint(node nodeCapacityRow) string {
	switch node.Borrowing {
	case 0:
		return ""
	case 1:
		return " One pod here is using more than it reserved."
	default:
		return " " + strconv.Itoa(node.Borrowing) + " pods here are using more than they reserved."
	}
}

// overcommitDetail says why the same ratio means different things for the two
// resources. It is the one place in this file where the asymmetry is explained
// to the operator rather than only to the reader of the code.
func overcommitDetail(code, label string) string {
	if code == "memory" {
		return "If the containers here used the memory they are allowed, this node would run " +
			"out. Memory cannot be shared under pressure — the kubelet answers a shortage by " +
			"evicting pods, and the pod evicted is not necessarily the one that caused it."
	}
	return "Containers here are allowed far more " + label + " than the node has. CPU is " +
		"throttled rather than killed, so this is a latency risk rather than an outage: a busy " +
		"neighbour slows everything sharing the node."
}
