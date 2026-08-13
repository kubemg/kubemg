package api

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

/*
 * What these workloads should have asked for.
 *
 * This is the one place in KubeMG that offers a number somebody will paste into
 * a manifest, so it is also the one place that had to decide what evidence is
 * good enough to do that on. The answer: a window of history, or nothing.
 *
 * metrics-server keeps about two minutes. A recommendation from two minutes is a
 * recommendation about two minutes — a batch job sampled between runs uses
 * nothing, a nightly service sampled at 3pm looks free — so a cluster with no
 * metrics datasource registered gets **no recommendation at all** rather than a
 * degraded one. The cost report beside it still works there, because "what you
 * reserved costs this" needs no history; "you should reserve less" does.
 *
 * Three more decisions are the design:
 *
 * **Requests, never limits.** A request is a reservation: it decides scheduling,
 * it decides what the next node is bought for, and it is the number this report
 * can price. A limit is a reliability decision about what should happen to a
 * container that misbehaves, and the right value for it depends on things not
 * visible from a usage series — whether the workload is latency-sensitive,
 * whether it shares a node with something that is. KubeMG recommends the one it
 * can reason about and stays out of the other.
 *
 * **CPU down freely, memory carefully.** CPU over its share is throttled and
 * memory over its share is killed, so a CPU recommendation is sized on the mean
 * with ordinary headroom and a memory one on the observed peak — never below it,
 * whatever the mean says. The same asymmetry the capacity report's thresholds
 * encode.
 *
 * **The observation is per pod and the manifest is per container.** cadvisor's
 * container label is not promoted by every scrape config — see the note above
 * the metric catalogue — so a per-container reading is not something this can
 * rely on. The split across a pod's containers therefore follows what they
 * currently request, in proportion, and the drawer says so beside the YAML. A
 * pod with one container, which is most of them, has nothing to apportion.
 *
 * Nothing here writes to the cluster. The recommendation is a patch an operator
 * reads, copies and applies through whatever owns their manifests — which for a
 * GitOps fleet is not KubeMG and should not be.
 */

const (
	// cpuHeadroom multiplies the sustained mean into a reservation. Half again
	// is enough for the ordinary variance a mean hides while still being a real
	// reduction; a workload needing more than that above its own average is one
	// whose shape a single number was never going to describe.
	cpuHeadroom = 1.5
	// memoryHeadroom multiplies the observed peak. It is much tighter than the
	// CPU figure because it is applied to a peak rather than a mean — the
	// margin is for the peak this window did not happen to contain.
	memoryHeadroom = 1.25

	// The floors. Below these a reservation stops being a reservation and
	// starts being a rounding error that makes the scheduler's arithmetic
	// worse, and no cluster's bill turns on the difference.
	minRecommendedCPU    = int64(10)       // millicores
	minRecommendedMemory = int64(32 << 20) // 32 MiB
	cpuRoundTo           = int64(10)       // millicores
	memoryRoundTo        = int64(16 << 20) // 16 MiB

	// overReservedRatio is the roadmap's own bar and this report's: a workload
	// spending less than half what it reserved is over-provisioned. Anything
	// tighter is inside the noise of a window that is never the whole story.
	overReservedRatio = 0.5
)

/* ------------------------------------------------------------ the payload -- */

// sizeAdvice is one resource's recommendation for one workload.
type sizeAdvice struct {
	// Requested is what the workload's pods reserve today, per pod.
	Requested int64 `json:"requested"`
	// Observed is what they actually used: the mean for CPU, the peak for
	// memory. See the header for why those are different statistics.
	Observed int64 `json:"observed"`
	// Recommended is Observed with headroom, floored and rounded. It is absent
	// — zero — where nothing is recommended for this resource.
	Recommended int64 `json:"recommended"`
	// UsedPercent is Observed against Requested, which is the number an
	// operator argues with.
	UsedPercent float64 `json:"used_percent"`
	// Measured is false where the backend returned no series for this resource,
	// which is a state and not a zero.
	Measured bool `json:"measured"`
}

// containerAdvice is the per-container split, which is what a manifest needs.
type containerAdvice struct {
	Name string `json:"name"`
	// The current declared requests, and what is suggested in their place.
	CPURequest        int64 `json:"cpu_request"`
	MemoryRequest     int64 `json:"memory_request"`
	CPURecommended    int64 `json:"cpu_recommended"`
	MemoryRecommended int64 `json:"memory_recommended"`
}

