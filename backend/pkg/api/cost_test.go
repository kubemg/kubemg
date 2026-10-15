package api

import (
	"errors"
	"math"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

/*
 * Cost is requests times the operator's rate. Pinned here: the reservation is
 * the scheduler's arithmetic and counts only placed pods, a month is 730 hours,
 * and a category without a rate is absent and named — never priced at zero.
 */

func floatRef(v float64) *float64 { return &v }

func testRates() *db.ClusterCostRates {
	return &db.ClusterCostRates{Currency: "EUR", CPUCoreHour: 0.04, MemoryGiBHour: 0.005}
}

func placed(pod wastePod) wastePod {
	pod.Spec.NodeName = "node-a"
	return pod
}

func near(got, want float64) bool { return math.Abs(got-want) < 0.005 }

func TestReservedTotalsByNamespaceAndWorkload(t *testing.T) {
	app := container("app", requests("500m", "1Gi"), nil)
	pods := []wastePod{
		placed(wasteDeploymentPod("shop", "web", "a", time.Hour, app)),
		placed(wasteDeploymentPod("shop", "web", "b", time.Hour, app)),
		placed(wasteDeploymentPod("shop", "api", "a", time.Hour, container("app", requests("2", "512Mi"), nil))),
		placed(wasteDeploymentPod("ops", "agent", "a", time.Hour, container("app", requests("100m", "128Mi"), nil))),
	}

	report := buildReserved(pods, nil)
	if report.Pods != 4 || report.CPU != 3100 || report.Memory != (2048+512+128)*mi {
		t.Fatalf("totals = %d pods, %dm, %d bytes", report.Pods, report.CPU, report.Memory)
	}
	if report.Monthly != nil {
		t.Errorf("no rates prices nothing, got %v", *report.Monthly)
	}
	if len(report.Namespaces) != 2 || report.Namespaces[0].Namespace != "shop" {
		t.Fatalf("namespaces = %+v", report.Namespaces)
	}
	shop := report.Namespaces[0]
	if shop.Pods != 3 || shop.CPU != 3000 || shop.WorkloadsTotal != 2 {
		t.Errorf("shop = %+v", shop)
	}
	// api holds 65% of the cluster's CPU; web 32% of its CPU but 76% of its
	// memory. The larger of the two shares orders them, so web comes first.
	if shop.Workloads[0].Name != "web" || shop.Workloads[0].Pods != 2 || shop.Workloads[0].CPU != 1000 {
		t.Errorf("workloads = %+v", shop.Workloads)
	}
}

func TestReservedUsesTheSchedulersArithmetic(t *testing.T) {
	// A sidecar runs beside the app; a plain init step needs more than both but
	// only while it runs; the runtime adds overhead on top.
	pod := placed(wasteDeploymentPod("shop", "web", "a", time.Hour,
		container("app", requests("200m", "256Mi"), nil)))
	pod.Spec.InitContainers = []capacityInitContainer{
		{capacityContainer: container("proxy", requests("100m", "64Mi"), nil), RestartPolicy: restartAlways},
		{capacityContainer: container("migrate", requests("1", "128Mi"), nil)},
	}
	pod.Spec.Overhead = map[string]string{"cpu": "50m", "memory": "16Mi"}

	want := demandOf(pod.capacityPod())
	report := buildReserved([]wastePod{pod}, nil)
	if report.CPU != want.cpuRequest || report.Memory != want.memoryRequest {
		t.Fatalf("reserved %dm/%d, the capacity page says %dm/%d",
			report.CPU, report.Memory, want.cpuRequest, want.memoryRequest)
	}
	// max(200+100, 100+1000) + 50 overhead.
	if report.CPU != 1150 {
		t.Errorf("cpu = %dm, want 1150m", report.CPU)
	}
}

func TestReservedLeavesOutPodsWaitingForANode(t *testing.T) {
	waiting := wasteDeploymentPod("shop", "web", "a", time.Hour, container("app", requests("4", "8Gi"), nil))
	report := buildReserved([]wastePod{waiting}, testRates())
	if report.CPU != 0 || report.Pods != 0 || report.Unscheduled != 1 || len(report.Namespaces) != 0 {
		t.Errorf("an unplaced pod reserves nothing: %+v", report)
	}
}

func TestReservedNamesABarePodAndAJobByThemselves(t *testing.T) {
	bare := placed(wasteDeploymentPod("shop", "debug", "a", time.Hour, container("app", requests("100m", ""), nil)))
	bare.Metadata.OwnerReferences = nil
	job := placed(wasteDeploymentPod("shop", "batch", "a", time.Hour, container("app", requests("100m", ""), nil)))
	job.Metadata.OwnerReferences = []ownerRef{{Kind: "Job", Name: "nightly", Controller: boolRef(true)}}

	report := buildReserved([]wastePod{bare, job}, nil)
	kinds := []string{}
	for _, workload := range report.Namespaces[0].Workloads {
		kinds = append(kinds, workload.Kind+"/"+workload.Name)
	}
	slices.Sort(kinds)
	if !slices.Equal(kinds, []string{"Job/nightly", "Pod/" + bare.Metadata.Name}) {
		t.Errorf("workloads = %v", kinds)
	}
}

func TestReservedCapsRowsAndKeepsCountsExact(t *testing.T) {
	pods := []wastePod{}
	for i := range maxReservedWorkloads + 5 {
		pods = append(pods, placed(wasteDeploymentPod("shop", "web"+itoa(uint(i)), "a", time.Hour,
			container("app", requests("100m", ""), nil))))
	}
	report := buildReserved(pods, nil)
	shop := report.Namespaces[0]
	if len(shop.Workloads) != maxReservedWorkloads || shop.WorkloadsTotal != maxReservedWorkloads+5 {
		t.Errorf("rows %d of %d", len(shop.Workloads), shop.WorkloadsTotal)
	}
	if shop.CPU != int64(maxReservedWorkloads+5)*100 {
		t.Errorf("the namespace total counts every workload, got %dm", shop.CPU)
	}
}

func TestReservedIsPricedAtTheOperatorsRate(t *testing.T) {
	// Two cores and 4 GiB for 730 hours: (2×0.04 + 4×0.005) × 730 = 73.
	pod := placed(wasteDeploymentPod("shop", "web", "a", time.Hour, container("app", requests("2", "4Gi"), nil)))
	report := buildReserved([]wastePod{pod}, testRates())
	for name, figure := range map[string]*float64{
		"cluster":   report.Monthly,
		"namespace": report.Namespaces[0].Monthly,
		"workload":  report.Namespaces[0].Workloads[0].Monthly,
	} {
		if figure == nil || !near(*figure, 73) {
			t.Errorf("%s monthly = %v, want 73", name, figure)
		}
	}
}

func TestPriceWasteNamesWhatItCouldNotPrice(t *testing.T) {
	app := container("app", requests("1", "1Gi"), nil)
	pods := []wastePod{placed(wasteDeploymentPod("shop", "web", "a", 72*time.Hour, app))}
	usage := map[observability.UsageKey]observability.UsageProfile{
		usageKey("shop", pods[0].Metadata.Name, "app"): profile(100, float64(100*mi)),
	}
	claim := wasteClaimOf("shop", "orphan", "Bound", "10Gi")
	in := wasteInputs{
		now: wasteNow, pods: pods, usage: usage, rates: testRates(),
		claims: []wasteClaim{claim}, slicesRead: true,
		services: []wasteService{lbService("shop", "edge", map[string]string{"app": "gone"})},
	}

	report := buildWaste(in)
	monthly := report.Summary.Monthly
	if monthly == nil {
		t.Fatal("rates set, so the summary is priced")
	}
	if monthly.UnusedStorage != nil || monthly.IdleLoadBalancers != nil {
		t.Errorf("no storage or LB rate prices neither: %+v", monthly)
	}
	if !slices.Equal(monthly.Unpriced, []string{"storage", "load_balancers"}) {
		t.Errorf("unpriced = %v", monthly.Unpriced)
	}
	if report.Volumes[0].Monthly != nil || report.LoadBalancers[0].Monthly != nil {
		t.Error("a row without its rate carries no figure")
	}
	workload := report.Workloads[0]
	want := computeMonthly(workload.Reclaimable.CPU, workload.Reclaimable.Memory, *testRates())
	if workload.Monthly == nil || !near(*workload.Monthly, want) || !near(monthly.Total, want) {
		t.Errorf("reclaimable priced %v (total %v), want %v", workload.Monthly, monthly.Total, want)
	}

	// With every rate: 10 GiB at 0.08 a month, one LB at 0.025 an hour.
	in.rates.StorageGiBMonth = floatRef(0.08)
	in.rates.LoadBalancerHour = floatRef(0.025)
	report = buildWaste(in)
	monthly = report.Summary.Monthly
	if len(monthly.Unpriced) != 0 {
		t.Errorf("everything priced, unpriced = %v", monthly.Unpriced)
	}
	if !near(*monthly.UnusedStorage, 0.8) || !near(*report.Volumes[0].Monthly, 0.8) {
		t.Errorf("storage = %v", *monthly.UnusedStorage)
	}
	if !near(*monthly.IdleLoadBalancers, 18.25) || !near(*report.LoadBalancers[0].Monthly, 18.25) {
		t.Errorf("load balancers = %v", *monthly.IdleLoadBalancers)
	}
	if !near(monthly.Total, want+0.8+18.25) {
		t.Errorf("total = %v", monthly.Total)
	}
}

func TestPriceWasteWithoutRatesAddsNoMoney(t *testing.T) {
	claim := wasteClaimOf("shop", "orphan", "Bound", "10Gi")
	report := buildWaste(wasteInputs{now: wasteNow, claims: []wasteClaim{claim}, slicesRead: true})
	if report.Summary.Monthly != nil || report.Volumes[0].Monthly != nil {
		t.Error("no rates, no money")
	}
}

func TestWasteRatesNeverFailTheReport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := newFakeStore()
	s := &server{store: store}
	context := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/", nil)
		return c
	}

	if rates, reason := s.wasteRates(context(), 7); rates != nil || reason != "" {
		t.Errorf("none set is no money and no reason, got %v %q", rates, reason)
	}
	store.costRates[7] = *testRates()
	if rates, _ := s.wasteRates(context(), 7); rates == nil || rates.Currency != "EUR" {
		t.Errorf("rates = %+v", rates)
	}
	store.costRatesErr = errors.New("connection refused")
	if rates, reason := s.wasteRates(context(), 7); rates != nil || reason == "" {
		t.Errorf("an unreadable store is no money with a reason, got %v %q", rates, reason)
	}
}
