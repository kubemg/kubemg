package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * Reading a cluster's history out of whichever Prometheus-compatible backend it
 * registered.
 *
 * The Metrics API read in `pkg/api/metrics.go` answers "what is this using right
 * now" — metrics-server keeps about two minutes, which is why the console draws
 * meters and never a chart. This is the other half: the same two questions asked
 * over a window, so a pod that was killed an hour ago still has a shape.
 *
 * **KubeMG does not accept PromQL from the browser.** The queries are a fixed
 * catalogue here, parameterised by namespace, pod and container, and the reason
 * is the scope: a metrics backend has never heard of the caller and will answer
 * anything it is asked, so a namespace-scoped grant that could send its own
 * PromQL would read the entire cluster's series with one query. There is nothing
 * to impersonate and no cluster RBAC to fall back on — the enforcement has to be
 * here, and the only way to enforce it is to be the one writing the query.
 *
 * That is a real limitation and it is deliberate. Adding a chart means adding a
 * catalogue entry here, which is a line of PromQL written by someone who knows
 * what the scope has to be — not a text box in the browser.
 */

const (
	// queryTimeout bounds one query. A range query over a month can be slow on a
	// cold backend, so this is more generous than a probe's — but it is still
	// bounded, because a hung query holds a tunnel stream open.
	queryTimeout = 30 * time.Second
	// maxQueryBody caps a result set. At maxPoints resolution a legitimate answer
	// is well under this; past it, something is being asked for that no chart can
	// draw.
	maxQueryBody = 8 << 20
	// maxSeries bounds how many series come back to the browser. A namespace with
	// four hundred pods is a legitimate query and an illegible chart, so the
	// answer is truncated and says so.
	maxSeries = 60
)

// MetricKind names one thing worth charting. It is a closed set because each
// entry is a hand-written PromQL template — which is the point.
type MetricKind string

const (
	// MetricPodCPU and MetricPodMemory chart one pod, split per container, which
	// is the view the pod drawer needs: a pod at its limit is usually one
	// container at its limit.
	MetricPodCPU    MetricKind = "pod_cpu"
	MetricPodMemory MetricKind = "pod_memory"

	// MetricNamespaceCPU and MetricNamespaceMemory chart a namespace, split per
	// pod — what is this namespace spending, and on what.
	MetricNamespaceCPU    MetricKind = "namespace_cpu"
	MetricNamespaceMemory MetricKind = "namespace_memory"

	// MetricClusterCPU and MetricClusterMemory chart the cluster as one line.
	// They are refused to a scoped caller, like every other cluster-wide read.
	MetricClusterCPU    MetricKind = "cluster_cpu"
	MetricClusterMemory MetricKind = "cluster_memory"
)

// Unit is what a series is measured in. It travels with the answer so the
// browser never has to infer it from the metric's name.
type Unit string

const (
	// UnitMillicores and UnitBytes are the same two units the live Metrics API
	// endpoints normalise to, so a chart and a meter agree.
	UnitMillicores Unit = "millicores"
	UnitBytes      Unit = "bytes"
)

// metricSpec is one catalogue entry: what it charts, in what units, and the
// PromQL that answers it.
type metricSpec struct {
	// query takes the already-validated selector and returns PromQL. It is a
	// function rather than a format string because the selector differs per
	// entry, and a template with the wrong number of holes is a runtime surprise.
	query func(sel selector) string
	unit  Unit
	// legend is the label whose value names each series in the chart.
	legend string
	// namespaced is false for the cluster-wide entries, which a scoped grant is
	// refused outright.
	namespaced bool
	// description is what the UI puts under the chart title.
	description string
}

/*
 * The metric names below are cadvisor's. The *names* are stable — every kubelet
 * exports them and all four supported backends scrape them unchanged — but the
 * **labels are not**, and that distinction is the whole reason each entry below
 * carries a fallback.
 *
 * Whether a cadvisor series is labelled `namespace` / `pod` / `container` is
 * decided by the scrape config's relabeling, not by cadvisor. kube-prometheus-stack
 * promotes all three, so its series are per-container and the pod-level rollup
 * has to be excluded or every total doubles — that is what `container!=""` is
 * for, alongside `container!="POD"`, which drops the pause container holding the
 * network namespace. The prometheus-community chart's `kubernetes-nodes-cadvisor`
 * job promotes `namespace` and `pod` but **not** `container`, so on that very
 * common setup `container!=""` matches nothing at all and every chart comes back
 * empty — the label is absent, and an absent label compares equal to "".
 *
 * So each entry is `container-level or pod-level`. PromQL's `or` takes the left
 * side wherever it has samples and fills in from the right only where it does
 * not, which is exactly the semantics wanted: where container series exist they
 * are used and the rollup stays excluded; where they do not, the pod-level series
 * answer. Those pod-level series are one per pod, so summing them does not
 * double-count — verified against the `/kubepods.slice` rollup, which they match
 * to within sampling skew.
 */
