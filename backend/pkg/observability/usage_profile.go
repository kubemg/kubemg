package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * What every pod actually used over a window — the evidence a right-sizing
 * recommendation is allowed to rest on.
 *
 * The live Metrics API cannot serve this. metrics-server keeps roughly two
 * minutes, which is enough to draw a meter and nowhere near enough to say what
 * a workload needs: a batch job sampled between runs uses nothing, and an
 * overnight service sampled at 3pm looks free. Sizing a reservation off that
 * would be a guess wearing a number, so KubeMG does not offer one — the
 * right-sizing pass is available on clusters with a metrics datasource
 * registered and refuses on clusters without, rather than degrading to a
 * recommendation nobody should follow.
 *
 * **CPU is read as a mean and memory as a peak, and the asymmetry is the whole
 * design.** A CPU request is a scheduling reservation and a share weight, not a
 * cap — a container over its share is throttled, which is recoverable, so the
 * right size for it is near what it sustains. A memory request sits under a
 * limit that gets the container killed, and the kubelet answers a node-level
 * shortage by evicting somebody, so the right size for it is the worst it ever
 * got. It is the same asymmetry the capacity report's two overcommit thresholds
 * encode, arrived at from the other end.
 *
 * The memory figure is a **sum of per-container peaks**, which is slightly more
 * than the pod's own peak — the containers do not peak at the same instant.
 * The exact number needs a subquery over the whole window, which on a month of
 * data costs a great deal more than the difference is worth, and the error is in
 * the safe direction: it over-states what a pod needs, so a recommendation built
 * on it is never the one that gets a pod OOM-killed.
 *
 * These two templates are hand-written here for the same reason the chart
 * catalogue's are, and are subject to the same rule: **no PromQL reaches this
 * from the browser.** The caller supplies a window and a scope, and the scope
 * comes from the grant.
 */

// PodProfile is one pod's usage over the window.
type PodProfile struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	// CPUMillicores is the mean over the window.
	CPUMillicores float64 `json:"cpu_millicores"`
	// MemoryBytes is the peak over the window. See the note above on why it is
	// a sum of container peaks.
	MemoryBytes float64 `json:"memory_bytes"`
	// CPUSeen and MemorySeen say which of the two the backend actually
	// answered for this pod. A pod present in one series and absent from the
	// other is ordinary — a pod created inside the window, a container with no
	// cgroup CPU accounting — and treating an absent reading as zero would
	// recommend sizing it to nothing.
	CPUSeen    bool `json:"cpu_seen"`
	MemorySeen bool `json:"memory_seen"`
}

// ProfileResult is the readings plus what was asked to get them.
type ProfileResult struct {
	Pods []PodProfile `json:"pods"`

	Start time.Time `json:"start"`
	End   time.Time `json:"end"`

	// CPUQuery and MemoryQuery are returned for the reason every other query in
	// this package returns its own: an operator staring at an empty answer needs
	// to see what was actually asked, and here there are two questions.
	CPUQuery    string `json:"cpu_query"`
	MemoryQuery string `json:"memory_query"`
}

// minProfileSpan floors the window. A `rate` over anything shorter is usually
// two samples and often none, and the whole point of this read is that it
// covers enough time to be evidence.
const minProfileSpan = 15 * time.Minute

