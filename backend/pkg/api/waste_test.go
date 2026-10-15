package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/observability"
)

/*
 * The waste report is arithmetic over four reads, and the arithmetic is the
 * feature: which container counts as over-asking, what it is told to ask for
 * instead, and which reasons keep a workload off the list. All of it is pinned
 * here on buildWaste, a pure function of the reads.
 */

var wasteNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

const mi = int64(1 << 20)

func boolRef(v bool) *bool { return &v }

// wasteDeploymentPod builds a running pod of a Deployment, `age` old.
func wasteDeploymentPod(namespace, deployment, suffix string, age time.Duration,
	containers ...capacityContainer,
) wastePod {
	var pod wastePod
	pod.Metadata.Namespace = namespace
	pod.Metadata.Name = deployment + "-abc12-" + suffix
	pod.Metadata.CreationTimestamp = wasteNow.Add(-age)
	pod.Metadata.Labels = map[string]string{"pod-template-hash": "abc12"}
	pod.Metadata.OwnerReferences = []ownerRef{{
		Kind: "ReplicaSet", Name: deployment + "-abc12", Controller: boolRef(true),
	}}
	pod.Spec.Containers = containers
	return pod
}

func profile(cpu, memory float64) observability.UsageProfile {
	return observability.UsageProfile{CPU: &cpu, Memory: &memory}
}

func usageKey(namespace, pod, container string) observability.UsageKey {
	return observability.UsageKey{Namespace: namespace, Pod: pod, Container: container}
}

func requests(cpu, memory string) map[string]string {
	out := map[string]string{}
	if cpu != "" {
		out["cpu"] = cpu
	}
	if memory != "" {
		out["memory"] = memory
	}
	return out
}

func TestWasteListsAnOverRequestedDeploymentWithItsRecommendation(t *testing.T) {
	app := container("app", requests("1", "1Gi"), nil)
	pods := []wastePod{
		wasteDeploymentPod("shop", "web", "a", 72*time.Hour, app),
		wasteDeploymentPod("shop", "web", "b", 48*time.Hour, app),
	}
	usage := map[observability.UsageKey]observability.UsageProfile{
		usageKey("shop", pods[0].Metadata.Name, "app"): profile(80, float64(200*mi)),
		// The busier replica sets the reading: a request covers every replica.
		usageKey("shop", pods[1].Metadata.Name, "app"): profile(120, float64(150*mi)),
	}

	report := buildWaste(wasteInputs{now: wasteNow, pods: pods, usage: usage})
	if len(report.Workloads) != 1 {
		t.Fatalf("expected one listed workload, got %+v", report.Workloads)
	}
	workload := report.Workloads[0]
	if workload.Kind != "Deployment" || workload.Name != "web" || workload.Replicas != 2 {
		t.Errorf("workload resolved as %s/%s ×%d", workload.Kind, workload.Name, workload.Replicas)
	}
	row := workload.Containers[0]

	// CPU: observed 120m, ×1.2 = 144m, rounded up to 145m.
	if *row.CPU.Observed != 120 || *row.CPU.Recommended != 145 || !row.CPU.Over {
		t.Errorf("cpu = %+v / %v / %v", *row.CPU.Observed, *row.CPU.Recommended, row.CPU.Over)
	}
	// Memory: peak 200Mi, ×1.2 = 240Mi.
	if *row.Memory.Observed != 200*mi || *row.Memory.Recommended != 240*mi || !row.Memory.Over {
		t.Errorf("memory = %d / %d / %v", *row.Memory.Observed, *row.Memory.Recommended, row.Memory.Over)
	}
	if workload.Reclaimable.CPU != (1000-145)*2 || workload.Reclaimable.Memory != (1024-240)*mi*2 {
		t.Errorf("reclaimable = %+v", workload.Reclaimable)
	}
	if report.Summary.ReclaimableCPU != workload.Reclaimable.CPU || report.Summary.WorkloadsChecked != 1 {
		t.Errorf("summary = %+v", report.Summary)
	}
}

