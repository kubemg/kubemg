package api

import (
	"strings"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/observability"
)

/*
 * Right-sizing.
 *
 * This is the one place in KubeMG that produces a number somebody will paste
 * into a manifest, so what is pinned here is mostly what it *refuses* to
 * recommend: from a partial window, from an absent reading, and — in the
 * direction that gets a pod OOM-killed — from a mean.
 */

var sizingStart = time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
var sizingEnd = sizingStart.Add(7 * 24 * time.Hour)

// profileOf builds the backend's answer for a set of pods.
func profileOf(pods ...observability.PodProfile) observability.ProfileResult {
	return observability.ProfileResult{Pods: pods, Start: sizingStart, End: sizingEnd}
}

func reading(namespace, pod string, cpu, memory float64) observability.PodProfile {
	return observability.PodProfile{
		Namespace: namespace, Pod: pod,
		CPUMillicores: cpu, MemoryBytes: memory,
		CPUSeen: true, MemorySeen: true,
	}
}

// olderThanTheWindow stamps a pod as having existed for the whole window, which
// is what makes its reading evidence rather than a sample of part of it.
func olderThanTheWindow(pod capacityPod) capacityPod {
	pod.Metadata.CreationTimestamp = sizingStart.Add(-24 * time.Hour)
	return pod
}

/* ------------------------------------------------------- over-reservation -- */

func TestBuildRightsizingRecommendsCuttingAnOverReservedWorkload(t *testing.T) {
	pod := olderThanTheWindow(podOn("n1", "shop", "api",
		container("app", map[string]string{"cpu": "2", "memory": "4Gi"}, nil)))
	// A tenth of the CPU and a quarter of the memory it reserved.
	profile := profileOf(reading("shop", "api", 200, 1<<30))

	findings, summary := buildRightsizing(rates(), []capacityPod{pod}, replicaSetOwners{}, profile)

	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(findings))
	}
	got := findings[0]
	if got.Code != "over-reserved" {
		t.Fatalf("code = %s, want over-reserved", got.Code)
	}
	// 200m mean × 1.5 headroom = 300m, which rounds to itself.
	if got.CPU.Recommended != 300 {
		t.Errorf("cpu recommended = %d, want 300", got.CPU.Recommended)
	}
	// 1 GiB peak × 1.25 = 1.25 GiB, rounded up to a 16 MiB step.
	if want := int64(1280 << 20); got.Memory.Recommended != want {
		t.Errorf("memory recommended = %d, want %d", got.Memory.Recommended, want)
	}
	if got.MonthlySaving <= 0 {
		t.Error("cutting a reservation on a priced fleet must report a saving")
	}
	if summary.OverReserved != 1 {
		t.Errorf("over-reserved count = %d, want 1", summary.OverReserved)
	}
}

func TestBuildRightsizingLeavesAWellSizedWorkloadAlone(t *testing.T) {
	pod := olderThanTheWindow(podOn("n1", "shop", "api",
		container("app", map[string]string{"cpu": "500m", "memory": "1Gi"}, nil)))
	// Spending most of what it reserved: nothing to say.
	profile := profileOf(reading("shop", "api", 400, 900<<20))

	findings, summary := buildRightsizing(rates(), []capacityPod{pod}, replicaSetOwners{}, profile)

	if len(findings) != 0 {
		t.Errorf("findings = %+v, want none", findings)
	}
	if summary.RightSized != 1 {
		t.Errorf("right-sized count = %d, want 1 — it was measured and it is fine", summary.RightSized)
	}
}

/* ------------------------------------------------------ under-reservation -- */

func TestBuildRightsizingReportsMemoryUsedAboveWhatWasReserved(t *testing.T) {
	// The one finding that is not about money. Telling somebody they could save
	// on a workload one eviction from an outage would be the wrong sentence.
	pod := olderThanTheWindow(podOn("n1", "shop", "api",
		container("app", map[string]string{"cpu": "2", "memory": "512Mi"}, nil)))
	profile := profileOf(reading("shop", "api", 100, 2<<30))

	findings, summary := buildRightsizing(rates(), []capacityPod{pod}, replicaSetOwners{}, profile)

	if len(findings) != 1 || findings[0].Code != "under-reserved" {
		t.Fatalf("findings = %+v, want one under-reserved finding", findings)
	}
	got := findings[0]
	if got.Severity != severityWarn {
		t.Errorf("severity = %s, want %s", got.Severity, severityWarn)
	}
	if got.MonthlySaving != 0 {
		t.Errorf("saving = %v, want 0 — this finding costs money to fix", got.MonthlySaving)
	}
	// It must recommend at least what was actually observed.
	if got.Memory.Recommended < got.Memory.Observed {
		t.Errorf("recommended %d below the observed peak %d — memory is never sized down to below "+
			"what was seen", got.Memory.Recommended, got.Memory.Observed)
	}
	if summary.UnderReserved != 1 {
		t.Errorf("under-reserved count = %d, want 1", summary.UnderReserved)
	}
}

