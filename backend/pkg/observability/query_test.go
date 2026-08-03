package observability

import (
	"strings"
	"testing"
	"time"
)

/*
 * The query path has no cluster RBAC behind it — a metrics backend answers
 * whatever it is asked — so the scope is enforced by KubeMG being the one that
 * writes the query. What is pinned here is exactly that: a scoped caller cannot
 * reach past their namespaces, nothing a caller sends becomes query syntax, and
 * the window cannot be widened into an outage.
 */

func TestScopedCallerCannotReachAnotherNamespace(t *testing.T) {
	scope := Scope{Namespaces: []string{"team-a", "team-b"}}

	if _, err := scope.resolveNamespace("team-c"); err == nil {
		t.Fatal("expected a namespace outside the grant to be refused")
	}

	// Naming one they hold is fine, and narrows to it.
	got, err := scope.resolveNamespace("team-a")
	if err != nil {
		t.Fatalf("expected a granted namespace to be allowed: %v", err)
	}
	if len(got) != 1 || got[0] != "team-a" {
		t.Fatalf("resolved = %v, want [team-a]", got)
	}

	// Naming none is answered across their own namespaces, never across the
	// cluster — the same rule the resource lists follow for all-namespaces.
	got, err = scope.resolveNamespace("")
	if err != nil {
		t.Fatalf("expected an unnamed namespace to resolve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("resolved = %v, want both granted namespaces", got)
	}
}

func TestUnscopedCallerReadsTheCluster(t *testing.T) {
	scope := Scope{}

	got, err := scope.resolveNamespace("")
	if err != nil {
		t.Fatalf("expected an unscoped caller to resolve: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("resolved = %v, want no namespace restriction", got)
	}
	if !scope.Allows("anything") {
		t.Fatal("an unscoped grant covers every namespace")
	}
}

// A cluster-wide chart reaches past a namespace scope by definition, so a scoped
// grant is refused it outright rather than being shown a filtered version of a
// total that would then be wrong.
func TestClusterWideMetricsAreRefusedToAScopedGrant(t *testing.T) {
	spec := metricCatalogue[MetricClusterCPU]
	if spec.namespaced {
		t.Fatal("the cluster CPU chart must not be namespaced")
	}

	_, err := buildSelector(spec, Scope{Namespaces: []string{"team-a"}}, MetricRequest{
		Kind: MetricClusterCPU,
	})
	if err == nil {
		t.Fatal("expected a scoped grant to be refused a cluster-wide reading")
	}

	if _, err := buildSelector(spec, Scope{}, MetricRequest{Kind: MetricClusterCPU}); err != nil {
		t.Fatalf("expected an unscoped grant to be allowed: %v", err)
	}
}

// Nothing a caller sends may become query syntax. A name is validated rather
// than escaped, so a value carrying a quote is refused before a query exists.
func TestNamesThatCouldBecomeSyntaxAreRefused(t *testing.T) {
	spec := metricCatalogue[MetricPodCPU]

	for _, pod := range []string{
		`checkout"} or up{`,
		`checkout\`,
		"checkout{a=1}",
		"Checkout",
		"check out",
		strings.Repeat("a", 300),
	} {
		_, err := buildSelector(spec, Scope{}, MetricRequest{
			Kind:      MetricPodCPU,
			Namespace: "shop",
			Pod:       pod,
		})
		if err == nil {
			t.Fatalf("expected pod name %q to be refused", pod)
		}
	}
}

// A pod is only unambiguous inside one namespace; charting one without saying
// where would silently pick up a same-named pod elsewhere.
func TestChartingOnePodNeedsItsNamespace(t *testing.T) {
	spec := metricCatalogue[MetricPodCPU]

	if _, err := buildSelector(spec, Scope{}, MetricRequest{
		Kind: MetricPodCPU,
		Pod:  "checkout",
	}); err == nil {
		t.Fatal("expected a pod without a namespace to be refused")
	}

	if _, err := buildSelector(spec, Scope{Namespaces: []string{"a", "b"}}, MetricRequest{
		Kind: MetricPodCPU,
		Pod:  "checkout",
	}); err == nil {
		t.Fatal("expected a pod across several granted namespaces to be refused")
	}
}

func TestSelectorAlwaysExcludesTheRollupAndPauseSeries(t *testing.T) {
	sel := selector{namespaces: []string{"shop"}, pod: "checkout"}
	matchers := sel.containers()

	// The kubelet exports a pod-level rollup with an empty container label; not
	// excluding it doubles every total.
	if !strings.Contains(matchers, `container!=""`) {
		t.Fatalf("matchers = %q, want the rollup series excluded", matchers)
	}
	// The pause container holds the network namespace and is not what anyone
	// means by what a pod is using.
	if !strings.Contains(matchers, `container!="POD"`) {
		t.Fatalf("matchers = %q, want the pause container excluded", matchers)
	}
	if !strings.Contains(matchers, `namespace="shop"`) || !strings.Contains(matchers, `pod="checkout"`) {
		t.Fatalf("matchers = %q, want the namespace and pod pinned", matchers)
	}
}

// A scoped caller reading several namespaces gets one query with an alternation
// rather than a cluster-wide one, and the dots in a DNS-style namespace are
// escaped so they cannot widen the match.
func TestScopedSelectorMatchesOnlyTheGrantedNamespaces(t *testing.T) {
	sel := selector{namespaces: []string{"team-a", "team.b"}}
	matchers := sel.containers()

	if !strings.Contains(matchers, `namespace=~"team-a|team\\.b"`) {
		t.Fatalf("matchers = %q, want an escaped alternation of the granted namespaces", matchers)
	}
}

/*
 * Whether cadvisor series carry a `container` label is decided by the scrape
 * config, not by cadvisor — kube-prometheus-stack promotes it, the
 * prometheus-community chart's node-cadvisor job does not. Without the fallback
 * every chart on the second kind of cluster comes back empty, because an absent
 * label compares equal to "" and `container!=""` then matches nothing.
 */
func TestEveryChartFallsBackToPodLevelSeries(t *testing.T) {
	for kind, spec := range metricCatalogue {
		query := spec.query(selector{namespaces: []string{"shop"}})

		// Every selector in every entry carries the scope. Counting braces
		// against namespace matchers is what catches a branch added later that
		// quietly reads the whole cluster — including the two halves of a ratio.
		if got, want := strings.Count(query, `namespace="shop"`), strings.Count(query, "{"); got != want {
			t.Errorf("%s scopes %d of its %d selectors: %s", kind, got, want, query)
		}

		// The fallback is a property of cadvisor's usage series, whose labels
		// depend on somebody's scrape config. kube-state-metrics labels its own
		// series, so those entries have nothing to fall back to and a fallback
		// there would only be a second copy of the same query.
		if !strings.Contains(query, "container_cpu") && !strings.Contains(query, "container_memory") {
			continue
		}
		if !strings.Contains(query, " or ") {
			t.Errorf("%s has no pod-level fallback: %s", kind, query)
		}
		// The container-level branch still excludes the rollup and the pause
		// container, or a cluster that *does* label containers double-counts.
		if !strings.Contains(query, `container!=""`) ||
			!strings.Contains(query, `container!="POD"`) {
			t.Errorf("%s lost its rollup exclusion: %s", kind, query)
		}
		// The fallback branch pins `pod!=""`, which is what separates the
		// per-pod series from the node and cgroup-root ones that carry no pod
		// and would otherwise be added to every total.
		if !strings.Contains(query, `pod!=""`) {
			t.Errorf("%s fallback does not exclude the node-level series: %s", kind, query)
		}
	}
}

// The fallback branch must never name the container label — that branch exists
// precisely because the label is missing, so matching on it would match nothing.
func TestPodLevelFallbackDoesNotMatchTheMissingLabel(t *testing.T) {
	sel := selector{namespaces: []string{"shop"}, pod: "checkout", container: "server"}

	if got := sel.pods(); strings.Contains(got, "container") {
		t.Fatalf("pod-level matchers = %q, want no container matcher", got)
	}
	if got := sel.containers(); !strings.Contains(got, `container="server"`) {
		t.Fatalf("container-level matchers = %q, want the container pinned", got)
	}
}

// When the fallback answers, the grouping label the chart asked for is absent —
// so the legend has to name the granularity that actually came back rather than
// showing a row called "unlabelled".
func TestSeriesNameFallsBackToTheGranularityThatAnswered(t *testing.T) {
	if got := seriesName(map[string]string{"container": "server"}, "container"); got != "server" {
		t.Fatalf("name = %q, want the container", got)
	}
	if got := seriesName(map[string]string{"pod": "checkout", "namespace": "shop"}, "container"); got != "checkout" {
		t.Fatalf("name = %q, want the pod when there is no container label", got)
	}
	if got := seriesName(map[string]string{}, ""); got != "total" {
		t.Fatalf("name = %q, want a cluster total to be named", got)
	}
}

func TestDecodeMatrixReadsThePrometheusEnvelope(t *testing.T) {
	body := []byte(`{"status":"success","data":{"resultType":"matrix","result":[
		{"metric":{"container":"server"},"values":[[1785315600,"12.5"],[1785315615,"13"]]},
		{"metric":{"container":"sidecar"},"values":[[1785315600,"1"]]}
	]}}`)

	series, truncated, err := decodeMatrix(body, "container")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if truncated {
		t.Fatal("two series is not a truncated answer")
	}
	if len(series) != 2 {
		t.Fatalf("series = %d, want 2", len(series))
	}
	// Sorted, so a chart's colours do not reshuffle between refreshes.
	if series[0].Name != "server" || series[1].Name != "sidecar" {
		t.Fatalf("names = %q/%q", series[0].Name, series[1].Name)
	}
	if len(series[0].Points) != 2 || series[0].Points[0].Value != 12.5 {
		t.Fatalf("points = %+v", series[0].Points)
	}
	if series[0].Points[0].At.IsZero() {
		t.Fatal("expected the sample timestamp to be parsed")
	}
}

// A backend that answered but has nothing to say is not an error: "no data for
// this window" is a legitimate answer and the UI says so. A backend that
// explicitly refused the query is.
func TestDecodeMatrixSeparatesEmptyFromRefused(t *testing.T) {
	empty, _, err := decodeMatrix([]byte(`{"status":"success","data":{"result":[]}}`), "pod")
	if err != nil {
		t.Fatalf("an empty result is not an error: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("series = %d, want none", len(empty))
	}

	if _, _, err := decodeMatrix(
		[]byte(`{"status":"error","error":"invalid parameter"}`), "pod"); err == nil {
		t.Fatal("expected a refused query to be an error")
	}
}

// One unparseable sample — a NaN, most often — drops that point rather than
// blanking the whole panel.
func TestDecodeMatrixDropsOnlyTheBadSample(t *testing.T) {
	body := []byte(`{"data":{"result":[
		{"metric":{"pod":"a"},"values":[[1785315600,"NaN"],[1785315615,"7"]]}
	]}}`)

	series, _, err := decodeMatrix(body, "pod")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(series) != 1 || len(series[0].Points) != 1 || series[0].Points[0].Value != 7 {
		t.Fatalf("series = %+v", series)
	}
}

func TestWindowIsBoundedAndResolutionDerived(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	// Nothing given is the default window, with a step the caller never chose.
	got, err := Window{}.Normalize(now)
	if err != nil {
		t.Fatalf("expected an empty window to resolve: %v", err)
	}
	if got.End != now || got.Start != now.Add(-defaultWindow) {
		t.Fatalf("window = %v..%v, want the default range ending now", got.Start, got.End)
	}
	if got.Step < minStep {
		t.Fatalf("step = %v, want at least %v", got.Step, minStep)
	}

	// A range past the ceiling is refused rather than silently narrowed: an
	// operator who asked for a year should be told they cannot have it.
	if _, err := (Window{Start: now.Add(-365 * 24 * time.Hour), End: now}).Normalize(now); err == nil {
		t.Fatal("expected a range past the ceiling to be refused")
	}

	// Backwards is a mistake worth naming.
	if _, err := (Window{Start: now, End: now.Add(-time.Hour)}).Normalize(now); err == nil {
		t.Fatal("expected a range that ends before it starts to be refused")
	}

	// Widening coarsens the step rather than enlarging the answer: the point
	// ceiling is what keeps a wide range from becoming a huge response.
	wide, err := (Window{Start: now.Add(-7 * 24 * time.Hour), End: now, Step: time.Second}).Normalize(now)
	if err != nil {
		t.Fatalf("expected a week to resolve: %v", err)
	}
	points := int(wide.End.Sub(wide.Start) / wide.Step)
	if points > maxPoints {
		t.Fatalf("points = %d, want at most %d", points, maxPoints)
	}
}