func TestWasteLeavesARequestWithinTheRatioAlone(t *testing.T) {
	// 300m requested, 220m used: 300 < 1.5×220, so not over-asking.
	pod := wasteDeploymentPod("shop", "api", "a", 72*time.Hour, container("app", requests("300m", ""), nil))
	usage := map[observability.UsageKey]observability.UsageProfile{
		usageKey("shop", pod.Metadata.Name, "app"): profile(220, float64(64*mi)),
	}
	report := buildWaste(wasteInputs{now: wasteNow, pods: []wastePod{pod}, usage: usage})
	if len(report.Workloads) != 0 || report.Summary.WorkloadsChecked != 1 {
		t.Errorf("a request inside the ratio is not waste: %+v", report)
	}
}

func TestWasteIgnoresSmallSavings(t *testing.T) {
	// 100m requested, 10m used: over the ratio, but the floor-sized saving of
	// 85m is below the listing threshold.
	pod := wasteDeploymentPod("shop", "tiny", "a", 72*time.Hour, container("app", requests("100m", ""), nil))
	usage := map[observability.UsageKey]observability.UsageProfile{
		usageKey("shop", pod.Metadata.Name, "app"): profile(10, float64(10*mi)),
	}
	report := buildWaste(wasteInputs{now: wasteNow, pods: []wastePod{pod}, usage: usage})
	if len(report.Workloads) != 0 {
		t.Errorf("85m across one replica is noise, got %+v", report.Workloads)
	}
}

func TestWasteNeverRecommendsBelowTheFloor(t *testing.T) {
	pod := wasteDeploymentPod("shop", "idle", "a", 72*time.Hour, container("app", requests("500m", "512Mi"), nil))
	usage := map[observability.UsageKey]observability.UsageProfile{
		usageKey("shop", pod.Metadata.Name, "app"): profile(0.2, float64(2*mi)),
	}
	report := buildWaste(wasteInputs{now: wasteNow, pods: []wastePod{pod}, usage: usage})
	row := report.Workloads[0].Containers[0]
	if *row.CPU.Recommended != wasteFloorCPU || *row.Memory.Recommended != wasteFloorMemory {
		t.Errorf("floors not applied: %d / %d", *row.CPU.Recommended, *row.Memory.Recommended)
	}
}

func TestWasteDoesNotSizeAYoungWorkload(t *testing.T) {
	pod := wasteDeploymentPod("shop", "new", "a", 3*time.Hour, container("app", requests("4", "8Gi"), nil))
	usage := map[observability.UsageKey]observability.UsageProfile{
		usageKey("shop", pod.Metadata.Name, "app"): profile(10, float64(10*mi)),
	}
	report := buildWaste(wasteInputs{now: wasteNow, pods: []wastePod{pod}, usage: usage})
	if len(report.Workloads) != 0 || report.Summary.WorkloadsShortHistory != 1 {
		t.Errorf("three hours of history sizes nothing: %+v", report)
	}
}

// A pod a month old has only as much history as the datasource kept. Sizing it
// from the hour since the datasource was registered would be a guess.
func TestWasteCountsHistoryFromTheFirstSampleNotThePodsAge(t *testing.T) {
	pod := wasteDeploymentPod("shop", "old", "a", 30*24*time.Hour, container("app", requests("4", "8Gi"), nil))
	fresh := profile(10, float64(10*mi))
	started := wasteNow.Add(-3 * time.Hour)
	fresh.Since = &started
	usage := map[observability.UsageKey]observability.UsageProfile{
		usageKey("shop", pod.Metadata.Name, "app"): fresh,
	}
	report := buildWaste(wasteInputs{now: wasteNow, pods: []wastePod{pod}, usage: usage})
	if len(report.Workloads) != 0 || report.Summary.WorkloadsShortHistory != 1 {
		t.Errorf("three hours of samples sizes nothing, whatever the pod's age: %+v", report)
	}

	settled := wasteNow.Add(-48 * time.Hour)
	fresh.Since = &settled
	usage[usageKey("shop", pod.Metadata.Name, "app")] = fresh
	report = buildWaste(wasteInputs{now: wasteNow, pods: []wastePod{pod}, usage: usage})
	if len(report.Workloads) != 1 || !report.Workloads[0].Since.Equal(settled) {
		t.Errorf("two days of samples is sized, from the first sample: %+v", report.Workloads)
	}
}

