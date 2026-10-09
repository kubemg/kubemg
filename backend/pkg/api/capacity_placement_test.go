package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
)

/*
 * What the heatmap adds to the capacity report: where an ordinary pod can
 * still go, how the pods split across QoS classes, and which of them use more
 * than they reserved. Each is arithmetic over reads the report already makes —
 * plus the per-pod Metrics API for the last — so it is pinned here as
 * arithmetic.
 */

func withQOS(pod capacityPod, class string) capacityPod {
	pod.Status.QOSClass = class
	return pod
}

/* --------------------------------------------------------------- taints --- */

func decodeNodes(t *testing.T, raw string, out *nodeList) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		t.Fatalf("decoding nodes: %v", err)
	}
}

func TestNodeTaintsDecodeAndDecidePlacement(t *testing.T) {
	var list nodeList
	decodeNodes(t, `{"items":[
		{"metadata":{"name":"cp","labels":{"node-role.kubernetes.io/control-plane":""}},
		 "spec":{"taints":[{"key":"node-role.kubernetes.io/control-plane","effect":"NoSchedule"}]},
		 "status":{"allocatable":{"cpu":"2","memory":"4Gi","pods":"110"},
		           "conditions":[{"type":"Ready","status":"True"}]}},
		{"metadata":{"name":"gpu"},
		 "spec":{"taints":[{"key":"nvidia.com/gpu","value":"present","effect":"PreferNoSchedule"}]},
		 "status":{"allocatable":{"cpu":"8","memory":"32Gi","pods":"110"},
		           "conditions":[{"type":"Ready","status":"True"}]}}
	]}`, &list)

	nodes := list.records()
	if got := nodes[0].Taints[0].String(); got != "node-role.kubernetes.io/control-plane:NoSchedule" {
		t.Errorf("a taint with no value is written key:Effect, got %q", got)
	}
	if got := nodes[1].Taints[0].String(); got != "nvidia.com/gpu=present:PreferNoSchedule" {
		t.Errorf("a taint with a value is written key=value:Effect, got %q", got)
	}
	if nodes[0].placeable() {
		t.Error("a NoSchedule taint keeps an ordinary pod off the node")
	}
	if !nodes[1].placeable() {
		t.Error("PreferNoSchedule is a preference the scheduler may override, not a refusal")
	}

	rows, summary, _ := buildCapacity(nodes, nil, nil, nil)
	if rows[0].Placeable || !rows[1].Placeable {
		t.Errorf("rows must carry the same verdict, got cp=%v gpu=%v", rows[0].Placeable, rows[1].Placeable)
	}
	if summary.Placement.PlaceableNodes != 1 || summary.Placement.Free.CPU != 8000 {
		t.Errorf("the control plane's free cores are not room for ordinary work: %+v", summary.Placement)
	}
}

func TestNoExecuteAndCordonAndNotReadyAreNotPlaceable(t *testing.T) {
	tainted := fourCoreNode("tainted")
	tainted.Taints = []nodeTaint{{Key: "dedicated", Value: "db", Effect: "NoExecute"}}
	cordoned := fourCoreNode("cordoned")
	cordoned.Unschedulable = true
	down := fourCoreNode("down")
	down.Ready = false

	for _, node := range []nodeRecord{tainted, cordoned, down} {
		if node.placeable() {
			t.Errorf("%s must not read as placeable", node.Name)
		}
	}
	_, summary, _ := buildCapacity([]nodeRecord{tainted, cordoned, down}, nil, nil, nil)
	if summary.Placement.PlaceableNodes != 0 || summary.Placement.LargestCPU != nil ||
		summary.Placement.LargestMemory != nil {
		t.Errorf("no placeable node means no largest slot at all, not a slot of zero: %+v", summary.Placement)
	}
}

/* -------------------------------------------------------- QoS and slots --- */

func TestQOSCountsPerNodeAndCluster(t *testing.T) {
	pods := []capacityPod{
		withQOS(podOn("n1", "shop", "a", container("app", nil, nil)), "BestEffort"),
		withQOS(podOn("n1", "shop", "b", container("app", nil, nil)), "Guaranteed"),
		withQOS(podOn("n2", "shop", "c", container("app", nil, nil)), "Burstable"),
		// No class written: left out rather than guessed into one.
		podOn("n2", "shop", "d", container("app", nil, nil)),
	}
	rows, summary, _ := buildCapacity([]nodeRecord{fourCoreNode("n1"), fourCoreNode("n2")}, pods, nil, nil)

	if rows[0].QOS != (qosCounts{Guaranteed: 1, BestEffort: 1}) {
		t.Errorf("n1 qos = %+v", rows[0].QOS)
	}
	if rows[1].QOS != (qosCounts{Burstable: 1}) {
		t.Errorf("n2 qos = %+v — a pod without a class must not be counted", rows[1].QOS)
	}
	if summary.QOS != (qosCounts{Guaranteed: 1, Burstable: 1, BestEffort: 1}) {
		t.Errorf("summary qos = %+v", summary.QOS)
	}
}

