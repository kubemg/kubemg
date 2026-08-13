package api

import (
	"net/http"
	"strings"
	"testing"
)

/*
 * Node capacity and oversubscription.
 *
 * What is pinned here is the arithmetic, because the arithmetic *is* the
 * feature: a bar that is plausible and wrong is worse than no bar, and every
 * number on this page is a claim about whether more work will schedule. The
 * transport around it — the tunnel, the grant, the audit row — is shared with
 * every other resource read and is covered by those tests; what is checked here
 * is only that this route joins the same guard chain.
 *
 * The scheduler's own formula is the part most worth pinning. Summing the
 * containers is the obvious implementation and it is wrong on any cluster with
 * a service mesh, which is most of them.
 */

// container builds one container with the requests and limits given, either of
// which may be nil for "declares none".
func container(name string, requests, limits map[string]string) capacityContainer {
	out := capacityContainer{Name: name}
	out.Resources.Requests = requests
	out.Resources.Limits = limits
	return out
}

func podOn(node, namespace, name string, containers ...capacityContainer) capacityPod {
	var pod capacityPod
	pod.Metadata.Name = name
	pod.Metadata.Namespace = namespace
	pod.Spec.NodeName = node
	pod.Spec.Containers = containers
	pod.Status.Phase = "Running"
	return pod
}

/* ------------------------------------------------------------ pod demand --- */

func TestDemandOfSumsRegularContainers(t *testing.T) {
	pod := podOn("n1", "shop", "api",
		container("app", map[string]string{"cpu": "500m", "memory": "512Mi"},
			map[string]string{"cpu": "1", "memory": "1Gi"}),
		container("log", map[string]string{"cpu": "100m", "memory": "64Mi"}, nil),
	)

	got := demandOf(pod)
	if got.cpuRequest != 600 {
		t.Errorf("cpu request = %d, want 600", got.cpuRequest)
	}
	if want := int64(576 << 20); got.memoryRequest != want {
		t.Errorf("memory request = %d, want %d", got.memoryRequest, want)
	}
	if got.cpuLimit != 1000 {
		t.Errorf("cpu limit = %d, want 1000 — a container declaring none adds nothing", got.cpuLimit)
	}
	if got.cpuUnlimited != 1 || got.memoryUnlimited != 1 {
		t.Errorf("one container declares no limit at all: got cpu %d, memory %d",
			got.cpuUnlimited, got.memoryUnlimited)
	}
	if !got.requested {
		t.Error("a pod with requests must not read as BestEffort")
	}
}

// The formula the scheduler actually uses. A sidecar starts during
// initialisation and never exits, so it is part of the running pod *and* part
// of every later init step's ceiling — summing the containers misses both.
func TestDemandOfCountsSidecarsAndInitPeaks(t *testing.T) {
	pod := podOn("n1", "shop", "api",
		container("app", map[string]string{"cpu": "500m"}, nil))
	pod.Spec.InitContainers = []capacityInitContainer{
		{
			capacityContainer: container("proxy", map[string]string{"cpu": "100m"}, nil),
			RestartPolicy:     restartAlways,
		},
		{capacityContainer: container("migrate", map[string]string{"cpu": "2"}, nil)},
	}

	got := demandOf(pod)
	// Running is 500 + 100. The migration peaks at 2000 on top of the sidecar
	// that is already up, so 2100 is the larger of the two and is what the
	// node must have free.
	if got.cpuRequest != 2100 {
		t.Fatalf("cpu request = %d, want 2100 (init peak over a running sidecar)", got.cpuRequest)
	}
}

func TestDemandOfIgnoresAFinishedInitContainersLimit(t *testing.T) {
	pod := podOn("n1", "shop", "api",
		container("app", map[string]string{"cpu": "500m"}, map[string]string{"cpu": "500m"}))
	pod.Spec.InitContainers = []capacityInitContainer{
		{capacityContainer: container("migrate", nil, map[string]string{"cpu": "8"})},
	}

	got := demandOf(pod)
	if got.cpuLimit != 500 {
		t.Errorf("cpu limit = %d, want 500 — an init container's limit constrains a step that has finished",
			got.cpuLimit)
	}
	if got.cpuUnlimited != 0 {
		t.Errorf("a finished init container must not count towards the unlimited containers, got %d",
			got.cpuUnlimited)
	}
}