func TestWasteCountsAnUnmeasuredWorkloadRatherThanCallingItIdle(t *testing.T) {
	pod := wasteDeploymentPod("shop", "dark", "a", 72*time.Hour, container("app", requests("4", "8Gi"), nil))
	report := buildWaste(wasteInputs{now: wasteNow, pods: []wastePod{pod},
		usage: map[observability.UsageKey]observability.UsageProfile{}})
	if len(report.Workloads) != 0 || report.Summary.WorkloadsUnmeasured != 1 {
		t.Errorf("no series is unmeasured, never zero usage: %+v", report)
	}
}

func TestWasteDoesNotCallAContainerWithNoRequestOverRequested(t *testing.T) {
	pod := wasteDeploymentPod("shop", "free", "a", 72*time.Hour,
		container("app", requests("", "2Gi"), nil))
	usage := map[observability.UsageKey]observability.UsageProfile{
		usageKey("shop", pod.Metadata.Name, "app"): profile(5, float64(100*mi)),
	}
	report := buildWaste(wasteInputs{now: wasteNow, pods: []wastePod{pod}, usage: usage})
	row := report.Workloads[0].Containers[0]
	if row.CPU.Request != nil || row.CPU.Over {
		t.Errorf("a container that declares no CPU request is not over-asking: %+v", row.CPU)
	}
	if !row.Memory.Over {
		t.Error("its memory request still is")
	}
}

func TestWasteSizesSidecarsAndSkipsPlainInitContainers(t *testing.T) {
	pod := wasteDeploymentPod("shop", "mesh", "a", 72*time.Hour, container("app", requests("1", ""), nil))
	pod.Spec.InitContainers = []capacityInitContainer{
		{capacityContainer: container("migrate", requests("2", ""), nil)},
		{capacityContainer: container("proxy", requests("1", ""), nil), RestartPolicy: restartAlways},
	}
	usage := map[observability.UsageKey]observability.UsageProfile{
		usageKey("shop", pod.Metadata.Name, "app"):   profile(50, float64(50*mi)),
		usageKey("shop", pod.Metadata.Name, "proxy"): profile(20, float64(30*mi)),
	}
	report := buildWaste(wasteInputs{now: wasteNow, pods: []wastePod{pod}, usage: usage})
	names := []string{}
	for _, row := range report.Workloads[0].Containers {
		names = append(names, row.Name)
	}
	if len(names) != 2 || names[0] != "app" || names[1] != "proxy" {
		t.Errorf("sized containers = %v, want app and the proxy sidecar", names)
	}
	if !report.Workloads[0].Containers[1].Sidecar {
		t.Error("the proxy is marked as a sidecar")
	}
}

func TestWasteUsesAPodLevelReadingOnlyForASingleContainer(t *testing.T) {
	single := wasteDeploymentPod("shop", "solo", "a", 72*time.Hour, container("app", requests("1", ""), nil))
	double := wasteDeploymentPod("shop", "duo", "a", 72*time.Hour,
		container("app", requests("1", ""), nil), container("log", requests("1", ""), nil))
	usage := map[observability.UsageKey]observability.UsageProfile{
		usageKey("shop", single.Metadata.Name, ""): profile(50, float64(50*mi)),
		usageKey("shop", double.Metadata.Name, ""): profile(50, float64(50*mi)),
	}
	report := buildWaste(wasteInputs{now: wasteNow, pods: []wastePod{single, double}, usage: usage})
	if len(report.Workloads) != 1 || report.Workloads[0].Name != "solo" {
		t.Fatalf("only the single-container pod can be sized from a pod reading: %+v", report.Workloads)
	}
	if report.Summary.WorkloadsUnmeasured != 1 {
		t.Errorf("the two-container pod counts as unmeasured: %+v", report.Summary)
	}
}