// sizingFinding is one workload worth changing.
type sizingFinding struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Pods      int    `json:"pods"`

	Code     string `json:"code"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`

	CPU    sizeAdvice `json:"cpu"`
	Memory sizeAdvice `json:"memory"`

	// MonthlySaving is what dropping to the recommendation would stop
	// reserving, across every pod of this workload, at the cluster's rates. It
	// is zero on an unpriced fleet and negative savings are never reported —
	// an under-reserved workload costs more to fix, and saying "-$40" would
	// read as a saving.
	MonthlySaving float64 `json:"monthly_saving"`

	// Containers is the split the YAML is rendered from.
	Containers []containerAdvice `json:"containers"`
	// Patch is the recommendation as something to paste: a strategic-merge
	// patch against the workload's pod template, requests only.
	Patch string `json:"patch"`
}

func (f sizingFinding) sortKey() (string, string) { return f.Namespace, f.Name }

// sizingSummary totals what the pass found.
type sizingSummary struct {
	Workloads     int     `json:"workloads"`
	OverReserved  int     `json:"over_reserved"`
	UnderReserved int     `json:"under_reserved"`
	MonthlySaving float64 `json:"monthly_saving"`
	// Skipped is how many workloads were measured but had no finding — the
	// ones that are sized about right, which is worth reporting as a number
	// rather than as an absence.
	RightSized int `json:"right_sized"`
	// Unmeasured is how many were excluded for want of evidence: too young for
	// the window, or absent from the backend's answer.
	Unmeasured int `json:"unmeasured"`
}

/* -------------------------------------------------------------- the read -- */

// clusterRightsizing recommends request sizes from a window of history.
func (s *server) clusterRightsizing(c *gin.Context) {
	user, cluster, grant, ok := s.resourceCluster(c)
	if !ok {
		return
	}
	if !s.requireClusterScope(c, grant, "right-sizing recommendations") {
		return
	}

	// No datasource is a refusal rather than a fallback, and it carries the
	// same `unconfigured` flag every other datasource-backed read does, so the
	// console offers "register one" instead of an error.
	source, ok := s.querySource(c, cluster, db.SourceMetrics)
	if !ok {
		return
	}
	window, ok := queryWindow(c)
	if !ok {
		return
	}

	card, err := s.store.RateCardFor(c.Request.Context(), cluster.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read the rate card"})
		return
	}
	rates := db.RateCard{}
	if card != nil {
		rates = *card
	}

	pods, ok := s.fetchSchedulablePods(c, user, cluster, grant)
	if !ok {
		return
	}
	owners, ok := s.fetchReplicaSetOwners(c, user, cluster, grant)
	if !ok {
		return
	}

	profile, err := observability.QueryPodProfiles(c.Request.Context(),
		observability.TargetOf(*source), s.tunnelCall(user, cluster),
		queryScope(user, grant), window)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	findings, summary := buildRightsizing(rates, pods, owners, profile)

	c.JSON(http.StatusOK, gin.H{
		"priced":       rates.Priced(),
		"currency":     rates.Currency,
		"findings":     findings,
		"summary":      summary,
		"start":        profile.Start,
		"end":          profile.End,
		"coverage":     windowCoverage(profile.Start, profile.End),
		"cpu_query":    profile.CPUQuery,
		"memory_query": profile.MemoryQuery,
		"provider":     source.Provider,
	})
}

/* --------------------------------------------------------- the arithmetic -- */

// sizingWorkload accumulates one workload's pods on the way to a finding.
type sizingWorkload struct {
	kind, name, namespace string

	pods int
	// measuredPods counts the pods that had evidence for the whole window. The
	// per-pod figures below are averaged over these rather than over every pod,
	// so a Deployment where one replica restarted is not reported as using half
	// what it does.
	measuredPods int

	requestCPU, requestMemory int64
	observedCPU               float64
	observedMemory            float64
	cpuSeen, memorySeen       bool

	// containers is the pod template's declared requests, taken from the first
	// pod seen. Every pod of a workload runs the same template; where a
	// rollout means two of them briefly do not, the first is as good a
	// description as the second and the patch is reviewed by a human either way.
	containers []containerAdvice
}

