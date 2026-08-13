package api

import (
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Costing a cluster.
 *
 * What is pinned here is the arithmetic and the two totals that deliberately do
 * not match, because that distinction is the feature: a report where the
 * workloads add up to the bill has quietly spread unallocated capacity across
 * teams, and the per-team number then moves when a *different* team scales down.
 *
 * The transport is shared with every other resource read and is covered there.
 */

// rates is a rate card with numbers chosen to make the arithmetic checkable by
// hand: one core-hour is a dollar, one GiB-hour is a dollar, and a month is 730
// hours. So one core reserved for a month is $730, exactly.
func rates() db.RateCard {
	return db.RateCard{
		Provider: db.RateProviderCustom, Currency: "USD",
		CPUCoreHour: 1, MemoryGiBHour: 1,
		StorageGiBMonth: 1, LoadBalancerMonth: 10,
	}
}

func nodeOf(name string, cores int64, gib int64) nodeRecord {
	return nodeRecord{
		Name:        name,
		Ready:       true,
		Allocatable: nodeSize{cpu: cores * 1000, memory: gib << 30},
		PodSlots:    110,
	}
}

// ownedBy stamps a controller reference onto a pod, which is how every pod that
// belongs to something arrives from the API server.
func ownedBy(pod capacityPod, kind, name string) capacityPod {
	controller := true
	pod.Metadata.OwnerReferences = []ownerRef{{Kind: kind, Name: name, Controller: &controller}}
	return pod
}

/* ------------------------------------------------------------- the totals -- */

func TestBuildCostSeparatesInfrastructureFromAttribution(t *testing.T) {
	nodes := []nodeRecord{nodeOf("n1", 4, 16)}
	pods := []capacityPod{
		podOn("n1", "shop", "api-1", container("app", map[string]string{"cpu": "1", "memory": "4Gi"}, nil)),
	}

	report := buildCost(rates(), nodes, pods, replicaSetOwners{}, nil)

	// Four cores and sixteen GiB, at a dollar an hour each, for 730 hours.
	if want := float64(4 * 730); report.summary.Infrastructure.CPU != want {
		t.Errorf("infrastructure CPU = %v, want %v", report.summary.Infrastructure.CPU, want)
	}
	if want := float64(16 * 730); report.summary.Infrastructure.Memory != want {
		t.Errorf("infrastructure memory = %v, want %v", report.summary.Infrastructure.Memory, want)
	}
	if want := float64(20 * 730); report.summary.Infrastructure.Total != want {
		t.Errorf("infrastructure total = %v, want %v", report.summary.Infrastructure.Total, want)
	}

	// One core and four GiB reserved.
	if want := float64(5 * 730); report.summary.Attributed.Total != want {
		t.Errorf("attributed = %v, want %v", report.summary.Attributed.Total, want)
	}
	// The gap is its own line and is not spread over the workloads.
	if want := float64(15 * 730); report.summary.Unallocated.Total != want {
		t.Errorf("unallocated = %v, want %v", report.summary.Unallocated.Total, want)
	}
	if report.summary.AttributedPercent != 25 {
		t.Errorf("attributed percent = %v, want 25", report.summary.AttributedPercent)
	}
}

func TestBuildCostNeverReportsNegativeUnallocated(t *testing.T) {
	// An oversubscribed cluster: more reserved than the node can allocate. That
	// is the capacity report's finding, and a negative amount of money would be
	// this report's way of getting it wrong.
	nodes := []nodeRecord{nodeOf("n1", 1, 1)}
	pods := []capacityPod{
		podOn("n1", "shop", "hog", container("app", map[string]string{"cpu": "4", "memory": "8Gi"}, nil)),
	}

	report := buildCost(rates(), nodes, pods, replicaSetOwners{}, nil)

	if report.summary.Unallocated.Total != 0 {
		t.Errorf("unallocated = %v, want 0 on an oversubscribed cluster",
			report.summary.Unallocated.Total)
	}
	if report.summary.AttributedPercent <= 100 {
		t.Errorf("attributed percent = %v, want above 100 — the oversubscription must stay visible",
			report.summary.AttributedPercent)
	}
}

func TestBuildCostSkipsUnscheduledPods(t *testing.T) {
	// A pod with nowhere to go has reserved nothing on any node. Costing it
	// would price capacity that was never bought.
	pending := podOn("", "shop", "waiting",
		container("app", map[string]string{"cpu": "2", "memory": "2Gi"}, nil))

	report := buildCost(rates(), []nodeRecord{nodeOf("n1", 4, 16)},
		[]capacityPod{pending}, replicaSetOwners{}, nil)

	if report.summary.Attributed.Total != 0 {
		t.Errorf("attributed = %v, want 0 — an unplaced pod costs nothing yet",
			report.summary.Attributed.Total)
	}
	if len(report.workloads) != 0 {
		t.Errorf("workloads = %d, want none", len(report.workloads))
	}
}

/* ---------------------------------------------------- the workload rollup -- */

func TestWorkloadOfWalksReplicaSetToDeployment(t *testing.T) {
	// A pod's controller is a ReplicaSet named after a hash nobody deploys.
	// Reporting that name would make the cost table a list of strangers.
	controller := true
	pod := ownedBy(podOn("n1", "shop", "api-7d4b9c8f5-xk2vq"), "ReplicaSet", "api-7d4b9c8f5")
	owners := replicaSetOwners{
		"shop/api-7d4b9c8f5": {Kind: "Deployment", Name: "api", Controller: &controller},
	}

	kind, name := workloadOf(pod, owners)
	if kind != "Deployment" || name != "api" {
		t.Errorf("workloadOf = %s/%s, want Deployment/api", kind, name)
	}
}

func TestWorkloadOfKeepsAReplicaSetNobodyOwns(t *testing.T) {
	// A ReplicaSet created directly is a workload in its own right. Stripping
	// the hash off its name is the shortcut that gets this wrong.
	pod := ownedBy(podOn("n1", "shop", "standalone-abc"), "ReplicaSet", "standalone")

	kind, name := workloadOf(pod, replicaSetOwners{})
	if kind != "ReplicaSet" || name != "standalone" {
		t.Errorf("workloadOf = %s/%s, want ReplicaSet/standalone", kind, name)
	}
}

func TestWorkloadOfNamesABarePod(t *testing.T) {
	// An unmanaged pod holding a node is exactly what a cost report should
	// surface, so it is named rather than dropped.
	kind, name := workloadOf(podOn("n1", "shop", "debug-shell"), replicaSetOwners{})
	if kind != "Pod" || name != "debug-shell" {
		t.Errorf("workloadOf = %s/%s, want Pod/debug-shell", kind, name)
	}
}

func TestControllerOfIgnoresANonControllerOwner(t *testing.T) {
	// An object can carry several owners and at most one is the controller.
	// Taking the first would attribute a pod to whatever was listed first.
	controller := true
	refs := []ownerRef{
		{Kind: "SomethingElse", Name: "bystander"},
		{Kind: "DaemonSet", Name: "node-agent", Controller: &controller},
	}

	owner, found := controllerOf(refs)
	if !found || owner.Name != "node-agent" {
		t.Errorf("controllerOf = %+v (found %v), want the DaemonSet", owner, found)
	}
}

func TestBuildCostGroupsPodsIntoOneWorkload(t *testing.T) {
	controller := true
	owners := replicaSetOwners{
		"shop/api-abc": {Kind: "Deployment", Name: "api", Controller: &controller},
	}
	spec := container("app", map[string]string{"cpu": "500m", "memory": "1Gi"}, nil)
	pods := []capacityPod{
		ownedBy(podOn("n1", "shop", "api-abc-1", spec), "ReplicaSet", "api-abc"),
		ownedBy(podOn("n1", "shop", "api-abc-2", spec), "ReplicaSet", "api-abc"),
	}

	report := buildCost(rates(), []nodeRecord{nodeOf("n1", 8, 32)}, pods, owners, nil)

	if len(report.workloads) != 1 {
		t.Fatalf("workloads = %d, want 1 — two replicas are one Deployment", len(report.workloads))
	}
	got := report.workloads[0]
	if got.Kind != "Deployment" || got.Name != "api" || got.Pods != 2 {
		t.Errorf("workload = %+v, want Deployment api with 2 pods", got)
	}
	// One core and two GiB across the two replicas.
	if want := money(3 * 730); got.Monthly.Total != want {
		t.Errorf("monthly = %v, want %v", got.Monthly.Total, want)
	}
}

/* --------------------------------------------------------- reserved-idle -- */

func TestBuildCostPricesIdlePerResource(t *testing.T) {
	// A workload overspending its CPU reservation while underspending its
	// memory one has idle memory and no idle CPU. Netting the two off would
	// report it as efficient while it is wrong in both directions.
	pods := []capacityPod{
		podOn("n1", "shop", "api",
			container("app", map[string]string{"cpu": "1", "memory": "8Gi"}, nil)),
	}
	usage := map[podUsageKey]nodeSize{
		{"shop", "api"}: {cpu: 2000, memory: 2 << 30},
	}

	report := buildCost(rates(), []nodeRecord{nodeOf("n1", 8, 32)}, pods, replicaSetOwners{}, usage)

	got := report.workloads[0]
	if !got.Used {
		t.Fatal("usage was supplied, so the workload must read as measured")
	}
	if got.Idle.CPU != 0 {
		t.Errorf("idle CPU = %v, want 0 — it is spending more than it reserved", got.Idle.CPU)
	}
	if want := money(6 * 730); got.Idle.Memory != want {
		t.Errorf("idle memory = %v, want %v", got.Idle.Memory, want)
	}
}

func TestBuildCostWithoutUsageNeverClaimsAWorkloadIsIdle(t *testing.T) {
	// Without metrics-server nobody's usage is known, and `used: false` beside
	// `used: true` would read as "this one is idle" rather than "not measured".
	pods := []capacityPod{
		podOn("n1", "shop", "api", container("app", map[string]string{"cpu": "1"}, nil)),
	}

	report := buildCost(rates(), []nodeRecord{nodeOf("n1", 4, 8)}, pods, replicaSetOwners{}, nil)

	if report.workloads[0].Used {
		t.Error("no usage was supplied, so no workload may claim to have been measured")
	}
	if report.summary.Idle.Total != 0 {
		t.Errorf("idle total = %v, want 0 without usage", report.summary.Idle.Total)
	}
}

/* ------------------------------------------------------------ the tables -- */

func TestFinishWorkloadsRanksByMoneyAndCuts(t *testing.T) {
	all := []costedWorkload{}
	for i := 0; i < topCostedWorkloads+10; i++ {
		all = append(all, costedWorkload{
			Namespace: "shop", Name: string(rune('a' + i%26)),
			Monthly: moneyDimension{Total: float64(i + 1)},
		})
	}

	got := finishWorkloads(all, false)

	if len(got) != topCostedWorkloads {
		t.Fatalf("rows = %d, want %d", len(got), topCostedWorkloads)
	}
	if got[0].Monthly.Total <= got[1].Monthly.Total {
		t.Error("the table must be ranked by what it is a table of — money, biggest first")
	}
}

func TestFinishWorkloadsDropsRoundingErrors(t *testing.T) {
	// A DaemonSet reserving nothing measurable is a real workload and a
	// rounding error in this report.
	got := finishWorkloads([]costedWorkload{
		{Namespace: "kube-system", Name: "tiny", Monthly: moneyDimension{Total: 0.001}},
		{Namespace: "shop", Name: "real", Monthly: moneyDimension{Total: 500}},
	}, false)

	if len(got) != 1 || got[0].Name != "real" {
		t.Errorf("rows = %+v, want only the workload that costs something", got)
	}
}

/* ----------------------------------------------------------- the currency -- */

func TestPriceOfConvertsFromKubeMGsOwnUnits(t *testing.T) {
	// Millicores and bytes in, a price list's units out. A factor of a thousand
	// here is the classic way a cost report becomes absurd.
	got := priceOf(rates(), 500, 512<<20)

	if want := 0.5 * 730; got.CPU != want {
		t.Errorf("CPU = %v, want %v — 500m is half a core", got.CPU, want)
	}
	if want := 0.5 * 730; got.Memory != want {
		t.Errorf("memory = %v, want %v — 512Mi is half a GiB", got.Memory, want)
	}
}

func TestMoneySurvivesAnUnrepresentableAmount(t *testing.T) {
	// Rates are validated on the way in, but arithmetic over a cluster is not
	// somewhere a NaN should ever reach a JSON body.
	if got := money(math.NaN()); got != 0 {
		t.Errorf("money(NaN) = %v, want 0", got)
	}
	if got := money(math.Inf(1)); got != 0 {
		t.Errorf("money(+Inf) = %v, want 0", got)
	}
}

func TestRatioReadsAnUnknownDenominatorAsZero(t *testing.T) {
	if got := ratio(5, 0); got != 0 {
		t.Errorf("ratio(5, 0) = %v, want 0 rather than a division", got)
	}
}

/* ------------------------------------------------------------- the guard -- */

// The three reads join the same guard chain as every other resource read:
// agent-only, authenticated, and — because a node's price says nothing about a
// namespace and reaches well past one — refused to a namespace-scoped grant.
func TestCostRoutesShareTheResourceGuards(t *testing.T) {
	for _, route := range []string{"cost", "waste", "rightsizing"} {
		t.Run(route+": a scoped grant is refused", func(t *testing.T) {
			env := newTestEnv(t)
			user := env.store.addUser("scoped", "secret123", "user")
			cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
			env.store.grant(user.ID, cluster.ID, "view", []string{"team-a"})
			token := env.tokenFor(t, user)

			rec := env.do(t, http.MethodGet,
				"/api/v1/clusters/"+itoa(cluster.ID)+"/metrics/"+route, token, nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("expected %d for a scoped grant, got %d (%s)",
					http.StatusForbidden, rec.Code, rec.Body.String())
			}
		})

		t.Run(route+": direct clusters have no live state", func(t *testing.T) {
			env := newTestEnv(t)
			admin := env.store.addUser("admin", "secret123", "admin")
			cluster := env.store.addCluster("legacy", "dev")
			token := env.tokenFor(t, admin)

			rec := env.do(t, http.MethodGet,
				"/api/v1/clusters/"+itoa(cluster.ID)+"/metrics/"+route, token, nil)
			if rec.Code != http.StatusConflict {
				t.Fatalf("expected %d for a direct-mode cluster, got %d (%s)",
					http.StatusConflict, rec.Code, rec.Body.String())
			}
		})

		t.Run(route+": authentication is required", func(t *testing.T) {
			env := newTestEnv(t)
			cluster := env.store.addAgentCluster("edge", "dev", "agent-token")

			rec := env.do(t, http.MethodGet,
				"/api/v1/clusters/"+itoa(cluster.ID)+"/metrics/"+route, "", nil)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected %d without a token, got %d", http.StatusUnauthorized, rec.Code)
			}
		})
	}
}

// An unpriced fleet is told it is unpriced, and it costs no cluster reads to
// say so — reporting zeroes would be a report, and inventing a rate would be
// worse than either.
func TestClusterCostAnswersUnpricedWithoutReadingTheCluster(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/metrics/cost", token, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an unpriced fleet is an answer, not a failure", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"priced":false`) {
		t.Errorf("body = %s, want priced:false", body)
	}
}