func TestWasteMarksARolloutAndShowsTheNewestRequest(t *testing.T) {
	old := wasteDeploymentPod("shop", "web", "a", 96*time.Hour, container("app", requests("2", ""), nil))
	fresh := wasteDeploymentPod("shop", "web", "b", 30*time.Hour, container("app", requests("1", ""), nil))
	usage := map[observability.UsageKey]observability.UsageProfile{
		usageKey("shop", old.Metadata.Name, "app"):   profile(50, float64(50*mi)),
		usageKey("shop", fresh.Metadata.Name, "app"): profile(50, float64(50*mi)),
	}
	report := buildWaste(wasteInputs{now: wasteNow, pods: []wastePod{old, fresh}, usage: usage})
	workload := report.Workloads[0]
	if !workload.Rollout || *workload.Containers[0].CPU.Request != 1000 {
		t.Errorf("rollout=%v request=%d", workload.Rollout, *workload.Containers[0].CPU.Request)
	}
}

func TestWasteSkipsJobsAndBarePods(t *testing.T) {
	job := wasteDeploymentPod("shop", "batch", "a", 72*time.Hour, container("app", requests("4", ""), nil))
	job.Metadata.OwnerReferences = []ownerRef{{Kind: "Job", Name: "batch", Controller: boolRef(true)}}
	bare := wasteDeploymentPod("shop", "bare", "a", 72*time.Hour, container("app", requests("4", ""), nil))
	bare.Metadata.OwnerReferences = nil
	usage := map[observability.UsageKey]observability.UsageProfile{
		usageKey("shop", job.Metadata.Name, "app"):  profile(1, 1),
		usageKey("shop", bare.Metadata.Name, "app"): profile(1, 1),
	}
	report := buildWaste(wasteInputs{now: wasteNow, pods: []wastePod{job, bare}, usage: usage})
	if report.Summary.WorkloadsChecked != 0 {
		t.Errorf("jobs and bare pods are not sized: %+v", report.Summary)
	}
}

func TestWasteWithoutHistoryStillAnswersVolumesAndBalancers(t *testing.T) {
	var claim wasteClaim
	claim.Metadata.Namespace, claim.Metadata.Name = "shop", "orphan"
	claim.Status.Phase = "Bound"
	claim.Status.Capacity = map[string]string{"storage": "10Gi"}

	report := buildWaste(wasteInputs{now: wasteNow, claims: []wasteClaim{claim}, slicesRead: true})
	if len(report.Workloads) != 0 || report.Summary.WorkloadsChecked != 0 {
		t.Errorf("no history sizes nothing: %+v", report.Workloads)
	}
	if len(report.Volumes) != 1 || report.Summary.UnusedStorage != 10<<30 {
		t.Errorf("volumes are read from the cluster alone: %+v", report.Volumes)
	}
}

func wasteClaimOf(namespace, name, phase, size string) wasteClaim {
	var claim wasteClaim
	claim.Metadata.Namespace, claim.Metadata.Name = namespace, name
	claim.Status.Phase = phase
	claim.Status.Capacity = map[string]string{"storage": size}
	return claim
}

