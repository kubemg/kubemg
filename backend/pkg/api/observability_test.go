package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
	"github.com/kubemg/kubemg/backend/pkg/observability"
)

// promServer stands in for a reachable Prometheus-compatible datasource.
func promServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/status/buildinfo" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"version":"v2.53.0"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func directSourcePayload(url string) map[string]any {
	return map[string]any{
		"provider":    db.ProviderPrometheus,
		"access_mode": db.AccessDirect,
		"url":         url,
		"auth_mode":   db.AuthBearer,
		"credential":  "s3cret",
	}
}

func TestRegisterAMetricsSourceChecksItAndNeverEchoesTheCredential(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	backend := promServer(t)

	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/sources/metrics"
	rec := env.do(t, http.MethodPut, path, env.tokenFor(t, admin), directSourcePayload(backend.URL))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatal("the stored credential must never travel back out")
	}

	body := decode[struct {
		Source sourceResponse `json:"source"`
	}](t, rec)
	if !body.Source.HasCredential {
		t.Fatal("expected the response to say a credential is stored")
	}
	if body.Source.LastStatus != db.SourceStatusHealthy {
		t.Fatalf("expected the check to pass, got %q (%s)", body.Source.LastStatus, body.Source.LastMessage)
	}
	if body.Source.DetectedVersion != "v2.53.0" {
		t.Fatalf("expected the detected version, got %q", body.Source.DetectedVersion)
	}
	if body.Source.Endpoint != backend.URL {
		t.Fatalf("expected the endpoint to be rendered, got %q", body.Source.Endpoint)
	}
}

// An unreachable datasource is still saved: an operator may be configuring one
// before it exists. What must not happen is it being recorded as working.
func TestAnUnreachableSourceIsSavedButNotReportedHealthy(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/sources/metrics"
	rec := env.do(t, http.MethodPut, path, env.tokenFor(t, admin),
		directSourcePayload("http://127.0.0.1:1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	source := decode[struct {
		Source sourceResponse `json:"source"`
	}](t, rec).Source
	if source.LastStatus != db.SourceStatusUnhealthy {
		t.Fatalf("expected the source to be recorded unhealthy, got %q", source.LastStatus)
	}
	if source.LastMessage == "" {
		t.Fatal("expected the failure to be explained")
	}
	if _, err := env.store.ObservabilitySource(t.Context(), cluster.ID, db.SourceMetrics); err != nil {
		t.Fatalf("expected the source to be stored anyway: %v", err)
	}
}

// Editing the port should not make anyone re-type a token they already gave.
func TestOmittingTheCredentialKeepsTheStoredOne(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	token := env.tokenFor(t, admin)
	backend := promServer(t)

	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/sources/metrics"
	env.do(t, http.MethodPut, path, token, directSourcePayload(backend.URL))

	update := directSourcePayload(backend.URL)
	delete(update, "credential")
	update["path_prefix"] = "/prometheus"
	if rec := env.do(t, http.MethodPut, path, token, update); rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	stored, err := env.store.ObservabilitySource(t.Context(), cluster.ID, db.SourceMetrics)
	if err != nil {
		t.Fatalf("load source: %v", err)
	}
	if stored.Credential != "s3cret" {
		t.Fatalf("expected the stored credential to survive the edit, got %q", stored.Credential)
	}
	if stored.PathPrefix != "/prometheus" {
		t.Fatalf("expected the edit to land, got %q", stored.PathPrefix)
	}
}

// A direct-mode cluster has no tunnel, so there is nothing to reach a Service
// through. Saying so beats storing a source that can never answer.
func TestInClusterSourceIsRefusedForADirectModeCluster(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/sources/metrics"
	rec := env.do(t, http.MethodPut, path, env.tokenFor(t, admin), map[string]any{
		"provider":          db.ProviderVictoriaMetrics,
		"access_mode":       db.AccessInCluster,
		"service_namespace": "monitoring",
		"service_name":      "vmsingle",
		"service_port":      "8428",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusConflict, rec.Code, rec.Body.String())
	}
}

