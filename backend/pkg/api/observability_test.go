package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
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