func TestDemandOfAddsPodOverhead(t *testing.T) {
	pod := podOn("n1", "shop", "api",
		container("app", map[string]string{"cpu": "500m", "memory": "256Mi"},
			map[string]string{"cpu": "500m", "memory": "256Mi"}))
	pod.Spec.Overhead = map[string]string{"cpu": "50m", "memory": "64Mi"}

	got := demandOf(pod)
	if got.cpuRequest != 550 {
		t.Errorf("cpu request = %d, want 550 — a sandboxed runtime charges for the sandbox", got.cpuRequest)
	}
	if want := int64(320 << 20); got.memoryRequest != want {
		t.Errorf("memory request = %d, want %d", got.memoryRequest, want)
	}
	if got.cpuLimit != 550 {
		t.Errorf("cpu limit = %d, want 550 — overhead is charged against the ceiling too", got.cpuLimit)
	}
}

// A BestEffort pod is the discrepancy that makes a node read as idle right up
// until it is not: it consumes while reserving nothing.
func TestDemandOfMarksAPodThatReservesNothing(t *testing.T) {
	got := demandOf(podOn("n1", "shop", "batch", container("app", nil, nil)))
	if got.requested {
		t.Fatal("a pod declaring no requests at all must read as BestEffort")
	}
}

/* --------------------------------------------------------- the aggregate --- */

func fourCoreNode(name string) nodeRecord {
	return nodeRecord{
		Name:        name,
		Roles:       []string{"worker"},
		Ready:       true,
		Allocatable: nodeSize{cpu: 4000, memory: 8 << 30},
		PodSlots:    110,
	}
}

func TestBuildCapacityAggregatesOntoNodes(t *testing.T) {
	nodes := []nodeRecord{fourCoreNode("n1"), fourCoreNode("n2")}
	pods := []capacityPod{
		podOn("n1", "shop", "api",
			container("app", map[string]string{"cpu": "2", "memory": "2Gi"},
				map[string]string{"cpu": "2", "memory": "2Gi"})),
		podOn("n1", "shop", "worker",
			container("app", map[string]string{"cpu": "1", "memory": "1Gi"}, nil)),
		podOn("n2", "kube-system", "dns",
			container("app", map[string]string{"cpu": "100m", "memory": "128Mi"}, nil)),
	}
	usage := map[string]nodeSize{"n1": {cpu: 900, memory: 1 << 30}}

	rows, summary, unscheduled := buildCapacity(nodes, pods, usage)
	if len(rows) != 2 {
		t.Fatalf("expected a row per node, got %d", len(rows))
	}

	first := rows[0]
	if first.Name != "n1" {
		t.Fatalf("rows must be sorted by name, got %q first", first.Name)
	}
	if first.CPU.Requested != 3000 || first.CPU.RequestedPercent != 75 {
		t.Errorf("n1 cpu requested = %d (%v%%), want 3000 (75%%)",
			first.CPU.Requested, first.CPU.RequestedPercent)
	}
	if first.CPU.Used != 900 || first.CPU.UsedPercent != 22.5 {
		t.Errorf("n1 cpu used = %d (%v%%), want 900 (22.5%%)", first.CPU.Used, first.CPU.UsedPercent)
	}
	if first.Pods.Scheduled != 2 {
		t.Errorf("n1 pods scheduled = %d, want 2", first.Pods.Scheduled)
	}
	if first.CPU.Unlimited != 1 {
		t.Errorf("n1 unlimited cpu containers = %d, want 1", first.CPU.Unlimited)
	}

	// A total's percentage comes from the totals, never from averaging the
	// nodes' own percentages: 3100 of 8000 is 38.8%, not the mean of 75 and 2.5.
	if summary.CPU.Requested != 3100 || summary.CPU.RequestedPercent != 38.8 {
		t.Errorf("summary cpu = %d (%v%%), want 3100 (38.8%%)",
			summary.CPU.Requested, summary.CPU.RequestedPercent)
	}
	if summary.Nodes != 2 || summary.Ready != 2 || summary.Schedulable != 2 {
		t.Errorf("summary counts = %+v, want 2 nodes, all ready and schedulable", summary)
	}
	if unscheduled.count != 0 {
		t.Errorf("nothing here is unscheduled, got %d", unscheduled.count)
	}
}