func TestUnusedVolumes(t *testing.T) {
	mounting := wasteDeploymentPod("shop", "web", "a", time.Hour, container("app", nil, nil))
	mounting.Spec.Volumes = append(mounting.Spec.Volumes, struct {
		PersistentVolumeClaim *struct {
			ClaimName string `json:"claimName"`
		} `json:"persistentVolumeClaim"`
	}{PersistentVolumeClaim: &struct {
		ClaimName string `json:"claimName"`
	}{ClaimName: "in-use"}})

	var set wasteStatefulSet
	set.Metadata.Namespace, set.Metadata.Name = "shop", "db"
	replicas := int32(2)
	set.Spec.Replicas = &replicas
	set.Spec.VolumeClaimTemplates = append(set.Spec.VolumeClaimTemplates, struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}{})
	set.Spec.VolumeClaimTemplates[0].Metadata.Name = "data"

	released := wastePersistentVolume{}
	released.Metadata.Name = "pv-released"
	released.Spec.Capacity = map[string]string{"storage": "50Gi"}
	released.Spec.ReclaimPolicy = "Retain"
	released.Spec.ClaimRef = &struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	}{"shop", "gone"}
	released.Status.Phase = "Released"
	bound := wastePersistentVolume{}
	bound.Metadata.Name = "pv-bound"
	bound.Status.Phase = "Bound"

	report := buildWaste(wasteInputs{
		now:  wasteNow,
		pods: []wastePod{mounting},
		claims: []wasteClaim{
			wasteClaimOf("shop", "in-use", "Bound", "1Gi"),
			wasteClaimOf("shop", "pending", "Pending", "1Gi"),
			wasteClaimOf("shop", "data-db-1", "Bound", "5Gi"),
			wasteClaimOf("shop", "data-db-3", "Bound", "5Gi"),
		},
		statefulSets: []wasteStatefulSet{set},
		volumes:      []wastePersistentVolume{released, bound},
	})

	byName := map[string]wasteVolume{}
	for _, row := range report.Volumes {
		byName[row.Name] = row
	}
	if _, listed := byName["in-use"]; listed {
		t.Error("a mounted claim is not unused")
	}
	if _, listed := byName["pending"]; listed {
		t.Error("an unbound claim holds no storage")
	}
	if row := byName["data-db-1"]; row.StatefulSet != "db" || row.ScaledDown {
		t.Errorf("data-db-1 is the StatefulSet's, inside its replicas: %+v", row)
	}
	if row := byName["data-db-3"]; row.StatefulSet != "db" || !row.ScaledDown {
		t.Errorf("data-db-3 was kept by a scale-down: %+v", row)
	}
	if row := byName["pv-released"]; row.State != "released" || row.Claim != "shop/gone" {
		t.Errorf("released volume = %+v", row)
	}
	if _, listed := byName["pv-bound"]; listed {
		t.Error("a bound volume is not unused")
	}
	if report.Volumes[0].Name != "pv-released" {
		t.Errorf("largest first, got %s", report.Volumes[0].Name)
	}
}

func lbService(namespace, name string, selector map[string]string) wasteService {
	var service wasteService
	service.Metadata.Namespace, service.Metadata.Name = namespace, name
	service.Spec.Type = "LoadBalancer"
	service.Spec.Selector = selector
	return service
}

func wasteSlice(namespace, service string, ready ...*bool) wasteEndpointSlice {
	var slice wasteEndpointSlice
	slice.Metadata.Namespace = namespace
	slice.Metadata.Labels = map[string]string{"kubernetes.io/service-name": service}
	for _, state := range ready {
		slice.Endpoints = append(slice.Endpoints, struct {
			Conditions struct {
				Ready *bool `json:"ready"`
			} `json:"conditions"`
		}{})
		slice.Endpoints[len(slice.Endpoints)-1].Conditions.Ready = state
	}
	return slice
}

