package observability

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * The usage profile is the waste report's only read of history, and it is a
 * query the caller does not write — so what is pinned is the query: that the
 * scope reaches every selector, that the fallback cannot double a pod, that a
 * month costs no more points than a week, and that the answer is keyed by
 * container without inventing readings.
 */

func inClusterMetrics() Target {
	return Target{
		Kind:             db.SourceMetrics,
		Provider:         db.ProviderVictoriaMetrics,
		AccessMode:       db.AccessInCluster,
		ServiceNamespace: "monitoring",
		ServiceName:      "vmsingle",
		ServicePort:      "8429",
		ServiceScheme:    "http",
		AuthMode:         db.AuthNone,
	}
}

func TestProfileQueriesCarryTheScopeOnEverySelector(t *testing.T) {
	sel := selector{namespaces: []string{"shop", "pay"}}
	for name, query := range map[string]string{
		"cpu":     cpuProfileQuery(sel, 7*24*time.Hour),
		"memory":  memoryProfileQuery(sel, 7*24*time.Hour),
		"history": historyProfileQuery(sel, 7*24*time.Hour),
	} {
		if got, want := strings.Count(query, `namespace=~"shop|pay"`), strings.Count(query, "{"); got != want {
			t.Errorf("%s scopes %d of its %d selectors: %s", name, got, want, query)
		}
	}
}

// A plain `or` between a by-container and a by-pod aggregation never matches,
// so every pod-level series would be added on top of the container ones.
func TestProfileFallbackMatchesOnThePod(t *testing.T) {
	for name, query := range map[string]string{
		"cpu":     cpuProfileQuery(selector{}, 24*time.Hour),
		"memory":  memoryProfileQuery(selector{}, 24*time.Hour),
		"history": historyProfileQuery(selector{}, 24*time.Hour),
	} {
		if !strings.Contains(query, "or on (namespace, pod)") {
			t.Errorf("%s fallback does not match on the pod: %s", name, query)
		}
		if !strings.Contains(query, `container!="POD"`) || !strings.Contains(query, `pod!=""`) {
			t.Errorf("%s is missing the container or pod-level matchers: %s", name, query)
		}
	}
}

func TestProfileQueriesUseTheWindow(t *testing.T) {
	cpu := cpuProfileQuery(selector{}, 7*24*time.Hour)
	if !strings.Contains(cpu, "quantile_over_time(0.95,") || !strings.Contains(cpu, "[604800s:300s]") {
		t.Errorf("a week's CPU p95 should be a 5m-step subquery over 604800s: %s", cpu)
	}
	memory := memoryProfileQuery(selector{}, 7*24*time.Hour)
	if strings.Count(memory, "max_over_time(") != 2 || strings.Count(memory, "[604800s]") != 2 {
		t.Errorf("memory peak should take max_over_time over the window on both halves: %s", memory)
	}
}

func TestSubqueryStepCoarsensRatherThanGrows(t *testing.T) {
	for _, tc := range []struct {
		span time.Duration
		want time.Duration
	}{
		{24 * time.Hour, 5 * time.Minute},
		{7 * 24 * time.Hour, 5 * time.Minute},
		{30 * 24 * time.Hour, 22 * time.Minute},
	} {
		got := subqueryStep(tc.span)
		if got != tc.want {
			t.Errorf("step for %s = %s, want %s", tc.span, got, tc.want)
		}
		if points := tc.span / got; points > maxSubqueryPoints {
			t.Errorf("a %s window evaluates %d points, more than %d", tc.span, points, maxSubqueryPoints)
		}
	}
}

func TestDecodeProfileKeysByContainerAndKeepsThePeak(t *testing.T) {
	body := []byte(`{"status":"success","data":{"resultType":"vector","result":[
		{"metric":{"namespace":"shop","pod":"web-1","container":"app"},"value":[1,"120"]},
		{"metric":{"namespace":"shop","pod":"web-1","container":"app"},"value":[1,"300"]},
		{"metric":{"namespace":"shop","pod":"web-1","container":"app"},"value":[1,"200"]},
		{"metric":{"namespace":"shop","pod":"legacy-0"},"value":[1,"50"]},
		{"metric":{"namespace":"shop","pod":"web-1","container":"nan"},"value":[1,"NaN"]},
		{"metric":{"pod":"orphan"},"value":[1,"9"]}
	]}}`)
	got, truncated, err := decodeProfile(body)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Error("six series is not past the bound")
	}
	if v := got[UsageKey{"shop", "web-1", "app"}]; v != 300 {
		t.Errorf("a container answering three times keeps its peak, got %v", v)
	}
	if v, ok := got[UsageKey{"shop", "legacy-0", ""}]; !ok || v != 50 {
		t.Errorf("a pod-level series is keyed with no container, got %v %v", v, ok)
	}
	if _, ok := got[UsageKey{"shop", "web-1", "nan"}]; ok {
		t.Error("a NaN is not a reading")
	}
	if len(got) != 2 {
		t.Errorf("a series with no namespace is not a container's; got %d keys", len(got))
	}
}