/* ----------------------------------------------------------- the evidence -- */

func TestBuildRightsizingExcludesAPodYoungerThanTheWindow(t *testing.T) {
	// A pod created inside the window was measured for part of it, and a
	// partial window under-states anything with a daily shape.
	pod := podOn("n1", "shop", "api",
		container("app", map[string]string{"cpu": "2", "memory": "4Gi"}, nil))
	pod.Metadata.CreationTimestamp = sizingStart.Add(time.Hour)
	profile := profileOf(reading("shop", "api", 10, 64<<20))

	findings, summary := buildRightsizing(rates(), []capacityPod{pod}, replicaSetOwners{}, profile)

	if len(findings) != 0 {
		t.Errorf("findings = %+v, want none from a partial window", findings)
	}
	if summary.Unmeasured != 1 {
		t.Errorf("unmeasured = %d, want 1", summary.Unmeasured)
	}
}

func TestBuildRightsizingExcludesAPodTheBackendNeverAnsweredFor(t *testing.T) {
	pod := olderThanTheWindow(podOn("n1", "shop", "api",
		container("app", map[string]string{"cpu": "2"}, nil)))

	findings, summary := buildRightsizing(rates(), []capacityPod{pod},
		replicaSetOwners{}, profileOf())

	if len(findings) != 0 {
		t.Errorf("findings = %+v, want none without a reading", findings)
	}
	if summary.Unmeasured != 1 {
		t.Errorf("unmeasured = %d, want 1", summary.Unmeasured)
	}
}

func TestBuildRightsizingAveragesOverTheMeasuredPodsOnly(t *testing.T) {
	// A Deployment where one replica restarted inside the window must not read
	// as using half what it does.
	controller := true
	owners := replicaSetOwners{
		"shop/api-abc": {Kind: "Deployment", Name: "api", Controller: &controller},
	}
	spec := container("app", map[string]string{"cpu": "1", "memory": "2Gi"}, nil)
	old := olderThanTheWindow(ownedBy(podOn("n1", "shop", "api-abc-1", spec), "ReplicaSet", "api-abc"))
	fresh := ownedBy(podOn("n1", "shop", "api-abc-2", spec), "ReplicaSet", "api-abc")
	fresh.Metadata.CreationTimestamp = sizingStart.Add(time.Hour)

	profile := profileOf(reading("shop", "api-abc-1", 100, 256<<20))
	findings, _ := buildRightsizing(rates(), []capacityPod{old, fresh}, owners, profile)

	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(findings))
	}
	got := findings[0]
	if got.CPU.Observed != 100 {
		t.Errorf("observed CPU = %d, want 100 — averaged over the one pod with evidence",
			got.CPU.Observed)
	}
	if got.CPU.Requested != 1000 {
		t.Errorf("requested CPU = %d, want 1000 per pod", got.CPU.Requested)
	}
	// The saving applies to every pod, because the change is to the template.
	if got.Pods != 2 {
		t.Errorf("pods = %d, want 2 — the template is what gets changed", got.Pods)
	}
}

/* ------------------------------------------------------------- the patch -- */

func TestRenderPatchWritesRequestsOnly(t *testing.T) {
	patch := renderPatch([]containerAdvice{
		{Name: "app", CPURecommended: 300, MemoryRecommended: 1280 << 20},
	})

	for _, want := range []string{"spec:", "template:", "containers:", "- name: app",
		"requests:", `cpu: "300m"`, `memory: "1280Mi"`} {
		if !strings.Contains(patch, want) {
			t.Errorf("patch is missing %q:\n%s", want, patch)
		}
	}
	if strings.Contains(patch, "limits:") {
		t.Errorf("the patch must never carry a limit — it recommends reservations only:\n%s", patch)
	}
}

func TestRenderPatchOmitsAContainerWithNothingToChange(t *testing.T) {
	// What is on screen is the change, not a copy of the current state with the
	// change somewhere inside it.
	patch := renderPatch([]containerAdvice{
		{Name: "app", CPURecommended: 300},
		{Name: "sidecar"},
	})

	if strings.Contains(patch, "sidecar") {
		t.Errorf("a container with no recommendation must not appear:\n%s", patch)
	}
}