// buildRightsizing is the pass, as a pure function of the reads.
func buildRightsizing(card db.RateCard, pods []capacityPod, owners replicaSetOwners,
	profile observability.ProfileResult,
) ([]sizingFinding, sizingSummary) {
	readings := map[podUsageKey]observability.PodProfile{}
	for _, entry := range profile.Pods {
		readings[podUsageKey{entry.Namespace, entry.Pod}] = entry
	}

	type key struct{ kind, namespace, name string }
	index := map[key]*sizingWorkload{}
	order := []key{}

	for _, pod := range pods {
		if pod.Spec.NodeName == "" {
			continue
		}
		kind, name := workloadOf(pod, owners)
		id := key{kind, pod.Metadata.Namespace, name}
		entry, seen := index[id]
		if !seen {
			entry = &sizingWorkload{
				kind: kind, name: name, namespace: pod.Metadata.Namespace,
				containers: containersOf(pod),
			}
			index[id] = entry
			order = append(order, id)
		}

		demand := demandOf(pod)
		entry.pods++

		// A pod younger than the window was measured for part of it, and a
		// partial window under-states a workload with any periodicity at all.
		// Excluding it is the same refusal the whole feature is built on.
		if pod.Metadata.CreationTimestamp.After(profile.Start) {
			continue
		}
		reading, measured := readings[podUsageKey{pod.Metadata.Namespace, pod.Metadata.Name}]
		if !measured {
			continue
		}

		entry.measuredPods++
		entry.requestCPU += demand.cpuRequest
		entry.requestMemory += demand.memoryRequest
		if reading.CPUSeen {
			entry.observedCPU += reading.CPUMillicores
			entry.cpuSeen = true
		}
		if reading.MemorySeen {
			entry.observedMemory += reading.MemoryBytes
			entry.memorySeen = true
		}
	}

	findings := []sizingFinding{}
	summary := sizingSummary{}

	for _, id := range order {
		entry := index[id]
		summary.Workloads++
		if entry.measuredPods == 0 {
			summary.Unmeasured++
			continue
		}

		finding, worth := entry.finding(card)
		if !worth {
			summary.RightSized++
			continue
		}
		switch finding.Code {
		case "over-reserved":
			summary.OverReserved++
		case "under-reserved":
			summary.UnderReserved++
		}
		summary.MonthlySaving += finding.MonthlySaving
		findings = append(findings, finding)
	}

	summary.MonthlySaving = money(summary.MonthlySaving)
	slices.SortFunc(findings, func(a, b sizingFinding) int {
		if a.MonthlySaving != b.MonthlySaving {
			if a.MonthlySaving > b.MonthlySaving {
				return -1
			}
			return 1
		}
		if order := strings.Compare(a.Namespace, b.Namespace); order != 0 {
			return order
		}
		return strings.Compare(a.Name, b.Name)
	})
	return findings, summary
}

// finding turns one workload's accumulated readings into a recommendation, or
// reports that it is sized about right.
func (w *sizingWorkload) finding(card db.RateCard) (sizingFinding, bool) {
	perPod := float64(w.measuredPods)

	out := sizingFinding{
		Kind: w.kind, Name: w.name, Namespace: w.namespace, Pods: w.pods,
		CPU: sizeAdvice{
			Requested: int64(float64(w.requestCPU) / perPod),
			Observed:  int64(w.observedCPU / perPod),
			Measured:  w.cpuSeen,
		},
		Memory: sizeAdvice{
			Requested: int64(float64(w.requestMemory) / perPod),
			Observed:  int64(w.observedMemory / perPod),
			Measured:  w.memorySeen,
		},
	}
	out.CPU.UsedPercent = ratio(float64(out.CPU.Observed), float64(out.CPU.Requested))
	out.Memory.UsedPercent = ratio(float64(out.Memory.Observed), float64(out.Memory.Requested))

	// Under-reserved is checked first and reported instead of a saving. A
	// workload whose peak memory exceeds what it reserved is one eviction away
	// from an outage, and telling somebody they could save money on it would be
	// the wrong sentence entirely.
	if out.Memory.Measured && out.Memory.Requested > 0 &&
		out.Memory.Observed > out.Memory.Requested {
		out.Code = "under-reserved"
		out.Severity = severityWarn
		out.Title = "Reserves less memory than it used"
		out.Detail = "This workload's peak memory over the window is above what its pods " +
			"reserve. The scheduler placed them on that reservation, so the node they landed " +
			"on may not have the memory they actually need — and the kubelet answers a " +
			"shortage by evicting, which need not be the pod that caused it."
		out.Memory.Recommended = recommendMemory(out.Memory.Observed)
		out.Containers = apportion(w.containers, 0, out.Memory.Recommended, w.requestMemoryTotal())
		out.Patch = renderPatch(out.Containers)
		return out, true
	}

	cpuOver := out.CPU.Measured && out.CPU.Requested > 0 &&
		float64(out.CPU.Observed) < float64(out.CPU.Requested)*overReservedRatio
	memoryOver := out.Memory.Measured && out.Memory.Requested > 0 &&
		float64(out.Memory.Observed) < float64(out.Memory.Requested)*overReservedRatio

	if !cpuOver && !memoryOver {
		return sizingFinding{}, false
	}

	if cpuOver {
		if recommended := recommendCPU(out.CPU.Observed); recommended < out.CPU.Requested {
			out.CPU.Recommended = recommended
		}
	}
	if memoryOver {
		if recommended := recommendMemory(out.Memory.Observed); recommended < out.Memory.Requested {
			out.Memory.Recommended = recommended
		}
	}
	// Headroom can land the recommendation above the current reservation on a
	// workload that is barely over the bar. Nothing to recommend is not a
	// finding.
	if out.CPU.Recommended == 0 && out.Memory.Recommended == 0 {
		return sizingFinding{}, false
	}

	out.Code = "over-reserved"
	out.Severity = severityNote
	out.Title = "Reserves more than it uses"
	out.Detail = "Over the window, this workload's pods spent well under what they reserve. " +
		"The reservation is what stops the scheduler placing other work there, so it is " +
		"capacity bought and held rather than capacity used."
	out.MonthlySaving = money(savingOf(card, out, w.pods))
	out.Containers = apportion(w.containers, out.CPU.Recommended, out.Memory.Recommended,
		w.requestMemoryTotal())
	out.Patch = renderPatch(out.Containers)
	return out, true
}

