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
 * The comparison table: the top few of something, and how each one compares with
 * the window before this one.
 *
 * A chart answers "what shape is this". It does not answer the question an
 * operator actually arrives with — *what is worst right now, and is it worse
 * than it was* — because reading a rank off a chart with forty lines is not
 * reading, and reading a change off it means remembering what an hour ago looked
 * like. Both halves of that cost a query, which is why this is not free and why
 * it is a route of its own rather than a flag on the chart.
 *
 * **Top-N is done here and not on the chart.** `topk` in a range query is
 * evaluated at every step, so the membership changes mid-window and lines
 * appear and vanish — a chart that lies about which pods were in it. Ranking
 * over the window once, as an instant query, has no such wobble: every row is
 * one number for the whole span.
 *
 * **Only the current window is ranked.** The window before it is read unranked
 * and used purely as a lookup, because a previous window with a `topk` of its
 * own has its own membership: a pod that sat sixth an hour ago would be missing
 * from it, and missing is how this reports "new in this window". That would turn
 * an ordinary pod moving up one place into a row claiming it appeared from
 * nowhere — precisely the false alarm a delta column exists to prevent. Only the
 * ranked names are kept from that answer, so what is held in memory is bounded
 * by the table even when the query is not.
 *
 * **The comparison is the same query at an earlier evaluation time**, not the
 * same query with `offset` spliced into it. PromQL's offset attaches to each
 * selector rather than to an expression, so building it would mean rewriting
 * somebody's hand-written PromQL by hand — and an instant query already takes
 * the instant it is evaluated at. Asking the identical expression at
 * `end - span` is exactly the previous window, with nothing rewritten.
 *
 * Everything the chart path enforces still holds unchanged: the caller names a
 * catalogue entry rather than a query, names are validated rather than escaped,
 * the scope is resolved from the grant and a cluster-wide entry is refused to a
 * scoped caller.
 */

const (
	// defaultTopK is the five of the pattern. It is small on purpose: a table
	// nobody scrolls is a table people read.
	defaultTopK = 5
	// maxTopK bounds what a caller may ask for. Past this it stops being a
	// comparison and becomes a listing, which the resource browser already is.
	maxTopK = 20
	// minSummarySpan floors the aggregation range. A `rate` over ten seconds is
	// usually two samples and often none, so a window shorter than this would
	// produce an empty table rather than a fast one.
	minSummarySpan = time.Minute
)

// CompareRequest is one table's worth of question.
type CompareRequest struct {
	Kind      MetricKind
	Namespace string
	Pod       string
	Container string
	// TopK is how many rows to rank. Zero takes the default.
	TopK   int
	Window Window
}

// CompareRow is one ranked entity across two windows.
type CompareRow struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	// Current is the reading over the window asked for.
	Current float64 `json:"current"`
	// Previous is the same reading over the window immediately before it, and is
	// absent when the entity has no reading there at all. That is a real state
	// and a common one — a pod created inside this window has no previous — and
	// it is deliberately not folded into "previously zero", because "new" and
	// "was quiet" are different things to be told.
	Previous *float64 `json:"previous,omitempty"`
	// Delta and DeltaRatio are absent for the same reason. A ratio is also
	// absent when the previous reading was zero: everything is an infinite
	// increase over nothing, which is a division rather than a fact.
	Delta      *float64 `json:"delta,omitempty"`
	DeltaRatio *float64 `json:"delta_ratio,omitempty"`
}

// CompareResult is the whole table.
type CompareResult struct {
	Kind MetricKind `json:"kind"`
	Unit Unit       `json:"unit"`
	// Rise says whether more is worse, which is what decides whether the delta
	// column is allowed to spend colour. See Trend.
	Rise Trend `json:"rise"`
	// Legend names what a row *is* — a pod, a namespace, a container — so the
	// table can head its first column with it rather than with "name".
	Legend string       `json:"legend,omitempty"`
	Rows   []CompareRow `json:"rows"`
	TopK   int          `json:"topk"`

	// The window compared, and the one compared against.
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	CompareStart time.Time `json:"compare_start"`
	CompareEnd   time.Time `json:"compare_end"`

	// Query is the PromQL the ranking asked, and CompareQuery the unranked one
	// the previous window answered. Both are returned for the reason a chart
	// returns its own: an operator staring at an empty table needs to see what
	// was actually asked, and here there are two questions rather than one.
	Query        string `json:"query"`
	CompareQuery string `json:"compare_query"`
	// CompareUnavailable explains a missing comparison rather than leaving every
	// row looking new. The current window is the answer and the previous one is
	// what makes it useful, so a backend that could not serve the second query
	// costs the deltas and not the table.
	CompareUnavailable string `json:"compare_unavailable,omitempty"`
	Description        string `json:"description,omitempty"`
}

