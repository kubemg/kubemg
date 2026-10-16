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
 * A container's usage over a window, reduced to the two numbers a request is
 * sized from.
 *
 * The waste report asks one question of history: over the last week, what did
 * each running container actually need? Two readings answer it, and they are
 * deliberately different aggregations because the two resources fail
 * differently:
 *
 *   CPU     the 95th percentile of the five-minute rate. CPU is compressible —
 *           a container over its request is slowed, not killed — so sizing to
 *           the rare peak would reserve a core for the one minute a day it is
 *           wanted. The p95 is what a request should cover.
 *   memory  the peak working set. Memory is not compressible: a container that
 *           needs more than its node can give is the one the kubelet evicts,
 *           so the only honest basis is the most it ever held.
 *
 * A third query asks **when each container's history starts**. A pod's age is
 * not its history: a datasource registered an hour ago holds an hour of a pod
 * that has run for a month, and sizing that pod from the hour would be sizing
 * it from a guess. The report sizes nothing with less than a day behind it, and
 * this is what lets it tell.
 *
 * Both are the catalogue's discipline unchanged: the browser never sends a
 * query, names are validated rather than escaped, and the scope rides on the
 * selector, resolved from the caller's grant. Unlike the catalogue this is not
 * a chart — it is one instant query per resource, evaluated at the window's
 * end, returning one number per container.
 *
 * **The container/pod fallback needs `or on (namespace, pod)` here**, where the
 * catalogue's plain `or` suffices. The catalogue's two halves aggregate to the
 * same label set, so `or` matches them and drops the duplicate; these group by
 * container on the left and cannot on the right, so a plain `or` would never
 * match and would add every pod-level series on top of the container ones. The
 * `on` clause matches by pod, which is exactly "use the pod-level series only
 * for a pod the container-level ones do not cover".
 */

const (
	// usageProfileTimeout is longer than a chart's: a 30-day subquery over every
	// container in a cluster is the most expensive read KubeMG asks of a
	// backend, and it is asked on a button rather than a timer.
	usageProfileTimeout = 60 * time.Second
	// maxUsageSeries bounds what is held from one answer. Past it the profile is
	// partial and says so; a cluster with more running containers than this is
	// read a namespace at a time by a scoped caller anyway.
	maxUsageSeries = 10_000
	// maxSubqueryPoints bounds the CPU subquery's resolution. A week at five
	// minutes is 2016 points per series, and a longer window coarsens the step
	// rather than growing the work.
	maxSubqueryPoints = 2016
	// minSubqueryStep is the rate window itself: a step finer than the range
	// the rate is taken over samples the same value twice.
	minSubqueryStep = 5 * time.Minute
)

// UsageKey addresses one container's profile. Container is empty for a
// datasource whose cadvisor series carry no `container` label: the reading is
// then the whole pod's.
type UsageKey struct {
	Namespace string
	Pod       string
	Container string
}

// UsageProfile is one container's two readings. Either is nil where the
// backend had nothing for it — "not measured" is not "used nothing".
type UsageProfile struct {
	// CPU is the 95th percentile of the five-minute rate, in millicores.
	CPU *float64
	// Memory is the peak working set, in bytes.
	Memory *float64
	// Since is the first sample inside the window — where this container's
	// history, as the datasource holds it, begins.
	Since *time.Time
}

// UsageResult is the whole answer.
type UsageResult struct {
	Profiles map[UsageKey]UsageProfile
	// Truncated reports an answer cut at maxUsageSeries.
	Truncated bool
	Start     time.Time
	End       time.Time
	// The two queries asked, returned for the reason every query route returns
	// its own: an empty answer is diagnosed by reading what was asked.
	CPUQuery     string
	MemoryQuery  string
	HistoryQuery string
}

// QueryUsageProfile reads every running container's CPU p95 and memory peak
// over the window, within the caller's scope.
func QueryUsageProfile(ctx context.Context, target Target, tunnel TunnelCall,
	scope Scope, window Window,
) (UsageResult, error) {
	if target.Kind != db.SourceMetrics {
		return UsageResult{}, fmt.Errorf("this datasource does not serve metrics")
	}
	if err := target.Validate(); err != nil {
		return UsageResult{}, err
	}
	if target.AccessMode == db.AccessInCluster && tunnel == nil {
		return UsageResult{}, fmt.Errorf(
			"an in-cluster datasource can only be read through a connected agent")
	}

	namespaces, err := scope.resolveNamespace("")
	if err != nil {
		return UsageResult{}, err
	}
	sel := selector{namespaces: namespaces}

	window, err = window.Normalize(time.Now().UTC())
	if err != nil {
		return UsageResult{}, err
	}
	span := window.End.Sub(window.Start)
	if span < minSummarySpan {
		span = minSummarySpan
	}

	result := UsageResult{
		Profiles:    map[UsageKey]UsageProfile{},
		Start:       window.End.Add(-span),
		End:         window.End,
		CPUQuery:     cpuProfileQuery(sel, span),
		MemoryQuery:  memoryProfileQuery(sel, span),
		HistoryQuery: historyProfileQuery(sel, span),
	}

	ctx, cancel := context.WithTimeout(ctx, usageProfileTimeout)
	defer cancel()

	cpu, cpuTruncated, err := profileVector(ctx, target, tunnel, result.CPUQuery, result.End)
	if err != nil {
		return UsageResult{}, err
	}
	memory, memoryTruncated, err := profileVector(ctx, target, tunnel, result.MemoryQuery, result.End)
	if err != nil {
		return UsageResult{}, err
	}
	history, historyTruncated, err := profileVector(ctx, target, tunnel, result.HistoryQuery, result.End)
	if err != nil {
		return UsageResult{}, err
	}
	result.Truncated = cpuTruncated || memoryTruncated || historyTruncated

	for key, value := range cpu {
		reading := value
		profile := result.Profiles[key]
		profile.CPU = &reading
		result.Profiles[key] = profile
	}
	for key, value := range memory {
		reading := value
		profile := result.Profiles[key]
		profile.Memory = &reading
		result.Profiles[key] = profile
	}
	for key, value := range history {
		// Only a container with a reading has a history worth stating.
		profile, found := result.Profiles[key]
		if !found {
			continue
		}
		since := time.Unix(int64(value), 0).UTC()
		profile.Since = &since
		result.Profiles[key] = profile
	}
	return result, nil
}