func TestAMalformedSourceIsRejected(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	token := env.tokenFor(t, admin)

	base := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/sources/"
	cases := map[string]struct {
		path    string
		payload map[string]any
		status  int
	}{
		"an unknown kind": {
			path:    base + "traces",
			payload: directSourcePayload("https://metrics.example.com"),
			status:  http.StatusBadRequest,
		},
		"a logs provider under metrics": {
			path: base + "metrics",
			payload: map[string]any{
				"provider": db.ProviderLoki, "access_mode": db.AccessDirect,
				"url": "https://loki.example.com",
			},
			status: http.StatusBadRequest,
		},
		"a direct source with no address": {
			path:    base + "metrics",
			payload: map[string]any{"provider": db.ProviderPrometheus, "access_mode": db.AccessDirect},
			status:  http.StatusBadRequest,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := env.do(t, http.MethodPut, tc.path, token, tc.payload)
			if rec.Code != tc.status {
				t.Fatalf("expected status %d, got %d (%s)", tc.status, rec.Code, rec.Body.String())
			}
		})
	}
}

// Reading which backend a cluster has is not a privilege: you cannot be shown a
// chart from a source you are not allowed to know exists. Changing it is.
func TestSourcesAreReadableByAGrantedUserAndWritableOnlyByAdmins(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	dev := env.store.addUser("dev", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	env.store.grant(dev.ID, cluster.ID, db.K8sRoleView, nil)
	backend := promServer(t)

	base := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability"
	env.do(t, http.MethodPut, base+"/sources/metrics", env.tokenFor(t, admin),
		directSourcePayload(backend.URL))

	devToken := env.tokenFor(t, dev)
	rec := env.do(t, http.MethodGet, base, devToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatal("the credential must not reach a reader")
	}

	body := decode[struct {
		Sources  []sourceResponse `json:"sources"`
		Editable bool             `json:"editable"`
	}](t, rec)
	if len(body.Sources) != 1 || body.Sources[0].Provider != db.ProviderPrometheus {
		t.Fatalf("expected the metrics source to be listed, got %+v", body.Sources)
	}
	if body.Editable {
		t.Fatal("expected a non-admin to be told they cannot edit")
	}

	for _, call := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPut, base + "/sources/metrics", directSourcePayload(backend.URL)},
		{http.MethodDelete, base + "/sources/metrics", nil},
		{http.MethodPost, base + "/sources/metrics/test", directSourcePayload(backend.URL)},
		{http.MethodPost, base + "/sources/metrics/check", nil},
	} {
		rec := env.do(t, call.method, call.path, devToken, call.body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected %s %s to be refused, got %d", call.method, call.path, rec.Code)
		}
	}
}