func TestBuildCapacityReportsUnscheduledPods(t *testing.T) {
	pending := podOn("", "shop", "api",
		container("app", map[string]string{"cpu": "8"}, nil))
	pending.Status.Phase = "Pending"
	pending.Status.Conditions = []struct {
		Type    string `json:"type"`
		Status  string `json:"status"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}{
		{Type: "PodScheduled", Status: "False", Reason: "Unschedulable",
			Message: "0/2 nodes are available: 2 Insufficient cpu."},
	}

	_, _, unscheduled := buildCapacity([]nodeRecord{fourCoreNode("n1")},
		[]capacityPod{pending}, nil)

	if unscheduled.count != 1 || len(unscheduled.sample) != 1 {
		t.Fatalf("expected one unscheduled pod, got count %d sample %d",
			unscheduled.count, len(unscheduled.sample))
	}
	// The scheduler's own sentence says what no arithmetic here could.
	if !strings.Contains(unscheduled.sample[0].Reason, "Insufficient cpu") {
		t.Errorf("the scheduler's reason must be carried through, got %q", unscheduled.sample[0].Reason)
	}
}

func TestBuildCapacityCountsPodsThatReserveNothing(t *testing.T) {
	rows, summary, _ := buildCapacity(
		[]nodeRecord{fourCoreNode("n1")},
		[]capacityPod{
			podOn("n1", "shop", "batch", container("app", nil, nil)),
			podOn("n1", "shop", "api", container("app", map[string]string{"cpu": "1"}, nil)),
		}, nil)

	if rows[0].Pods.WithoutRequests != 1 || summary.Pods.WithoutRequests != 1 {
		t.Fatalf("expected exactly one pod reserving nothing, got node %d summary %d",
			rows[0].Pods.WithoutRequests, summary.Pods.WithoutRequests)
	}
	if !hasConcern(rows[0], "requests-unset") {
		t.Error("a node carrying a pod that reserves nothing must say so — the reserved figure understates it")
	}
}

// A pod naming a node the node list does not have is a node removed between the
// two reads. Inventing a row for it would report capacity that no longer exists.
func TestBuildCapacityIgnoresPodsOnAVanishedNode(t *testing.T) {
	rows, summary, unscheduled := buildCapacity(
		[]nodeRecord{fourCoreNode("n1")},
		[]capacityPod{podOn("gone", "shop", "api", container("app", map[string]string{"cpu": "1"}, nil))},
		nil)

	if len(rows) != 1 || rows[0].CPU.Requested != 0 {
		t.Fatalf("a pod on an unknown node must not be counted anywhere, got %+v", rows)
	}
	if summary.Nodes != 1 || unscheduled.count != 0 {
		t.Errorf("it is neither a node nor unscheduled: nodes %d, unscheduled %d",
			summary.Nodes, unscheduled.count)
	}
}

// Share is the greater of a pod's two shares, not their sum: a pod holding 60%
// of the memory and 2% of the CPU is 60% of the reason the node is full.
func TestTopRequestersRankByTheLargerShare(t *testing.T) {
	rows, _, _ := buildCapacity(
		[]nodeRecord{fourCoreNode("n1")},
		[]capacityPod{
			podOn("n1", "shop", "cpu-heavy", container("app", map[string]string{"cpu": "1"}, nil)),
			podOn("n1", "shop", "memory-heavy",
				container("app", map[string]string{"cpu": "100m", "memory": "6Gi"}, nil)),
			podOn("n1", "shop", "nothing", container("app", nil, nil)),
		}, nil)

	top := rows[0].TopRequests
	if len(top) != 2 {
		t.Fatalf("a pod reserving nothing has no share to rank, so two rows are expected, got %d", len(top))
	}
	if top[0].Name != "memory-heavy" {
		t.Errorf("75%% of the memory outranks 25%% of the CPU, got %q first", top[0].Name)
	}
}

/* ---------------------------------------------------------- the verdicts --- */

func hasConcern(row nodeCapacityRow, code string) bool {
	for _, concern := range row.Concerns {
		if concern.Code == code {
			return true
		}
	}
	return false
}

func concernOf(t *testing.T, row nodeCapacityRow, code string) capacityConcern {
	t.Helper()
	for _, concern := range row.Concerns {
		if concern.Code == code {
			return concern
		}
	}
	t.Fatalf("expected a %q concern, got %+v", code, row.Concerns)
	return capacityConcern{}
}

// The complaint the whole page exists to answer: a node with plenty of idle CPU
// that will not take another pod.
func TestConcernsReadReservationRatherThanUsage(t *testing.T) {
	node := fourCoreNode("n1")
	rows, _, _ := buildCapacity([]nodeRecord{node},
		[]capacityPod{podOn("n1", "shop", "api",
			container("app", map[string]string{"cpu": "4"}, map[string]string{"cpu": "4"}))},
		map[string]nodeSize{"n1": {cpu: 200}})

	row := rows[0]
	if row.CPU.UsedPercent != 5 {
		t.Fatalf("this node is 5%% busy, got %v%%", row.CPU.UsedPercent)
	}
	if got := concernOf(t, row, "cpu-exhausted"); got.Severity != severityDanger {
		t.Errorf("a fully reserved node is a danger, got %q", got.Severity)
	}
	if row.Severity != severityDanger {
		t.Errorf("node severity = %q, want danger", row.Severity)
	}
}

func TestConcernsWarnBeforeANodeIsFull(t *testing.T) {
	rows, _, _ := buildCapacity([]nodeRecord{fourCoreNode("n1")},
		[]capacityPod{podOn("n1", "shop", "api",
			container("app", map[string]string{"cpu": "3800m"}, map[string]string{"cpu": "3800m"}))},
		nil)

	if got := concernOf(t, rows[0], "cpu-committed"); got.Severity != severityWarn {
		t.Errorf("95%% reserved is a warning, got %q", got.Severity)
	}
	if hasConcern(rows[0], "cpu-exhausted") {
		t.Error("95%% is not 100%%: a node with room left must not read as exhausted")
	}
}

// The asymmetry that makes the two thresholds different numbers: CPU over its
// share is throttled, memory over its share is killed.
func TestOvercommitThresholdsDifferByResource(t *testing.T) {
	node := fourCoreNode("n1")
	rows, _, _ := buildCapacity([]nodeRecord{node},
		[]capacityPod{podOn("n1", "shop", "api",
			container("app",
				map[string]string{"cpu": "500m", "memory": "1Gi"},
				// 125% of the node's CPU, 125% of its memory.
				map[string]string{"cpu": "5", "memory": "10Gi"}))},
		nil)

	row := rows[0]
	if hasConcern(row, "cpu-overcommitted") {
		t.Error("CPU at 125% is contention the kernel absorbs, and must not fire")
	}
	memory := concernOf(t, row, "memory-overcommitted")
	if memory.Severity != severityWarn {
		t.Errorf("memory over the node's own size is a warning, got %q", memory.Severity)
	}
	if !strings.Contains(memory.Detail, "evicting") {
		t.Errorf("the memory case must say what happens — eviction — got %q", memory.Detail)
	}
}

// Right-sizing needs live usage to mean anything. A cluster with no
// metrics-server must see nothing here rather than see it wrongly.
func TestReservedIdleNeedsLiveUsage(t *testing.T) {
	node := fourCoreNode("n1")
	pods := []capacityPod{podOn("n1", "shop", "api",
		container("app", map[string]string{"cpu": "3"}, nil))}

	withUsage, _, _ := buildCapacity([]nodeRecord{node}, pods,
		map[string]nodeSize{"n1": {cpu: 600}})
	if got := concernOf(t, withUsage[0], "cpu-reserved-idle"); got.Severity != severityNote {
		t.Errorf("reserved and unspent is money rather than an incident, got %q", got.Severity)
	}
	if withUsage[0].Severity != severityNote {
		t.Errorf("a note must not lift a node past a warning, got %q", withUsage[0].Severity)
	}

	withoutUsage, _, _ := buildCapacity([]nodeRecord{node}, pods, nil)
	if hasConcern(withoutUsage[0], "cpu-reserved-idle") {
		t.Error("with no usage to compare against, a reservation cannot be called idle")
	}
}

func TestPodSlotsCapANodeBeforeItsCPUDoes(t *testing.T) {
	node := fourCoreNode("n1")
	node.PodSlots = 4
	pods := make([]capacityPod, 0, 4)
	for _, name := range []string{"a", "b", "c", "d"} {
		pods = append(pods, podOn("n1", "shop", name, container("app", map[string]string{"cpu": "1m"}, nil)))
	}

	rows, _, _ := buildCapacity([]nodeRecord{node}, pods, nil)
	if got := concernOf(t, rows[0], "pod-slots-exhausted"); got.Severity != severityDanger {
		t.Errorf("a node with no pod slots left takes nothing more, got %q", got.Severity)
	}
	if rows[0].CPU.RequestedPercent > 1 {
		t.Fatal("this node's CPU is untouched — the ceiling here is the kubelet's own")
	}
}

func TestConcernsForACordonedAndUnreadyNode(t *testing.T) {
	node := fourCoreNode("n1")
	node.Ready = false
	node.Unschedulable = true

	rows, summary, _ := buildCapacity([]nodeRecord{node}, nil, nil)
	if !hasConcern(rows[0], "not-ready") || !hasConcern(rows[0], "unschedulable") {
		t.Fatalf("both facts must be stated, got %+v", rows[0].Concerns)
	}
	// Hardest first, so the page can lead with the worst line of each node.
	if rows[0].Concerns[0].Code != "not-ready" {
		t.Errorf("concerns must be ordered by severity, got %q first", rows[0].Concerns[0].Code)
	}
	if summary.Ready != 0 || summary.Schedulable != 0 || summary.SeverityCounts[severityDanger] != 1 {
		t.Errorf("summary must count what it found: %+v", summary)
	}
}

/* ------------------------------------------------------------- the route --- */

// A completed pod still exists and still lists, but it holds nothing: the
// scheduler released its reservation when it finished.
func TestSchedulablePodsPathExcludesFinishedPods(t *testing.T) {
	got := schedulablePodsPath()
	if !strings.HasPrefix(got, "/api/v1/pods?") {
		t.Fatalf("capacity reads pods cluster-wide, got %q", got)
	}
	for _, phase := range []string{"Succeeded", "Failed"} {
		if !strings.Contains(got, "status.phase%21%3D"+phase) {
			t.Errorf("%s must be filtered out server-side, got %q", phase, got)
		}
	}
}

func TestParsePodSlots(t *testing.T) {
	cases := map[string]int64{"110": 110, "": 0, "250": 250, "not-a-value": 0}
	for raw, want := range cases {
		if got := parsePodSlots(raw); got != want {
			t.Errorf("parsePodSlots(%q) = %d, want %d", raw, got, want)
		}
	}
}

// The route is a resource read like the ones beside it, and inherits the same
// guards: agent-only, authenticated, and — because node capacity reaches well
// past any one namespace — refused to a namespace-scoped grant.
func TestCapacityRouteSharesTheResourceGuards(t *testing.T) {
	t.Run("a scoped grant is refused", func(t *testing.T) {
		env := newTestEnv(t)
		user := env.store.addUser("scoped", "secret123", "user")
		cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
		env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
		token := env.tokenFor(t, user)

		rec := env.do(t, http.MethodGet,
			"/api/v1/clusters/"+itoa(cluster.ID)+"/metrics/capacity", token, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected %d for a scoped grant, got %d (%s)",
				http.StatusForbidden, rec.Code, rec.Body.String())
		}
	})

	t.Run("direct clusters have no live state", func(t *testing.T) {
		env := newTestEnv(t)
		admin := env.store.addUser("admin", "secret123", "admin")
		cluster := env.store.addCluster("legacy", "dev")
		token := env.tokenFor(t, admin)

		rec := env.do(t, http.MethodGet,
			"/api/v1/clusters/"+itoa(cluster.ID)+"/metrics/capacity", token, nil)
		if rec.Code != http.StatusConflict {
			t.Fatalf("expected %d for a direct-mode cluster, got %d (%s)",
				http.StatusConflict, rec.Code, rec.Body.String())
		}
	})

	t.Run("authentication is required", func(t *testing.T) {
		env := newTestEnv(t)
		cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

		rec := env.do(t, http.MethodGet,
			"/api/v1/clusters/"+itoa(cluster.ID)+"/metrics/capacity", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected %d without a token, got %d", http.StatusUnauthorized, rec.Code)
		}
	})
}