var metricCatalogue = map[MetricKind]metricSpec{
	MetricPodCPU: {
		unit:        UnitMillicores,
		legend:      "container",
		namespaced:  true,
		description: "CPU used per container, against the container's own limit.",
		query: func(sel selector) string {
			return withFallback(
				`sum by (container) (rate(container_cpu_usage_seconds_total{%s}[5m])) * 1000`,
				sel)
		},
	},
	MetricPodMemory: {
		unit:        UnitBytes,
		legend:      "container",
		namespaced:  true,
		description: "Working set per container — what the kernel cannot reclaim.",
		query: func(sel selector) string {
			return withFallback(`sum by (container) (container_memory_working_set_bytes{%s})`, sel)
		},
	},

	MetricNamespaceCPU: {
		unit:        UnitMillicores,
		legend:      "pod",
		namespaced:  true,
		description: "CPU used per pod in this namespace.",
		query: func(sel selector) string {
			return withFallback(
				`sum by (pod) (rate(container_cpu_usage_seconds_total{%s}[5m])) * 1000`, sel)
		},
	},
	MetricNamespaceMemory: {
		unit:        UnitBytes,
		legend:      "pod",
		namespaced:  true,
		description: "Working set per pod in this namespace.",
		query: func(sel selector) string {
			return withFallback(`sum by (pod) (container_memory_working_set_bytes{%s})`, sel)
		},
	},

	MetricClusterCPU: {
		unit:        UnitMillicores,
		legend:      "",
		description: "CPU used across every namespace.",
		query: func(sel selector) string {
			return withFallback(`sum(rate(container_cpu_usage_seconds_total{%s}[5m])) * 1000`, sel)
		},
	},
	MetricClusterMemory: {
		unit:        UnitBytes,
		legend:      "",
		description: "Working set across every namespace.",
		query: func(sel selector) string {
			return withFallback(`sum(container_memory_working_set_bytes{%s})`, sel)
		},
	},
}

// withFallback renders one aggregation twice — over the container-level series
// and over the pod-level ones — and joins them with PromQL's `or`, so the same
// entry answers on a Prometheus that labels cadvisor series with `container` and
// on one that does not. See the note above the catalogue for why that varies.
func withFallback(aggregation string, sel selector) string {
	return fmt.Sprintf(aggregation, sel.containers()) +
		" or " + fmt.Sprintf(aggregation, sel.pods())
}

// MetricKinds lists the catalogue, for a client that wants to know what it may
// ask for without hard-coding the same list a second time.
func MetricKinds() []string {
	out := make([]string, 0, len(metricCatalogue))
	for kind := range metricCatalogue {
		out = append(out, string(kind))
	}
	sort.Strings(out)
	return out
}

// selector is the validated set of label matchers a query is built around.
// Nothing reaches it that has not been through validateName or the scope.
type selector struct {
	namespaces []string
	pod        string
	container  string
}

// containers renders the label matchers for a container-level metric.
func (s selector) containers() string {
	matchers := []string{`container!=""`, `container!="POD"`}

	switch {
	case len(s.namespaces) == 1:
		matchers = append(matchers, fmt.Sprintf(`namespace=%q`, s.namespaces[0]))
	case len(s.namespaces) > 1:
		matchers = append(matchers,
			fmt.Sprintf(`namespace=~"%s"`, promLabelAlternation(s.namespaces)))
	}
	if s.pod != "" {
		matchers = append(matchers, fmt.Sprintf(`pod=%q`, s.pod))
	}
	if s.container != "" {
		matchers = append(matchers, fmt.Sprintf(`container=%q`, s.container))
	}
	return joinMatchers(matchers)
}