func TestDecodeProfileReportsARefusal(t *testing.T) {
	if _, _, err := decodeProfile([]byte(`{"status":"error","error":"bad_data: parse error"}`)); err == nil ||
		!strings.Contains(err.Error(), "parse error") {
		t.Errorf("a refusal should carry the backend's words, got %v", err)
	}
}

func TestQueryUsageProfileJoinsBothReadingsInScope(t *testing.T) {
	var asked []string
	tunnel := func(_ context.Context, _, path string, _ http.Header, _ []byte) (int, []byte, error) {
		parsed, err := url.Parse(path)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query().Get("query")
		asked = append(asked, query)
		if strings.Contains(query, "timestamp(") {
			return 200, []byte(`{"status":"success","data":{"result":[
				{"metric":{"namespace":"shop","pod":"web-1","container":"app"},"value":[1,"1760000000"]},
				{"metric":{"namespace":"shop","pod":"gone-1","container":"app"},"value":[1,"1760000000"]}]}}`), nil
		}
		if strings.HasPrefix(query, "quantile_over_time") {
			return 200, []byte(`{"status":"success","data":{"result":[
				{"metric":{"namespace":"shop","pod":"web-1","container":"app"},"value":[1,"42.5"]}]}}`), nil
		}
		return 200, []byte(`{"status":"success","data":{"result":[
			{"metric":{"namespace":"shop","pod":"web-1","container":"app"},"value":[1,"104857600"]},
			{"metric":{"namespace":"shop","pod":"web-1","container":"sidecar"},"value":[1,"1048576"]}]}}`), nil
	}

	end := time.Now().UTC()
	result, err := QueryUsageProfile(context.Background(), inClusterMetrics(), tunnel,
		Scope{Namespaces: []string{"shop"}},
		Window{Start: end.Add(-7 * 24 * time.Hour), End: end})
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 3 {
		t.Fatalf("expected three queries, got %d", len(asked))
	}
	for _, query := range asked {
		if strings.Count(query, `namespace="shop"`) != strings.Count(query, "{") {
			t.Errorf("a scoped caller's query reached past the grant: %s", query)
		}
	}

	app := result.Profiles[UsageKey{"shop", "web-1", "app"}]
	if app.CPU == nil || *app.CPU != 42.5 || app.Memory == nil || *app.Memory != 104857600 {
		t.Errorf("app profile = %+v", app)
	}
	sidecar := result.Profiles[UsageKey{"shop", "web-1", "sidecar"}]
	if sidecar.CPU != nil {
		t.Error("a container with no CPU series is not measured, not zero")
	}
	if app.Since == nil || !app.Since.Equal(time.Unix(1760000000, 0)) {
		t.Errorf("app history should start at its first sample, got %v", app.Since)
	}
	if _, kept := result.Profiles[UsageKey{"shop", "gone-1", "app"}]; kept {
		t.Error("a history with no reading is not a profile")
	}
	if result.CPUQuery == "" || result.MemoryQuery == "" || result.HistoryQuery == "" {
		t.Error("the queries asked travel with the answer")
	}
}

func TestQueryUsageProfileRefusesWhatItCannotRead(t *testing.T) {
	end := time.Now().UTC()
	window := Window{Start: end.Add(-24 * time.Hour), End: end}

	if _, err := QueryUsageProfile(context.Background(), inClusterMetrics(), nil, Scope{}, window); err == nil {
		t.Error("an in-cluster source with no tunnel should be refused")
	}
	logs := inClusterMetrics()
	logs.Kind = db.SourceLogs
	if _, err := QueryUsageProfile(context.Background(), logs, nil, Scope{}, window); err == nil {
		t.Error("a logs source does not serve metrics")
	}
	tooLong := Window{Start: end.Add(-31 * 24 * time.Hour), End: end}
	tunnel := func(context.Context, string, string, http.Header, []byte) (int, []byte, error) { return 200, nil, nil }
	if _, err := QueryUsageProfile(context.Background(), inClusterMetrics(), tunnel, Scope{}, tooLong); err == nil {
		t.Error("a window past thirty days should be refused")
	}
}