// requestMemoryTotal is the pod template's declared memory, which apportion
// splits a pod-level recommendation by. It is taken from the template rather
// than from the observed reading for the reason the header gives.
func (w *sizingWorkload) requestMemoryTotal() int64 {
	var total int64
	for _, container := range w.containers {
		total += container.MemoryRequest
	}
	return total
}

// containersOf reads the pod template's declared requests. Init containers are
// deliberately absent: a plain init step's reservation is not part of the
// steady state, and a native sidecar's is — but a sidecar is somebody else's
// chart, injected, and recommending a change to it is advice about a manifest
// the operator does not own.
func containersOf(pod capacityPod) []containerAdvice {
	out := make([]containerAdvice, 0, len(pod.Spec.Containers))
	for _, container := range pod.Spec.Containers {
		out = append(out, containerAdvice{
			Name:          container.Name,
			CPURequest:    parseCPUMillicores(container.Resources.Requests["cpu"]),
			MemoryRequest: parseMemoryBytes(container.Resources.Requests["memory"]),
		})
	}
	return out
}

/*
 * apportion splits a pod-level recommendation across a pod's containers.
 *
 * One container takes the whole figure, which is the common case and the exact
 * one. Several split it in proportion to what they currently request, because
 * that is the only ratio available — the measurement is pod-level, and see the
 * header for why a per-container one cannot be relied on. A container declaring
 * no request at all is left declaring none: it has no share of a proportional
 * split, and inventing one would put a number in a manifest that nothing
 * measured.
 */
func apportion(containers []containerAdvice, cpu, memory, memoryTotal int64) []containerAdvice {
	out := slices.Clone(containers)
	if len(out) == 0 {
		return out
	}
	if len(out) == 1 {
		out[0].CPURecommended = cpu
		out[0].MemoryRecommended = memory
		return out
	}

	var cpuTotal int64
	for _, container := range out {
		cpuTotal += container.CPURequest
	}

	for i := range out {
		if cpu > 0 && cpuTotal > 0 && out[i].CPURequest > 0 {
			out[i].CPURecommended = max(share(cpu, out[i].CPURequest, cpuTotal), minRecommendedCPU)
		}
		if memory > 0 && memoryTotal > 0 && out[i].MemoryRequest > 0 {
			out[i].MemoryRecommended = max(share(memory, out[i].MemoryRequest, memoryTotal),
				minRecommendedMemory)
		}
	}
	return out
}

// share is `total * part / whole`, in floating point.
//
// The obvious integer form overflows: two gibibyte-scale operands multiplied
// before the division is comfortably past what an int64 holds, and it wraps to
// a negative that the floor below then quietly turns into a plausible-looking
// 32 MiB. A recommendation is not somewhere to discover that.
func share(total, part, whole int64) int64 {
	if whole <= 0 {
		return 0
	}
	return int64(float64(total) * (float64(part) / float64(whole)))
}