// pods renders the matchers for the pod-level rollup series — one per pod, which
// is what a Prometheus that does not promote the `container` label leaves behind.
// `pod!=""` is what separates them from the node and cgroup-root series, which
// carry no pod at all and would otherwise be added to every total.
func (s selector) pods() string {
	matchers := []string{`pod!=""`}

	switch {
	case len(s.namespaces) == 1:
		matchers = append(matchers, fmt.Sprintf(`namespace=%q`, s.namespaces[0]))
	case len(s.namespaces) > 1:
		matchers = append(matchers,
			fmt.Sprintf(`namespace=~"%s"`, promLabelAlternation(s.namespaces)))
	}
	if s.pod != "" {
		matchers = append(matchers, fmt.Sprintf(`pod=%q`, s.pod))
	}
	// The container is deliberately not matched: this branch exists precisely
	// because that label is missing, so naming it would match nothing again.
	return joinMatchers(matchers)
}

func joinMatchers(matchers []string) string {
	out := ""
	for i, matcher := range matchers {
		if i > 0 {
			out += ","
		}
		out += matcher
	}
	return out
}

// MetricRequest is one chart's worth of question.
type MetricRequest struct {
	Kind      MetricKind
	Namespace string
	Pod       string
	Container string
	Window    Window
}

// Point is one sample. The value is a float because that is what the backend
// stores; a missing sample is an absent point rather than a zero, since a gap in
// a chart and a genuine zero are different facts.
type Point struct {
	At    time.Time `json:"at"`
	Value float64   `json:"value"`
}

// Series is one line on a chart.
type Series struct {
	// Name is the legend entry, taken from the spec's legend label.
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Points []Point           `json:"points"`
}

// MetricResult is a whole chart.
type MetricResult struct {
	Kind   MetricKind `json:"kind"`
	Unit   Unit       `json:"unit"`
	Series []Series   `json:"series"`

	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	// StepSeconds is the resolution actually used, which is derived from the
	// range rather than taken from the caller.
	StepSeconds int `json:"step_seconds"`

	// Truncated says the backend returned more series than were sent on. The
	// chart is still honest about what it draws; it just is not everything.
	Truncated bool `json:"truncated,omitempty"`
	// Query is the PromQL KubeMG built. It is returned because an operator
	// staring at an empty chart needs to know what was actually asked — the most
	// common cause is a backend that labels its series differently.
	Query string `json:"query"`
	// Description is the catalogue entry's own explanation.
	Description string `json:"description,omitempty"`
}

// QueryMetrics runs one catalogue entry against a cluster's metrics datasource.
func QueryMetrics(ctx context.Context, target Target, tunnel TunnelCall,
	scope Scope, req MetricRequest,
) (MetricResult, error) {
	spec, known := metricCatalogue[req.Kind]
	if !known {
		return MetricResult{}, fmt.Errorf("%q is not a metric KubeMG charts", req.Kind)
	}
	if target.Kind != db.SourceMetrics {
		return MetricResult{}, fmt.Errorf("this datasource does not serve metrics")
	}
	if err := target.Validate(); err != nil {
		return MetricResult{}, err
	}
	if target.AccessMode == db.AccessInCluster && tunnel == nil {
		return MetricResult{}, fmt.Errorf(
			"an in-cluster datasource can only be read through a connected agent")
	}

	sel, err := buildSelector(spec, scope, req)
	if err != nil {
		return MetricResult{}, err
	}

	window, err := req.Window.Normalize(time.Now().UTC())
	if err != nil {
		return MetricResult{}, err
	}

	promQL := spec.query(sel)
	result := MetricResult{
		Kind:        req.Kind,
		Unit:        spec.unit,
		Start:       window.Start,
		End:         window.End,
		StepSeconds: int(window.Step.Seconds()),
		Query:       promQL,
		Description: spec.description,
		Series:      []Series{},
	}

	path := rangeQueryPath(promQL, window)

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	status, body, err := callLimited(ctx, target, path, tunnel, maxQueryBody, queryTimeout)
	if err != nil {
		return MetricResult{}, err
	}
	if status < 200 || status >= 300 {
		return MetricResult{}, fmt.Errorf("%s", explain(target, status, body))
	}

	series, truncated, err := decodeMatrix(body, spec.legend)
	if err != nil {
		return MetricResult{}, err
	}
	result.Series = series
	result.Truncated = truncated
	return result, nil
}