// A user with no grant on the cluster cannot even learn what it is wired to.
func TestSourcesAreHiddenFromAUserWithoutAccess(t *testing.T) {
	env := newTestEnv(t)
	stranger := env.store.addUser("stranger", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/observability", env.tokenFor(t, stranger), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

// Testing a draft is what makes the wizard's check honest: the address is
// checked while the operator is still looking at the field holding it.
func TestTestingADraftChecksItWithoutSavingAnything(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	backend := promServer(t)

	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/sources/metrics/test"
	rec := env.do(t, http.MethodPost, path, env.tokenFor(t, admin), directSourcePayload(backend.URL))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !decode[struct {
		Reachable bool `json:"reachable"`
	}](t, rec).Reachable {
		t.Fatalf("expected the draft to check out: %s", rec.Body.String())
	}

	if _, err := env.store.ObservabilitySource(t.Context(), cluster.ID, db.SourceMetrics); err == nil {
		t.Fatal("testing a draft must not store it")
	}
}

func TestDeletingASourceRemovesIt(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	token := env.tokenFor(t, admin)
	backend := promServer(t)

	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/sources/metrics"
	env.do(t, http.MethodPut, path, token, directSourcePayload(backend.URL))

	if rec := env.do(t, http.MethodDelete, path, token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}
	if rec := env.do(t, http.MethodDelete, path, token, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d on the second delete, got %d", http.StatusNotFound, rec.Code)
	}
}

/*
 * The console's time range, resolved here rather than in the browser.
 *
 * The point of the shared table is that "the last 6 hours" is the same span in a
 * chart, in the audit trail and in a pasted link — so what these check is that
 * the preset reaches the window the query is built from, that the vocabulary is
 * closed, and that an explicit boundary still beats a preset.
 */

// metricWindow runs a chart query and reports the window the server resolved.
func metricWindow(t *testing.T, env *testEnv, token string, cluster uint, query string) (time.Time, time.Time) {
	t.Helper()
	path := "/api/v1/clusters/" + itoa(cluster) + "/observability/metrics/query?metric=cluster_cpu" + query
	rec := env.do(t, http.MethodGet, path, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: expected status %d, got %d (%s)", query, http.StatusOK, rec.Code, rec.Body.String())
	}
	body := decode[struct {
		Result observability.MetricResult `json:"result"`
	}](t, rec)
	return body.Result.Start, body.Result.End
}

func TestAChartRangePresetIsResolvedServerSide(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	token := env.tokenFor(t, admin)
	backend := promServer(t)

	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/sources/metrics"
	if rec := env.do(t, http.MethodPut, path, token, directSourcePayload(backend.URL)); rec.Code != http.StatusOK {
		t.Fatalf("register the source: %d (%s)", rec.Code, rec.Body.String())
	}

	for _, tc := range []struct {
		query string
		span  time.Duration
	}{
		{"&range=15m", 15 * time.Minute},
		{"&range=6h", 6 * time.Hour},
		{"&range=30d", 30 * 24 * time.Hour},
		// "All time" against a datasource is as far back as this path allows: a
		// metrics backend has retention, and the cap is the widest honest answer.
		{"&range=all", observability.MaxWindow},
		// No preset at all is the engine's own default, unchanged.
		{"", time.Hour},
	} {
		start, end := metricWindow(t, env, token, cluster.ID, tc.query)
		if got := end.Sub(start); got != tc.span {
			t.Errorf("%q: expected a window of %s, got %s", tc.query, tc.span, got)
		}
		if time.Since(end) > time.Minute {
			t.Errorf("%q: expected the window to end about now, it ends at %s", tc.query, end)
		}
	}

	// The vocabulary is closed, so a preset this build does not know is refused
	// rather than quietly widened to something else.
	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/observability/metrics/query?metric=cluster_cpu&range=3y",
		token, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected an unknown preset to be refused, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// An incident has a start and an end, not a duration ending now — so a hand-set
// boundary is the more specific statement and wins over the console's preset.
func TestAnExplicitStartBeatsTheRangePreset(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	token := env.tokenFor(t, admin)
	backend := promServer(t)

	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/sources/metrics"
	if rec := env.do(t, http.MethodPut, path, token, directSourcePayload(backend.URL)); rec.Code != http.StatusOK {
		t.Fatalf("register the source: %d (%s)", rec.Code, rec.Body.String())
	}

	wantEnd := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	wantStart := wantEnd.Add(-2 * time.Hour)
	start, end := metricWindow(t, env, token, cluster.ID,
		"&range=15m&start="+wantStart.Format(time.RFC3339)+"&end="+wantEnd.Format(time.RFC3339))
	if !start.Equal(wantStart) || !end.Equal(wantEnd) {
		t.Fatalf("expected the explicit window %s→%s, got %s→%s", wantStart, wantEnd, start, end)
	}
}

/*
 * The comparison table. It is two queries rather than one, which makes it the
 * one place a scope could be enforced on the reading and lost on the reading it
 * is compared against — so what these check is that both queries are the same
 * question asked at two instants, and that the ranking is on the current window
 * only.
 */

// rankingServer answers the two instant queries a comparison makes, recording
// what it was asked. The `time` parameter is what separates them: the same
// expression evaluated an hour earlier is the previous window.
func rankingServer(t *testing.T, asked *[]url.Values) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/status/buildinfo" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"version":"v2.53.0"}}`))
			return
		}
		query := r.URL.Query()
		mu.Lock()
		*asked = append(*asked, query)
		latest := len(*asked)
		mu.Unlock()

		// The first query is the ranking; the second is the window before it.
		// `shop` grew, `search` shrank, and `fresh` did not exist an hour ago.
		if latest == 1 {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
				{"metric":{"namespace":"shop"},"value":[1700000000,"120"]},
				{"metric":{"namespace":"search"},"value":[1700000000,"50"]},
				{"metric":{"namespace":"fresh"},"value":[1700000000,"10"]}
			]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"namespace":"shop"},"value":[1699996400,"100"]},
			{"metric":{"namespace":"search"},"value":[1699996400,"80"]},
			{"metric":{"namespace":"archive"},"value":[1699996400,"7"]}
		]}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestTheComparisonRanksTheCurrentWindowAndLooksUpTheOneBefore(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	token := env.tokenFor(t, admin)

	var asked []url.Values
	backend := rankingServer(t, &asked)
	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/sources/metrics"
	if rec := env.do(t, http.MethodPut, path, token, directSourcePayload(backend.URL)); rec.Code != http.StatusOK {
		t.Fatalf("register the source: %d (%s)", rec.Code, rec.Body.String())
	}
	asked = nil // the registration probe is not one of the two queries under test

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+
			"/observability/metrics/compare?metric=cluster_cpu_by_namespace&range=1h&topk=3",
		token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	result := decode[struct {
		Result observability.CompareResult `json:"result"`
	}](t, rec).Result

	if len(asked) != 2 {
		t.Fatalf("expected exactly two queries, got %d", len(asked))
	}
	// The current window is ranked; the previous one is not, because a window
	// with a top-five of its own would report anything outside it as new.
	if !strings.HasPrefix(asked[0].Get("query"), "topk(3, ") {
		t.Errorf("the ranking query is not ranked: %s", asked[0].Get("query"))
	}
	if strings.Contains(asked[1].Get("query"), "topk(") {
		t.Errorf("the comparison query is ranked: %s", asked[1].Get("query"))
	}
	// Same expression, two instants, one window apart.
	if asked[1].Get("query") != result.CompareQuery {
		t.Errorf("the comparison query is not the one reported: %s", asked[1].Get("query"))
	}
	first, second := asked[0].Get("time"), asked[1].Get("time")
	if first == "" || second == "" {
		t.Fatalf("both queries have to name the instant they are asked at: %q %q", first, second)
	}
	if delta := atoiOrFail(t, first) - atoiOrFail(t, second); delta != 3600 {
		t.Errorf("the windows are %d seconds apart, want one hour", delta)
	}
	// The span reaches the query rather than the chart's fixed five minutes.
	if !strings.Contains(asked[0].Get("query"), "[3600s]") {
		t.Errorf("the ranking does not cover the window: %s", asked[0].Get("query"))
	}

	rows := map[string]observability.CompareRow{}
	for _, row := range result.Rows {
		rows[row.Name] = row
	}
	if len(result.Rows) != 3 || result.Rows[0].Name != "shop" {
		t.Fatalf("rows are not the current window's ranking: %+v", result.Rows)
	}
	if got := rows["shop"]; got.Delta == nil || *got.Delta != 20 {
		t.Errorf("shop delta = %v, want +20", got.Delta)
	}
	if got := rows["search"]; got.Delta == nil || *got.Delta != -30 {
		t.Errorf("search delta = %v, want -30", got.Delta)
	}
	if got := rows["fresh"]; got.Previous != nil {
		t.Errorf("a namespace absent an hour ago is new, not compared: %+v", got)
	}
	// Something that only existed in the previous window is not carried into a
	// table answering "what is worst now".
	if _, present := rows["archive"]; present {
		t.Error("the previous window's own entities leaked into the table")
	}
	if result.Rise != observability.TrendNeutral {
		t.Errorf("rise = %q, want a usage reading to be neutral", result.Rise)
	}
}