func TestRenderPatchIsEmptyWhenThereIsNothingToSay(t *testing.T) {
	if got := renderPatch([]containerAdvice{{Name: "app"}}); got != "" {
		t.Errorf("patch = %q, want empty", got)
	}
}

/* ------------------------------------------------------- the apportioning -- */

func TestApportionGivesASingleContainerTheWholeFigure(t *testing.T) {
	got := apportion([]containerAdvice{{Name: "app", CPURequest: 1000, MemoryRequest: 1 << 30}},
		300, 512<<20, 1<<30)

	if got[0].CPURecommended != 300 || got[0].MemoryRecommended != 512<<20 {
		t.Errorf("apportion = %+v, want the pod-level figure unchanged", got[0])
	}
}

func TestApportionSplitsInProportionToCurrentRequests(t *testing.T) {
	// The measurement is pod-level, so the only ratio available is what the
	// containers already ask for. The drawer says so beside the YAML.
	containers := []containerAdvice{
		{Name: "app", CPURequest: 750, MemoryRequest: 3 << 30},
		{Name: "sidecar", CPURequest: 250, MemoryRequest: 1 << 30},
	}

	got := apportion(containers, 400, 4<<30, 4<<30)

	if got[0].CPURecommended != 300 || got[1].CPURecommended != 100 {
		t.Errorf("cpu split = %d/%d, want 300/100", got[0].CPURecommended, got[1].CPURecommended)
	}
	if got[0].MemoryRecommended != 3<<30 || got[1].MemoryRecommended != 1<<30 {
		t.Errorf("memory split = %d/%d, want 3Gi/1Gi",
			got[0].MemoryRecommended, got[1].MemoryRecommended)
	}
}

func TestApportionLeavesAContainerDeclaringNothingDeclaringNothing(t *testing.T) {
	// It has no share of a proportional split, and inventing one would put a
	// number in a manifest that nothing measured.
	containers := []containerAdvice{
		{Name: "app", CPURequest: 1000, MemoryRequest: 1 << 30},
		{Name: "sidecar"},
	}

	got := apportion(containers, 400, 1<<30, 1<<30)

	if got[1].CPURecommended != 0 || got[1].MemoryRecommended != 0 {
		t.Errorf("sidecar = %+v, want nothing recommended", got[1])
	}
}

/* ------------------------------------------------------------- the floors -- */

func TestRecommendationsRoundUpAndNeverBelowTheFloor(t *testing.T) {
	// Every rounding in this file is in the direction that gives a workload
	// more than the evidence strictly demands.
	if got := recommendCPU(1); got != minRecommendedCPU {
		t.Errorf("recommendCPU(1) = %d, want the %dm floor", got, minRecommendedCPU)
	}
	if got := recommendMemory(1 << 20); got != minRecommendedMemory {
		t.Errorf("recommendMemory(1Mi) = %d, want the floor %d", got, minRecommendedMemory)
	}
	if got := recommendCPU(201); got != 310 {
		t.Errorf("recommendCPU(201) = %d, want 310 — 301.5 rounded up to a 10m step", got)
	}
}

func TestFormatQuantitiesTheWayAManifestDoes(t *testing.T) {
	cases := map[int64]string{500: `"500m"`, 1000: `"1"`, 2000: `"2"`}
	for millicores, want := range cases {
		if got := formatMillicores(millicores); got != want {
			t.Errorf("formatMillicores(%d) = %s, want %s", millicores, got, want)
		}
	}
	if got := formatMebibytes(2 << 30); got != `"2Gi"` {
		t.Errorf("formatMebibytes(2Gi) = %s, want \"2Gi\"", got)
	}
	if got := formatMebibytes(1536 << 20); got != `"1536Mi"` {
		t.Errorf("formatMebibytes(1536Mi) = %s, want \"1536Mi\"", got)
	}
}

func TestWindowCoverageNamesAShortWindowAsShort(t *testing.T) {
	short := windowCoverage(sizingStart, sizingStart.Add(20*time.Minute))
	if !strings.Contains(short, "short") {
		t.Errorf("coverage = %q, want it to say a twenty-minute window is short", short)
	}
	week := windowCoverage(sizingStart, sizingEnd)
	if !strings.Contains(week, "week") {
		t.Errorf("coverage = %q, want it to name a week", week)
	}
}