// recommendCPU sizes a reservation from the sustained mean.
func recommendCPU(observed int64) int64 {
	scaled := int64(float64(observed) * cpuHeadroom)
	return roundUpTo(max(scaled, minRecommendedCPU), cpuRoundTo)
}

// recommendMemory sizes a reservation from the observed peak, never below it.
func recommendMemory(observed int64) int64 {
	scaled := int64(float64(observed) * memoryHeadroom)
	return roundUpTo(max(scaled, minRecommendedMemory), memoryRoundTo)
}

// roundUpTo rounds a reservation up to a readable step. Up rather than to
// nearest, always: every rounding in this file is in the direction that gives a
// workload more than the evidence strictly demands.
func roundUpTo(value, step int64) int64 {
	if step <= 0 || value <= 0 {
		return value
	}
	return ((value + step - 1) / step) * step
}

// savingOf prices what dropping to the recommendation would stop reserving,
// across every pod of the workload — including the ones that were not measured,
// because the change is to the template and the template is what they all run.
func savingOf(card db.RateCard, finding sizingFinding, pods int) float64 {
	var cpu, memory int64
	if finding.CPU.Recommended > 0 {
		cpu = max(finding.CPU.Requested-finding.CPU.Recommended, 0)
	}
	if finding.Memory.Recommended > 0 {
		memory = max(finding.Memory.Requested-finding.Memory.Recommended, 0)
	}
	return priceOf(card, cpu*int64(pods), memory*int64(pods)).Total
}

/* ----------------------------------------------------------- the manifest -- */

// renderPatch writes the recommendation as a strategic-merge patch against the
// pod template.
//
// A patch rather than a whole manifest, and requests rather than resources: it
// is the smallest thing that says exactly what changed, it merges cleanly into
// whatever the workload's actual manifest looks like, and it cannot silently
// carry away a limit or a field this report never read. Anything with nothing
// recommended for it is left out entirely rather than restated, so what is on
// screen is the change and not a copy of the current state with the change
// somewhere inside it.
func renderPatch(containers []containerAdvice) string {
	body := []string{}
	for _, container := range containers {
		if container.CPURecommended == 0 && container.MemoryRecommended == 0 {
			continue
		}
		lines := []string{
			"        - name: " + container.Name,
			"          resources:",
			"            requests:",
		}
		if container.CPURecommended > 0 {
			lines = append(lines, fmt.Sprintf("              cpu: %s",
				formatMillicores(container.CPURecommended)))
		}
		if container.MemoryRecommended > 0 {
			lines = append(lines, fmt.Sprintf("              memory: %s",
				formatMebibytes(container.MemoryRecommended)))
		}
		body = append(body, lines...)
	}
	if len(body) == 0 {
		return ""
	}

	header := []string{
		"spec:",
		"  template:",
		"    spec:",
		"      containers:",
	}
	return strings.Join(append(header, body...), "\n") + "\n"
}

// formatMillicores writes a CPU quantity the way a manifest does.
func formatMillicores(millicores int64) string {
	if millicores%1000 == 0 {
		return fmt.Sprintf("%q", fmt.Sprintf("%d", millicores/1000))
	}
	return fmt.Sprintf("%q", fmt.Sprintf("%dm", millicores))
}

// formatMebibytes writes a memory quantity in the binary units a manifest uses.
func formatMebibytes(bytes int64) string {
	const mebibyte = int64(1 << 20)
	if bytes >= 1<<30 && bytes%(1<<30) == 0 {
		return fmt.Sprintf("%q", fmt.Sprintf("%dGi", bytes/(1<<30)))
	}
	return fmt.Sprintf("%q", fmt.Sprintf("%dMi", max(bytes/mebibyte, 1)))
}

// windowCoverage is how much of the requested window the answer actually
// covers, for the sentence the console puts above the table. It is here rather
// than in the browser for the reason every other verdict is.
func windowCoverage(start, end time.Time) string {
	span := end.Sub(start)
	switch {
	case span >= 7*24*time.Hour:
		return "a week or more of history"
	case span >= 24*time.Hour:
		return "a day or more of history"
	case span >= time.Hour:
		return "an hour or more of history"
	default:
		return "less than an hour of history, which is short for a recommendation"
	}
}