// A rise in restarts means something went wrong, and the catalogue is what knows
// that — the browser only decides whether to spend colour on the arrow.
func TestAFailureReadingReportsThatARiseIsWorse(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	token := env.tokenFor(t, admin)

	var asked []url.Values
	backend := rankingServer(t, &asked)
	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/sources/metrics"
	env.do(t, http.MethodPut, path, token, directSourcePayload(backend.URL))

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+
			"/observability/metrics/compare?metric=pod_restarts&range=6h",
		token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	result := decode[struct {
		Result observability.CompareResult `json:"result"`
	}](t, rec).Result

	if result.Rise != observability.TrendWorse {
		t.Fatalf("rise = %q, want restarts to be worse when they climb", result.Rise)
	}
	if result.TopK != 5 {
		t.Fatalf("topk = %d, want the default five", result.TopK)
	}
	if result.Unit != observability.UnitCount {
		t.Fatalf("unit = %q, want a tally", result.Unit)
	}
}

// The scope rides on the query here as everywhere else: a scoped caller is
// refused the cluster-wide entries outright and answered from their own grant on
// the ones that break down by namespace.
func TestTheComparisonHonoursTheCallersScope(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	scoped := env.store.addUser("dev", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	env.store.grant(scoped.ID, cluster.ID, db.K8sRoleView, []string{"shop"})

	var asked []url.Values
	backend := rankingServer(t, &asked)
	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/sources/metrics"
	env.do(t, http.MethodPut, path, env.tokenFor(t, admin), directSourcePayload(backend.URL))

	base := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/metrics/compare?range=1h&metric="
	token := env.tokenFor(t, scoped)

	if rec := env.do(t, http.MethodGet, base+"cluster_cpu", token, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected a cluster-wide comparison to be refused, got %d (%s)", rec.Code, rec.Body.String())
	}

	asked = nil
	if rec := env.do(t, http.MethodGet, base+"cluster_cpu_by_namespace", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("expected the per-namespace comparison to be allowed, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(asked) != 2 {
		t.Fatalf("expected two queries, got %d", len(asked))
	}
	// Both of them — the ranking and the window it is compared against.
	for i, query := range asked {
		if !strings.Contains(query.Get("query"), `namespace="shop"`) {
			t.Errorf("query %d reaches past the grant: %s", i, query.Get("query"))
		}
	}
}

func TestTheComparisonRefusesWhatItCannotRank(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	token := env.tokenFor(t, admin)

	var asked []url.Values
	backend := rankingServer(t, &asked)
	path := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/sources/metrics"
	env.do(t, http.MethodPut, path, token, directSourcePayload(backend.URL))

	base := "/api/v1/clusters/" + itoa(cluster.ID) + "/observability/metrics/compare?range=1h"
	for _, tc := range []struct{ query, why string }{
		{"&metric=not_a_metric", "an unknown catalogue entry"},
		{"&metric=cluster_cpu&topk=0", "a table of no rows"},
		{"&metric=cluster_cpu&topk=abc", "a topk that is not a number"},
	} {
		if rec := env.do(t, http.MethodGet, base+tc.query, token, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("expected %s to be refused, got %d (%s)", tc.why, rec.Code, rec.Body.String())
		}
	}

	// Asking for more rows than the table allows is capped rather than refused —
	// it is a legible request for something slightly too large.
	rec := env.do(t, http.MethodGet, base+"&metric=cluster_cpu_by_namespace&topk=500", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected an oversized topk to be capped, got %d (%s)", rec.Code, rec.Body.String())
	}
	result := decode[struct {
		Result observability.CompareResult `json:"result"`
	}](t, rec).Result
	if result.TopK != 20 {
		t.Fatalf("topk = %d, want it capped at 20", result.TopK)
	}
}

func atoiOrFail(t *testing.T, raw string) int {
	t.Helper()
	value, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("%q is not a number: %v", raw, err)
	}
	return value
}