// buildSelector resolves the request against the scope, refusing anything the
// caller was not granted before a single character of PromQL is written.
func buildSelector(spec metricSpec, scope Scope, req MetricRequest) (selector, error) {
	var sel selector

	if err := validateName("pod", req.Pod); err != nil {
		return sel, err
	}
	if err := validateName("container", req.Container); err != nil {
		return sel, err
	}
	sel.pod = req.Pod
	sel.container = req.Container

	if !spec.namespaced {
		// A cluster-wide chart reaches past a namespace scope by definition, so a
		// scoped grant is refused it — the same rule `requireClusterScope` applies
		// to a cluster-wide resource list.
		if !scope.Unscoped() {
			return sel, fmt.Errorf(
				"this is a cluster-wide reading, and your access is limited to named namespaces")
		}
		return sel, nil
	}

	namespaces, err := scope.resolveNamespace(req.Namespace)
	if err != nil {
		return sel, err
	}
	// A pod named without a namespace is ambiguous across the cluster, and
	// answering it would silently chart a different pod of the same name.
	if sel.pod != "" && len(namespaces) != 1 {
		return sel, fmt.Errorf("charting one pod needs the namespace it is in")
	}
	sel.namespaces = namespaces
	return sel, nil
}

// rangeQueryPath renders the Prometheus range-query call. All four supported
// backends serve this path with these parameters; it is the shared surface that
// made them one provider family in the first place.
func rangeQueryPath(promQL string, window Window) string {
	params := url.Values{}
	params.Set("query", promQL)
	params.Set("start", strconv.FormatInt(window.Start.Unix(), 10))
	params.Set("end", strconv.FormatInt(window.End.Unix(), 10))
	params.Set("step", strconv.Itoa(int(window.Step.Seconds())))
	return "/api/v1/query_range?" + params.Encode()
}

// promResponse is the envelope all four backends answer in.
type promResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			// Values is a matrix's samples: [unix seconds, "value as a string"].
			// The value is a string in the wire format because JSON numbers
			// cannot carry NaN or the full float64 range, which Prometheus needs.
			Values [][2]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// decodeMatrix turns a range-query answer into series. A backend that answered
// but has nothing to say produces no series rather than an error: "no data for
// this window" is a legitimate and common answer, and it is the UI's job to say
// so rather than this function's job to fail.
func decodeMatrix(body []byte, legend string) ([]Series, bool, error) {
	var payload promResponse
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

	truncated := len(payload.Data.Result) > maxSeries
	results := payload.Data.Result
	if truncated {
		results = results[:maxSeries]
	}

	series := make([]Series, 0, len(results))
	for _, entry := range results {
		points := make([]Point, 0, len(entry.Values))
		for _, sample := range entry.Values {
			at, value, ok := decodeSample(sample)
			if !ok {
				// A sample that will not parse is dropped rather than failing the
				// chart: one NaN in an hour of data should not blank the panel.
				continue
			}
			points = append(points, Point{At: at, Value: value})
		}
		series = append(series, Series{
			Name:   seriesName(entry.Metric, legend),
			Labels: entry.Metric,
			Points: points,
		})
	}

	// Stable ordering, so a chart's colours do not reshuffle between refreshes.
	sort.Slice(series, func(i, j int) bool { return series[i].Name < series[j].Name })
	return series, truncated, nil
}

// decodeSample reads one [timestamp, value] pair. Both halves arrive as raw
// JSON: the timestamp is a number that may carry a fractional part, and the
// value is a quoted string that may be `NaN`.
func decodeSample(sample [2]json.RawMessage) (time.Time, float64, bool) {
	var seconds float64
	if err := json.Unmarshal(sample[0], &seconds); err != nil {
		return time.Time{}, 0, false
	}

	var raw string
	if err := json.Unmarshal(sample[1], &raw); err != nil {
		return time.Time{}, 0, false
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return time.Time{}, 0, false
	}
	// NaN and ±Inf parse happily and are what Prometheus writes for a gap or a
	// staleness marker — and neither is valid JSON, so one reaching the response
	// would fail the *whole* encode rather than spoiling one point. A gap is an
	// absent point, which is also what it should look like on a chart.
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return time.Time{}, 0, false
	}

	at := time.Unix(int64(seconds), int64((seconds-float64(int64(seconds)))*1e9)).UTC()
	return at, value, true
}

// seriesName picks the legend entry. A series whose legend label is empty — a
// cluster total, or a container the backend did not label — is named for what it
// is rather than left blank in a legend.
func seriesName(labels map[string]string, legend string) string {
	if legend == "" {
		return "total"
	}
	if name := labels[legend]; name != "" {
		return name
	}
	// The pod-level fallback answered, so the grouping label the chart asked for
	// does not exist on these series. Name the series for the granularity that
	// actually came back rather than calling it "unlabelled" — on a per-container
	// chart of one pod, the honest legend is the pod's own name.
	for _, alternative := range []string{"pod", "namespace"} {
		if name := labels[alternative]; name != "" {
			return name
		}
	}
	return "total"
}