/* ------------------------------------------------------- fragmentation --- */

func TestPlacementNamesTheLargestSlotPerResource(t *testing.T) {
	cpuRich := fourCoreNode("cpu-rich")
	memoryRich := fourCoreNode("memory-rich")
	full := fourCoreNode("full")
	full.PodSlots = 1

	pods := []capacityPod{
		// cpu-rich: 3 cores free, 1 GiB free.
		podOn("cpu-rich", "shop", "a", container("app",
			map[string]string{"cpu": "1", "memory": "7Gi"}, nil)),
		// memory-rich: 1 core free, 6 GiB free.
		podOn("memory-rich", "shop", "b", container("app",
			map[string]string{"cpu": "3", "memory": "2Gi"}, nil)),
		// full: plenty of CPU and memory, and no pod slot left.
		podOn("full", "shop", "c", container("app", nil, nil)),
	}
	rows, summary, _ := buildCapacity([]nodeRecord{cpuRich, memoryRich, full}, pods, nil, nil)

	placement := summary.Placement
	if placement.PlaceableNodes != 2 {
		t.Fatalf("a node with no free pod slot takes nothing, whatever its CPU says: %+v", placement)
	}
	if placement.Free.CPU != 4000 || placement.Free.Memory != 7<<30 {
		t.Errorf("free = %+v, want 4000m and 7Gi across the two nodes with room", placement.Free)
	}
	if got := placement.LargestCPU; got == nil || got.Node != "cpu-rich" || got.CPU != 3000 || got.Memory != 1<<30 {
		t.Errorf("largest cpu slot = %+v, want cpu-rich with 3000m beside 1Gi", got)
	}
	if got := placement.LargestMemory; got == nil || got.Node != "memory-rich" || got.Memory != 6<<30 || got.CPU != 1000 {
		t.Errorf("largest memory slot = %+v, want memory-rich with 6Gi beside 1000m", got)
	}

	if rows[1].Headroom.Pods != 0 || rows[1].Name != "full" {
		t.Errorf("full has no pod slot left, got %+v", rows[1].Headroom)
	}
}

func TestHeadroomNeverGoesNegative(t *testing.T) {
	pods := []capacityPod{podOn("n1", "shop", "big", container("app",
		map[string]string{"cpu": "6", "memory": "12Gi"}, nil))}
	rows, _, _ := buildCapacity([]nodeRecord{fourCoreNode("n1")}, pods, nil, nil)
	if rows[0].Headroom.CPU != 0 || rows[0].Headroom.Memory != 0 {
		t.Errorf("an over-reserved node has no headroom, not a negative amount: %+v", rows[0].Headroom)
	}
}

/* ----------------------------------------------------------- borrowers --- */

func TestBorrowersAreThePodsUsingMoreThanTheyReserved(t *testing.T) {
	pods := []capacityPod{
		withQOS(podOn("n1", "shop", "within", container("app",
			map[string]string{"cpu": "1", "memory": "1Gi"}, nil)), "Burstable"),
		withQOS(podOn("n1", "shop", "over-memory", container("app",
			map[string]string{"cpu": "100m", "memory": "1Gi"}, nil)), "Burstable"),
		withQOS(podOn("n1", "batch", "best-effort", container("app", nil, nil)), "BestEffort"),
	}
	usage := map[string]nodeSize{"n1": {cpu: 1500, memory: 4 << 30}}
	podUsage := map[podKey]nodeSize{
		{"shop", "within"}:       {cpu: 500, memory: 512 << 20},
		{"shop", "over-memory"}:  {cpu: 50, memory: 3 << 30},
		{"batch", "best-effort"}: {cpu: 400, memory: 256 << 20},
	}
	rows, _, _ := buildCapacity([]nodeRecord{fourCoreNode("n1")}, pods, usage, podUsage)
	row := rows[0]

	if row.Borrowing != 2 {
		t.Fatalf("borrowing = %d, want 2 — a pod inside its request lends, it does not borrow", row.Borrowing)
	}
	top := row.TopBorrowers
	// 2 GiB over on an 8 GiB node is 25%; 400m of nothing-requested on four
	// cores is 10%. Measured against the node, not against the pod's request.
	if len(top) != 2 || top[0].Name != "over-memory" || top[1].Name != "best-effort" {
		t.Fatalf("borrowers must rank by what they take from the node, got %+v", top)
	}
	if top[0].ExcessPercent != 25 || top[1].ExcessPercent != 10 {
		t.Errorf("excess = %v / %v, want 25 / 10", top[0].ExcessPercent, top[1].ExcessPercent)
	}
	if top[1].QOS != "BestEffort" || top[1].CPURequest != 0 || top[1].CPUUsed != 400 {
		t.Errorf("a BestEffort pod borrows everything it uses: %+v", top[1])
	}
}