func TestIdleLoadBalancers(t *testing.T) {
	app := map[string]string{"app": "web"}
	clusterIP := lbService("shop", "internal", app)
	clusterIP.Spec.Type = "ClusterIP"
	idle := lbService("shop", "idle", app)
	idle.Status.LoadBalancer.Ingress = append(idle.Status.LoadBalancer.Ingress, struct {
		IP       string `json:"ip"`
		Hostname string `json:"hostname"`
	}{IP: "203.0.113.7"})

	in := wasteInputs{
		now: wasteNow,
		services: []wasteService{
			lbService("shop", "live", app),
			lbService("shop", "unset", app),
			idle,
			lbService("shop", "manual", nil),
			clusterIP,
		},
		slices: []wasteEndpointSlice{
			wasteSlice("shop", "live", boolRef(false), boolRef(true)),
			// The API documents an absent ready condition as ready.
			wasteSlice("shop", "unset", nil),
			wasteSlice("shop", "idle", boolRef(false)),
		},
		slicesRead: true,
	}
	report := buildWaste(in)
	if len(report.LoadBalancers) != 2 {
		t.Fatalf("expected idle and manual, got %+v", report.LoadBalancers)
	}
	if lb := report.LoadBalancers[0]; lb.Name != "idle" || lb.State != "no-ready-endpoints" ||
		len(lb.Addresses) != 1 || lb.Addresses[0] != "203.0.113.7" {
		t.Errorf("idle = %+v", lb)
	}
	if lb := report.LoadBalancers[1]; lb.Name != "manual" || lb.State != "no-selector" {
		t.Errorf("manual = %+v", lb)
	}

	in.slicesRead = false
	if report := buildWaste(in); len(report.LoadBalancers) != 0 || report.Summary.IdleLoadBalancers != 0 {
		t.Error("with no endpoint slices read, no load balancer is called idle")
	}
}

func TestWasteListIsOrderedBySharedReclaimable(t *testing.T) {
	cpuHeavy := wasteDeploymentPod("a", "cpu-heavy", "a", 72*time.Hour, container("app", requests("8", "256Mi"), nil))
	memHeavy := wasteDeploymentPod("b", "mem-heavy", "a", 72*time.Hour, container("app", requests("100m", "16Gi"), nil))
	small := wasteDeploymentPod("c", "small", "a", 72*time.Hour, container("app", requests("500m", "256Mi"), nil))
	usage := map[observability.UsageKey]observability.UsageProfile{
		usageKey("a", cpuHeavy.Metadata.Name, "app"): profile(100, float64(200*mi)),
		usageKey("b", memHeavy.Metadata.Name, "app"): profile(80, float64(1024*mi)),
		usageKey("c", small.Metadata.Name, "app"):    profile(100, float64(200*mi)),
	}
	report := buildWaste(wasteInputs{now: wasteNow, pods: []wastePod{small, memHeavy, cpuHeavy}, usage: usage})
	if len(report.Workloads) != 3 || report.Workloads[2].Name != "small" {
		t.Errorf("the smallest share sorts last: %+v", report.Workloads)
	}
}

func TestWasteRouteSharesTheResourceGuards(t *testing.T) {
	t.Run("an unknown window is refused", func(t *testing.T) {
		env := newTestEnv(t)
		admin := env.store.addUser("admin", "secret123", "admin")
		cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
		rec := env.do(t, http.MethodGet,
			"/api/v1/clusters/"+itoa(cluster.ID)+"/metrics/waste?window=1h", env.tokenFor(t, admin), nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for a one-hour window, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("direct clusters have no live state", func(t *testing.T) {
		env := newTestEnv(t)
		admin := env.store.addUser("admin", "secret123", "admin")
		cluster := env.store.addCluster("legacy", "dev")
		rec := env.do(t, http.MethodGet,
			"/api/v1/clusters/"+itoa(cluster.ID)+"/metrics/waste", env.tokenFor(t, admin), nil)
		if rec.Code != http.StatusConflict {
			t.Fatalf("expected 409 for a direct-mode cluster, got %d", rec.Code)
		}
	})

	t.Run("a cluster the caller is not granted is refused", func(t *testing.T) {
		env := newTestEnv(t)
		user := env.store.addUser("dev", "secret123", "user")
		cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
		rec := env.do(t, http.MethodGet,
			"/api/v1/clusters/"+itoa(cluster.ID)+"/metrics/waste", env.tokenFor(t, user), nil)
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
			t.Fatalf("expected a refusal without a grant, got %d", rec.Code)
		}
	})

	t.Run("authentication is required", func(t *testing.T) {
		env := newTestEnv(t)
		cluster := env.store.addAgentCluster("edge", "dev", "agent-token")
		rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID)+"/metrics/waste", "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 without a token, got %d", rec.Code)
		}
	})
}