// cpuProfileQuery renders the CPU p95. The subquery takes the rate at each step
// and the quantile over those steps; the step is derived from the span so a
// month costs the same number of points as a week.
func cpuProfileQuery(sel selector, span time.Duration) string {
	containers := fmt.Sprintf(
		`sum by (namespace, pod, container) (rate(container_cpu_usage_seconds_total{%s}[5m]))`,
		sel.containers())
	pods := fmt.Sprintf(
		`sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{%s}[5m]))`,
		sel.pods())
	return fmt.Sprintf(`quantile_over_time(0.95, (%s or on (namespace, pod) %s)[%s:%s]) * 1000`,
		containers, pods, promDuration(span), promDuration(subqueryStep(span)))
}

// memoryProfileQuery renders the memory peak. `max` rather than `sum` across a
// container's series: a container restarted inside the window is two series
// with the same labels but a different id, and its peak is the larger of the
// two, not their total.
func memoryProfileQuery(sel selector, span time.Duration) string {
	window := promDuration(span)
	containers := fmt.Sprintf(
		`max by (namespace, pod, container) (max_over_time(container_memory_working_set_bytes{%s}[%s]))`,
		sel.containers(), window)
	pods := fmt.Sprintf(
		`max by (namespace, pod) (max_over_time(container_memory_working_set_bytes{%s}[%s]))`,
		sel.pods(), window)
	return containers + " or on (namespace, pod) " + pods
}

// historyProfileQuery renders when each container's history begins: the
// earliest sample timestamp inside the window. Read off the memory gauge, which
// every scrape writes, at the CPU subquery's step — so it is as fine as the
// reading it qualifies and no finer.
func historyProfileQuery(sel selector, span time.Duration) string {
	window := promDuration(span) + ":" + promDuration(subqueryStep(span))
	containers := fmt.Sprintf(
		`min by (namespace, pod, container) (min_over_time(timestamp(container_memory_working_set_bytes{%s})[%s]))`,
		sel.containers(), window)
	pods := fmt.Sprintf(
		`min by (namespace, pod) (min_over_time(timestamp(container_memory_working_set_bytes{%s})[%s]))`,
		sel.pods(), window)
	return containers + " or on (namespace, pod) " + pods
}

// subqueryStep is the resolution the CPU subquery is evaluated at.
func subqueryStep(span time.Duration) time.Duration {
	step := span / maxSubqueryPoints
	if step <= minSubqueryStep {
		return minSubqueryStep
	}
	// Up to a whole minute, so the point count stays at or under the bound.
	if rounded := step.Truncate(time.Minute); rounded != step {
		return rounded + time.Minute
	}
	return step
}

// profileVector runs one instant query and keys its answer by container.
func profileVector(ctx context.Context, target Target, tunnel TunnelCall,
	promQL string, at time.Time,
) (map[UsageKey]float64, bool, error) {
	params := url.Values{}
	params.Set("query", promQL)
	params.Set("time", strconv.FormatInt(at.Unix(), 10))
	path := "/api/v1/query?" + params.Encode()

	status, body, err := callLimited(ctx, target, path, tunnel, maxQueryBody, usageProfileTimeout)
	if err != nil {
		return nil, false, err
	}
	if status < 200 || status >= 300 {
		return nil, false, fmt.Errorf("%s", explain(target, status, body))
	}
	return decodeProfile(body)
}

// decodeProfile reads an instant vector into a reading per container. A
// series with no namespace or pod is not a container's and is skipped; NaN and
// the infinities are dropped by decodeSample, as everywhere else.
func decodeProfile(body []byte) (map[UsageKey]float64, bool, error) {
	var payload vectorResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false, fmt.Errorf("the datasource returned an unreadable answer")
	}
	if payload.Status == "error" {
		message := payload.Error
		if message == "" {
			message = "the datasource refused the query"
		}
		return nil, false, fmt.Errorf("%s", message)
	}

	out := make(map[UsageKey]float64, min(len(payload.Data.Result), maxUsageSeries))
	truncated := false
	for _, entry := range payload.Data.Result {
		_, value, ok := decodeSample(entry.Value)
		if !ok {
			continue
		}
		key := UsageKey{
			Namespace: entry.Metric["namespace"],
			Pod:       entry.Metric["pod"],
			Container: entry.Metric["container"],
		}
		if key.Namespace == "" || key.Pod == "" {
			continue
		}
		if _, seen := out[key]; !seen && len(out) >= maxUsageSeries {
			truncated = true
			continue
		}
		// One container answering twice is the same container restarted; the
		// larger reading is the one a request has to cover.
		if existing, seen := out[key]; seen && existing >= value {
			continue
		}
		out[key] = value
	}
	return out, truncated, nil
}
