package observability

import (
	"strings"
	"testing"
	"time"
)

/*
 * The comparison table adds a second query and a second reading of the same
 * scope, which is two more places for the scope to be lost. What is pinned here
 * is that it is not: the summary carries the same matchers the chart does, the
 * ranking is applied where it belongs, and a delta is only claimed where both
 * windows actually answered.
 */

// Every comparable entry has to scope every selector it writes, exactly as its
// chart does — a summary is a second hand-written query, and a second query is
// where a scope goes missing.
func TestEverySummaryCarriesTheScope(t *testing.T) {
	for kind, spec := range metricCatalogue {
		if spec.summary == nil {
			continue
		}
		query := spec.summary(selector{namespaces: []string{"shop"}}, "3600s")

		if got, want := strings.Count(query, `namespace="shop"`), strings.Count(query, "{"); got != want {
			t.Errorf("%s scopes %d of its %d selectors: %s", kind, got, want, query)
		}
		// The span reaches every range selector, or half the table is measured
		// over five minutes while the other half is measured over the window.
		if strings.Contains(query, "[5m]") {
			t.Errorf("%s summary still carries the chart's fixed range: %s", kind, query)
		}
		if !strings.Contains(query, "[3600s]") {
			t.Errorf("%s summary does not use the window: %s", kind, query)
		}
	}
}

// The three readings this phase added are the ones where a rise means something
// went wrong, and that judgement belongs to the catalogue rather than to the
// browser: it is what decides whether the delta column may spend colour.
func TestOnlyFailureReadingsAreMarkedWorseWhenTheyRise(t *testing.T) {
	worse := map[MetricKind]bool{
		MetricPodRestarts:        true,
		MetricContainersNotReady: true,
		MetricCPUThrottling:      true,
	}
	for kind, spec := range metricCatalogue {
		got := spec.riseOrNeutral() == TrendWorse
		if got != worse[kind] {
			t.Errorf("%s: rise-is-worse = %v, want %v", kind, got, worse[kind])
		}
	}
}

// A comparison is only meaningful where the entry can be collapsed to one number
// per entity, so every entry either has a summary or is explicitly refused —
// never silently answered with something else.
func TestAnEntryWithNoSummaryIsRefusedRatherThanApproximated(t *testing.T) {
	spec := metricSpec{legend: "pod", namespaced: true}
	if spec.summary != nil {
		t.Fatal("this test is about the nil case")
	}
	// The refusal itself lives in QueryCompare; what is pinned here is that the
	// catalogue as shipped has no half-configured entry, which is the state that
	// would reach it.
	for kind, entry := range metricCatalogue {
		if entry.summary == nil {
			t.Errorf("%s has no summary and would be uncomparable", kind)
		}
	}
}

func TestRankingIsAppliedOnlyWhereThereIsSomethingToRank(t *testing.T) {
	if got := rank("sum(x)", "pod", 5); got != "topk(5, sum(x))" {
		t.Fatalf("rank = %q, want the ranking applied", got)
	}
	// A cluster total is one series. Ranking it produces the same series with a
	// position nobody asked about.
	if got := rank("sum(x)", "", 5); got != "sum(x)" {
		t.Fatalf("rank = %q, want a single-series entry left alone", got)
	}
}

func TestSpanIsRenderedExactlyAndFloored(t *testing.T) {
	// Six hours divided by anything is rarely a whole number of hours, so the
	// duration is written in seconds rather than rounded into a friendlier unit.
	if got := promDuration(6 * time.Hour); got != "21600s" {
		t.Fatalf("duration = %q, want exact seconds", got)
	}
	// Below the floor a rate has too few samples to answer at all, so a tiny
	// window is widened rather than answered emptily.
	if got := promDuration(3 * time.Second); got != "60s" {
		t.Fatalf("duration = %q, want the floor", got)
	}
}