// QueryPodProfiles reads mean CPU and peak memory per pod over a window.
func QueryPodProfiles(ctx context.Context, target Target, tunnel TunnelCall,
	scope Scope, window Window,
) (ProfileResult, error) {
	if target.Kind != db.SourceMetrics {
		return ProfileResult{}, fmt.Errorf("this datasource does not serve metrics")
	}
	if err := target.Validate(); err != nil {
		return ProfileResult{}, err
	}
	if target.AccessMode == db.AccessInCluster && tunnel == nil {
		return ProfileResult{}, fmt.Errorf(
			"an in-cluster datasource can only be read through a connected agent")
	}

	// namespaced: a scoped caller is answered across their own namespaces
	// rather than refused, exactly as the per-namespace breakdowns are.
	sel, err := buildSelector(metricSpec{namespaced: true}, scope, MetricRequest{Window: window})
	if err != nil {
		return ProfileResult{}, err
	}

	normalized, err := window.Normalize(time.Now().UTC())
	if err != nil {
		return ProfileResult{}, err
	}
	span := normalized.End.Sub(normalized.Start)
	if span < minProfileSpan {
		span = minProfileSpan
	}
	promSpan := promDuration(span)

	result := ProfileResult{
		Pods:        []PodProfile{},
		Start:       normalized.End.Add(-span),
		End:         normalized.End,
		CPUQuery:    profileCPUQuery(sel, promSpan),
		MemoryQuery: profileMemoryQuery(sel, promSpan),
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	cpu, err := instantSamples(ctx, target, tunnel, result.CPUQuery, result.End)
	if err != nil {
		return ProfileResult{}, err
	}
	memory, err := instantSamples(ctx, target, tunnel, result.MemoryQuery, result.End)
	if err != nil {
		return ProfileResult{}, err
	}

	result.Pods = mergeProfiles(cpu, memory)
	return result, nil
}

// profileCPUQuery is the sustained CPU one pod spent over the window. A `rate`
// over the whole span *is* its mean, which is why this needs no subquery.
func profileCPUQuery(sel selector, span string) string {
	return withFallback(
		`sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{%s}[`+span+`])) * 1000`,
		sel)
}

// profileMemoryQuery is the worst memory one pod held over the window.
func profileMemoryQuery(sel selector, span string) string {
	return withFallback(
		`sum by (namespace, pod) (max_over_time(container_memory_working_set_bytes{%s}[`+span+`]))`,
		sel)
}

// mergeProfiles joins the two readings on the pair of labels that identifies a
// pod. Pod names are unique within a namespace and not across one, so keying on
// the name alone would merge the `api` in two of them — and this answer feeds a
// recommendation about how much memory to give something.
func mergeProfiles(cpu, memory []sample) []PodProfile {
	index := map[[2]string]*PodProfile{}
	order := [][2]string{}

	take := func(entry sample) (*PodProfile, bool) {
		namespace := entry.labels["namespace"]
		pod := entry.labels["pod"]
		if namespace == "" || pod == "" {
			// A series carrying neither is a rollup rather than a pod, and
			// there is nothing to recommend about a cgroup root.
			return nil, false
		}
		id := [2]string{namespace, pod}
		profile, seen := index[id]
		if !seen {
			profile = &PodProfile{Namespace: namespace, Pod: pod}
			index[id] = profile
			order = append(order, id)
		}
		return profile, true
	}

	for _, entry := range cpu {
		if profile, ok := take(entry); ok {
			profile.CPUMillicores += entry.value
			profile.CPUSeen = true
		}
	}
	for _, entry := range memory {
		if profile, ok := take(entry); ok {
			profile.MemoryBytes += entry.value
			profile.MemorySeen = true
		}
	}

	out := make([]PodProfile, 0, len(order))
	for _, id := range order {
		out = append(out, *index[id])
	}
	return out
}

// instantSamples runs one instant query and keeps every series with its labels.
//
// It is the labelled sibling of instantVector, which reduces an answer to one
// value per legend label. That reduction is what a comparison table wants and
// exactly what this cannot use: the identity here is two labels, and collapsing
// it to one silently sums two different pods together.
func instantSamples(ctx context.Context, target Target, tunnel TunnelCall,
	promQL string, at time.Time,
) ([]sample, error) {
	params := url.Values{}
	params.Set("query", promQL)
	params.Set("time", strconv.FormatInt(at.Unix(), 10))
	path := "/api/v1/query?" + params.Encode()

	status, body, err := callLimited(ctx, target, path, tunnel, maxQueryBody, queryTimeout)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%s", explain(target, status, body))
	}

	var payload vectorResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("the datasource returned an unreadable answer")
	}
	if payload.Status == "error" {
		message := payload.Error
		if message == "" {
			message = "the datasource refused the query"
		}
		return nil, fmt.Errorf("%s", message)
	}

	out := make([]sample, 0, len(payload.Data.Result))
	for _, entry := range payload.Data.Result {
		_, value, ok := decodeSample(entry.Value)
		if !ok {
			continue
		}
		out = append(out, sample{labels: entry.Metric, value: value})
	}
	return out, nil
}