// QueryCompare ranks a catalogue entry over the window and against the window
// before it.
func QueryCompare(ctx context.Context, target Target, tunnel TunnelCall,
	scope Scope, req CompareRequest,
) (CompareResult, error) {
	spec, known := metricCatalogue[req.Kind]
	if !known {
		return CompareResult{}, fmt.Errorf("%q is not a metric KubeMG charts", req.Kind)
	}
	if spec.summary == nil {
		return CompareResult{}, fmt.Errorf("%q cannot be compared across windows", req.Kind)
	}
	if target.Kind != db.SourceMetrics {
		return CompareResult{}, fmt.Errorf("this datasource does not serve metrics")
	}
	if err := target.Validate(); err != nil {
		return CompareResult{}, err
	}
	if target.AccessMode == db.AccessInCluster && tunnel == nil {
		return CompareResult{}, fmt.Errorf(
			"an in-cluster datasource can only be read through a connected agent")
	}

	sel, err := buildSelector(spec, scope, req.asMetricRequest())
	if err != nil {
		return CompareResult{}, err
	}

	window, err := req.Window.Normalize(time.Now().UTC())
	if err != nil {
		return CompareResult{}, err
	}
	span := window.End.Sub(window.Start)
	if span < minSummarySpan {
		span = minSummarySpan
	}

	topK := req.TopK
	switch {
	case topK <= 0:
		topK = defaultTopK
	case topK > maxTopK:
		topK = maxTopK
	}

	summary := spec.summary(sel, promDuration(span))
	promQL := rank(summary, spec.legend, topK)

	result := CompareResult{
		Kind:         req.Kind,
		Unit:         spec.unit,
		Rise:         spec.riseOrNeutral(),
		Legend:       spec.legend,
		Rows:         []CompareRow{},
		TopK:         topK,
		Start:        window.End.Add(-span),
		End:          window.End,
		CompareStart: window.End.Add(-2 * span),
		CompareEnd:   window.End.Add(-span),
		Query:        promQL,
		CompareQuery: summary,
		Description:  spec.description,
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	current, err := instantVector(ctx, target, tunnel, promQL, result.End, spec.legend, nil)
	if err != nil {
		return CompareResult{}, err
	}

	// Only the entities that made the table are wanted from the previous window,
	// and only those are kept — the query is unranked, so on a large cluster its
	// answer is every pod, and none of the rest of it is ever held.
	wanted := make(map[string]bool, len(current))
	for name := range current {
		wanted[name] = true
	}
	previous, err := instantVector(ctx, target, tunnel, summary, result.CompareEnd, spec.legend, wanted)
	if err != nil {
		// A failed comparison is not a failed table. Saying so is the point: a
		// silent fallback would show every row as new, which reads as an incident
		// rather than as a missing second query.
		result.CompareUnavailable = err.Error()
		previous = nil
	}

	result.Rows = mergeRows(current, previous, topK)
	return result, nil
}

// asMetricRequest reuses the chart path's selector rules verbatim. The two
// requests carry the same fields for the same reasons, and validating them twice
// in two places is how they come to disagree.
func (r CompareRequest) asMetricRequest() MetricRequest {
	return MetricRequest{
		Kind:      r.Kind,
		Namespace: r.Namespace,
		Pod:       r.Pod,
		Container: r.Container,
		Window:    r.Window,
	}
}

// riseOrNeutral defaults an unset trend to neutral. The zero value of a Trend is
// the empty string, and a reading whose author did not say is not one whose
// delta should be coloured.
func (s metricSpec) riseOrNeutral() Trend {
	if s.rise == TrendWorse {
		return TrendWorse
	}
	return TrendNeutral
}

// rank wraps a summary in topk. An entry with no legend produces exactly one
// series — a cluster total — and `topk(5, <one series>)` is that same series with
// a rank nobody asked about, so it is left alone.
func rank(promQL, legend string, topK int) string {
	if legend == "" {
		return promQL
	}
	return fmt.Sprintf("topk(%d, %s)", topK, promQL)
}

// promDuration renders a span as PromQL's duration literal. Seconds rather than
// a friendlier unit because a window is derived arithmetic, not a preset — 6h
// divided by anything is rarely a whole number of hours, and `21600s` is exact
// where `6h` is only sometimes right.
func promDuration(d time.Duration) string {
	seconds := int64(d.Seconds())
	if seconds < int64(minSummarySpan.Seconds()) {
		seconds = int64(minSummarySpan.Seconds())
	}
	return strconv.FormatInt(seconds, 10) + "s"
}

// instantVector runs one instant query and reduces it to a value per series.
// A non-nil keep narrows what is retained to those names, so reading an unranked
// answer costs no more memory than the table it feeds.
func instantVector(ctx context.Context, target Target, tunnel TunnelCall,
	promQL string, at time.Time, legend string, keep map[string]bool,
) (map[string]sample, error) {
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
	return decodeVector(body, legend, keep)
}

// sample is one entity's reading at one instant.
type sample struct {
	labels map[string]string
	value  float64
}

// vectorResponse is the instant-query envelope. It differs from the matrix one
// by a single field — one `value` pair rather than a list of them — which is why
// it is spelled out here rather than bent out of promResponse with a
// json.RawMessage and a branch.
type vectorResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string  `json:"metric"`
			Value  [2]json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// decodeVector turns an instant-query answer into a reading per series name.
// A backend that answered with nothing produces an empty map rather than an
// error: no data for this window is a legitimate answer, and it is the table's
// job to say so.
func decodeVector(body []byte, legend string, keep map[string]bool) (map[string]sample, error) {
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

	size := len(payload.Data.Result)
	if keep != nil && len(keep) < size {
		size = len(keep)
	}
	out := make(map[string]sample, size)
	for _, entry := range payload.Data.Result {
		_, value, ok := decodeSample(entry.Value)
		if !ok {
			// NaN is what a ratio with no denominator comes back as — a pod with
			// no CPU quota cannot be throttled — and it is dropped rather than
			// shown as zero, which would read as "measured, and fine".
			continue
		}
		name := seriesName(entry.Metric, legend)
		if keep != nil && !keep[name] {
			continue
		}
		// Two series reducing to one name would otherwise silently overwrite each
		// other; summing is the only merge that keeps the column's total honest.
		if existing, seen := out[name]; seen {
			value += existing.value
		}
		out[name] = sample{labels: entry.Metric, value: value}
	}
	return out, nil
}

// mergeRows joins the two readings into the table.
//
// The rows are the *current* window's ranking. An entity that was in the top few
// an hour ago and is not now is deliberately absent rather than carried along
// with a fall to zero: the table answers "what is worst now", and a list that
// also holds everything that used to be worst stops being five rows long.
func mergeRows(current, previous map[string]sample, topK int) []CompareRow {
	rows := make([]CompareRow, 0, len(current))
	for name, now := range current {
		row := CompareRow{Name: name, Labels: now.labels, Current: now.value}
		if before, seen := previous[name]; seen {
			was := before.value
			delta := now.value - was
			row.Previous = &was
			row.Delta = &delta
			if was != 0 {
				ratio := delta / math.Abs(was)
				row.DeltaRatio = &ratio
			}
		}
		rows = append(rows, row)
	}

	// Highest first, and by name where two readings tie, so a table does not
	// reshuffle between refreshes over a difference of nothing.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Current != rows[j].Current {
			return rows[i].Current > rows[j].Current
		}
		return rows[i].Name < rows[j].Name
	})
	if len(rows) > topK {
		rows = rows[:topK]
	}
	return rows
}