func TestDeltaIsClaimedOnlyWhereBothWindowsAnswered(t *testing.T) {
	current := map[string]sample{
		"checkout": {value: 120},
		"search":   {value: 50},
		"new-pod":  {value: 10},
		"quiet":    {value: 5},
	}
	previous := map[string]sample{
		"checkout": {value: 100},
		"search":   {value: 80},
		"quiet":    {value: 0},
	}

	rows := mergeRows(current, previous, 5)
	byName := map[string]CompareRow{}
	for _, row := range rows {
		byName[row.Name] = row
	}

	if rows[0].Name != "checkout" || rows[1].Name != "search" {
		t.Fatalf("rows are not ranked by the current window: %v", rows)
	}
	if got := byName["checkout"]; got.Delta == nil || *got.Delta != 20 {
		t.Fatalf("checkout delta = %v, want +20", got.Delta)
	}
	if got := byName["checkout"]; got.DeltaRatio == nil || *got.DeltaRatio != 0.2 {
		t.Fatalf("checkout ratio = %v, want 0.2", got.DeltaRatio)
	}
	if got := byName["search"]; got.Delta == nil || *got.Delta != -30 {
		t.Fatalf("search delta = %v, want -30", got.Delta)
	}

	// A pod that did not exist in the previous window is *new*, which is a
	// different fact from "was zero" and must not be rendered as an increase
	// from nothing.
	if got := byName["new-pod"]; got.Previous != nil || got.Delta != nil {
		t.Fatalf("a new entity carries a comparison: %+v", got)
	}
	// Something that was genuinely zero has a delta but no ratio: everything is
	// an infinite increase over nothing.
	if got := byName["quiet"]; got.Previous == nil || got.Delta == nil || got.DeltaRatio != nil {
		t.Fatalf("a previously-zero entity is misreported: %+v", got)
	}
}

func TestTheTableIsCappedAndStableWhenReadingsTie(t *testing.T) {
	current := map[string]sample{"b": {value: 1}, "a": {value: 1}, "c": {value: 9}, "d": {value: 8}}

	rows := mergeRows(current, nil, 3)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want the table capped at 3", len(rows))
	}
	// Ties break by name, so a refresh does not reshuffle the table over a
	// difference of nothing.
	if rows[0].Name != "c" || rows[1].Name != "d" || rows[2].Name != "a" {
		t.Fatalf("unstable ordering: %v", rows)
	}
}

func TestDecodeVectorKeepsOnlyWhatTheTableAskedFor(t *testing.T) {
	body := []byte(`{"status":"success","data":{"resultType":"vector","result":[
		{"metric":{"pod":"checkout"},"value":[1700000000,"12.5"]},
		{"metric":{"pod":"search"},"value":[1700000000,"3"]},
		{"metric":{"pod":"noisy"},"value":[1700000000,"NaN"]}
	]}}`)

	all, err := decodeVector(body, "pod", nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// NaN is what a ratio with no denominator comes back as, and it is dropped
	// rather than shown as a measured zero.
	if len(all) != 2 {
		t.Fatalf("decoded %d series, want the NaN dropped: %v", len(all), all)
	}
	if all["checkout"].value != 12.5 {
		t.Fatalf("checkout = %v, want 12.5", all["checkout"].value)
	}

	// The previous window's query is unranked, so on a large cluster it answers
	// with everything. Only the ranked names are ever held.
	kept, err := decodeVector(body, "pod", map[string]bool{"search": true})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(kept) != 1 || kept["search"].value != 3 {
		t.Fatalf("kept = %v, want only the requested name", kept)
	}
}

func TestDecodeVectorSeparatesEmptyFromRefused(t *testing.T) {
	empty, err := decodeVector([]byte(`{"status":"success","data":{"result":[]}}`), "pod", nil)
	if err != nil {
		t.Fatalf("an empty answer is not an error: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no series, got %v", empty)
	}

	if _, err := decodeVector(
		[]byte(`{"status":"error","error":"parse error"}`), "pod", nil); err == nil {
		t.Fatal("expected a refusal to be reported")
	}
}