func TestBorrowersStayEmptyWithoutPodUsage(t *testing.T) {
	pods := []capacityPod{podOn("n1", "batch", "x", container("app", nil, nil))}
	rows, _, _ := buildCapacity([]nodeRecord{fourCoreNode("n1")}, pods,
		map[string]nodeSize{"n1": {cpu: 3900, memory: 1 << 30}}, nil)
	if rows[0].Borrowing != 0 || len(rows[0].TopBorrowers) != 0 || rows[0].TopBorrowers == nil {
		t.Errorf("with no per-pod reading, borrowers are empty (and never null): %+v", rows[0].TopBorrowers)
	}
}

func TestTopBorrowersAreCapped(t *testing.T) {
	pods := []capacityPod{}
	podUsage := map[podKey]nodeSize{}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		pods = append(pods, podOn("n1", "shop", name, container("app", nil, nil)))
		podUsage[podKey{"shop", name}] = nodeSize{cpu: 100}
	}
	rows, _, _ := buildCapacity([]nodeRecord{fourCoreNode("n1")}, pods,
		map[string]nodeSize{"n1": {cpu: 700}}, podUsage)
	if rows[0].Borrowing != 7 || len(rows[0].TopBorrowers) != topBorrowersPerNode {
		t.Errorf("count is exact, list is a sample: %d borrowing, %d named", rows[0].Borrowing,
			len(rows[0].TopBorrowers))
	}
}

/* ------------------------------------------------------------ pressure --- */

func TestPressureConcernsReadLiveUsage(t *testing.T) {
	pods := []capacityPod{withQOS(podOn("n1", "batch", "hog", container("app", nil, nil)), "BestEffort")}
	usage := map[string]nodeSize{"n1": {cpu: 3800, memory: 7600 << 20}}
	podUsage := map[podKey]nodeSize{{"batch", "hog"}: {cpu: 3000, memory: 6 << 30}}

	rows, _, _ := buildCapacity([]nodeRecord{fourCoreNode("n1")}, pods, usage, podUsage)
	memory := concernOf(t, rows[0], "memory-pressure")
	if memory.Severity != severityWarn {
		t.Errorf("a memory shortage evicts, so it warns; got %q", memory.Severity)
	}
	if !strings.Contains(memory.Detail, "One pod here is using more than it reserved") {
		t.Errorf("the concern must point at the borrowers, got %q", memory.Detail)
	}
	if got := concernOf(t, rows[0], "cpu-contended"); got.Severity != severityNote {
		t.Errorf("a CPU shortage still honours every request, so it is a note; got %q", got.Severity)
	}

	quiet, _, _ := buildCapacity([]nodeRecord{fourCoreNode("n1")}, pods, nil, nil)
	if hasConcern(quiet[0], "memory-pressure") || hasConcern(quiet[0], "cpu-contended") {
		t.Error("with no live usage, pressure cannot be claimed")
	}
}

/* ------------------------------------------------------ per-pod usage --- */

// The borrower reading is the one part of the page a refusal does not fail: a
// role may read node metrics and not pod metrics, and the rest is whole.
func TestPodUsageAnswer(t *testing.T) {
	refused := &bastion.Response{Status: http.StatusForbidden,
		Body: []byte(`{"kind":"Status","message":"pods.metrics.k8s.io is forbidden"}`)}
	usage, reason, answered := podUsageAnswer(refused)
	if !answered || usage != nil || !strings.Contains(reason, "pods.metrics.k8s.io is forbidden") {
		t.Errorf("a refusal is an answer in the cluster's words: usage=%v reason=%q answered=%v",
			usage, reason, answered)
	}

	for _, status := range []int{http.StatusNotFound, http.StatusServiceUnavailable} {
		usage, reason, answered := podUsageAnswer(&bastion.Response{Status: status})
		if !answered || usage != nil || reason != capacityPodUsageUnavailableReason {
			t.Errorf("%d means no Metrics API right now, got usage=%v reason=%q", status, usage, reason)
		}
	}

	if _, _, answered := podUsageAnswer(&bastion.Response{Status: http.StatusInternalServerError}); answered {
		t.Error("anything else fails the request rather than going quiet")
	}
	if _, _, answered := podUsageAnswer(&bastion.Response{Status: http.StatusOK, Body: []byte("{")}); answered {
		t.Error("an unreadable body fails the request")
	}

	ok := &bastion.Response{Status: http.StatusOK, Body: []byte(`{"items":[
		{"metadata":{"name":"api","namespace":"shop"},
		 "containers":[{"name":"app","usage":{"cpu":"250000000n","memory":"300Mi"}},
		               {"name":"proxy","usage":{"cpu":"50m","memory":"20Mi"}}]}]}`)}
	usage, _, answered = podUsageAnswer(ok)
	if !answered || usage[podKey{"shop", "api"}] != (nodeSize{cpu: 300, memory: 320 << 20}) {
		t.Errorf("a pod's usage is its containers summed, got %+v", usage)
	}
}
