package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func consolePath(clusterID uint, kind string) string {
	return "/api/v1/clusters/" + itoa(clusterID) + "/consoles/" + kind
}

func TestRegisteringAConsoleAndReadingItBack(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodPut, consolePath(cluster.ID, db.ConsoleGrafana), token,
		map[string]any{"url": "https://grafana.example.com/"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	rec = env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/consoles", token, nil)
	body := decode[struct {
		Consoles []consoleResponse `json:"consoles"`
		Editable bool              `json:"editable"`
	}](t, rec)
	if len(body.Consoles) != 1 {
		t.Fatalf("expected one console, got %+v", body.Consoles)
	}
	// Stored normalised, so everything appending to it can append.
	if body.Consoles[0].URL != "https://grafana.example.com" {
		t.Fatalf("url = %q", body.Consoles[0].URL)
	}
	if !body.Editable {
		t.Fatal("expected an admin to be told they may edit")
	}

	rec = env.do(t, http.MethodDelete, consolePath(cluster.ID, db.ConsoleGrafana), token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusNoContent, rec.Code, rec.Body.String())
	}
	rec = env.do(t, http.MethodDelete, consolePath(cluster.ID, db.ConsoleGrafana), token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected a second delete to 404, got %d", rec.Code)
	}
}

// A console is an address, not a credential — so it is readable by anyone the
// cluster is granted to, exactly like the datasource rows. Registering one is
// administrative.
func TestConsolesAreReadableByAGrantedUserAndWritableOnlyByAdmins(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	dev := env.store.addUser("dev", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	env.store.grant(dev.ID, cluster.ID, db.K8sRoleView, nil)

	env.do(t, http.MethodPut, consolePath(cluster.ID, db.ConsoleArgoCD), env.tokenFor(t, admin),
		map[string]any{"url": "https://argocd.example.com"})

	devToken := env.tokenFor(t, dev)
	rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID)+"/consoles", devToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	body := decode[struct {
		Consoles []consoleResponse `json:"consoles"`
		Editable bool              `json:"editable"`
	}](t, rec)
	if len(body.Consoles) != 1 || body.Consoles[0].Kind != db.ConsoleArgoCD {
		t.Fatalf("expected the argocd console, got %+v", body.Consoles)
	}
	if body.Editable {
		t.Fatal("expected a non-admin to be told they cannot edit")
	}

	for _, call := range []struct {
		method string
		body   any
	}{
		{http.MethodPut, map[string]any{"url": "https://elsewhere.example.com"}},
		{http.MethodDelete, nil},
	} {
		rec := env.do(t, call.method, consolePath(cluster.ID, db.ConsoleArgoCD), devToken, call.body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected %s to be refused, got %d", call.method, rec.Code)
		}
	}
}

// You cannot learn where a cluster's consoles are without the cluster.
func TestConsolesAreHiddenFromAUserWithoutAccess(t *testing.T) {
	env := newTestEnv(t)
	stranger := env.store.addUser("stranger", "pw", db.RoleUser)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)

	rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID)+"/consoles",
		env.tokenFor(t, stranger), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusForbidden, rec.Code, rec.Body.String())
	}
}

func TestConsoleAddressesAreValidatedOnTheWayIn(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	token := env.tokenFor(t, admin)

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"relative", map[string]any{"url": "/grafana"}},
		// A link with a password in it is a credential, and KubeMG stores none
		// for another console.
		{"userinfo", map[string]any{"url": "https://admin:pw@grafana.example.com"}},
		{"ref is a name not a path", map[string]any{
			"url": "https://argocd.example.com", "ref": "apps/default",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(t, http.MethodPut, consolePath(cluster.ID, db.ConsoleGrafana), token, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d (%s)",
					http.StatusBadRequest, rec.Code, rec.Body.String())
			}
		})
	}

	rec := env.do(t, http.MethodPut, consolePath(cluster.ID, "jenkins"), token,
		map[string]any{"url": "https://jenkins.example.com"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected an unknown console kind to be refused, got %d", rec.Code)
	}
}

// The datasource's own UI is derived from the address the cluster already
// declared rather than stored beside it: storing it would be storing the same
// fact twice and letting the two disagree the first time somebody moves a
// Prometheus.
func TestDatasourceUIsAreDerivedFromTheRegisteredSource(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	token := env.tokenFor(t, admin)
	backend := promServer(t)

	env.do(t, http.MethodPut,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/observability/sources/metrics", token,
		directSourcePayload(backend.URL))

	rec := env.do(t, http.MethodGet, "/api/v1/clusters/"+itoa(cluster.ID)+"/consoles", token, nil)
	body := decode[struct {
		DatasourceUIs []datasourceUIView `json:"datasource_uis"`
	}](t, rec)
	if len(body.DatasourceUIs) != 1 {
		t.Fatalf("expected one datasource UI, got %+v", body.DatasourceUIs)
	}
	if body.DatasourceUIs[0].URL != backend.URL+"/graph" {
		t.Fatalf("ui url = %q", body.DatasourceUIs[0].URL)
	}
}

// The Explore link carries the query KubeMG itself wrote, over the window it
// actually charted. It is built server-side for that reason: a browser
// assembling one would be a browser writing a query.
func TestAChartCarriesAGrafanaExploreLinkForTheQueryItRan(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	token := env.tokenFor(t, admin)
	backend := promServer(t)

	source := directSourcePayload(backend.URL)
	source["grafana_datasource"] = "prom-uid"
	env.do(t, http.MethodPut,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/observability/sources/metrics", token, source)
	env.do(t, http.MethodPut, consolePath(cluster.ID, db.ConsoleGrafana), token,
		map[string]any{"url": "https://grafana.example.com"})

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+
			"/observability/metrics/query?metric=cluster_cpu&range=1h", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}

	body := decode[struct {
		Result struct {
			Query string `json:"query"`
		} `json:"result"`
		GrafanaExplore string `json:"grafana_explore"`
	}](t, rec)
	if body.GrafanaExplore == "" {
		t.Fatal("expected an Explore link")
	}
	parsed, err := url.Parse(body.GrafanaExplore)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Host != "grafana.example.com" || parsed.Path != "/explore" {
		t.Fatalf("link = %q", body.GrafanaExplore)
	}
	// The expression in the link is the one the server ran, not one the caller
	// could have chosen.
	var panes map[string]struct {
		Queries []struct {
			Expr string `json:"expr"`
		} `json:"queries"`
	}
	if err := json.Unmarshal([]byte(parsed.Query().Get("panes")), &panes); err != nil {
		t.Fatalf("panes: %v", err)
	}
	if panes["kubemg"].Queries[0].Expr != body.Result.Query {
		t.Fatalf("link query %q does not match the query run %q",
			panes["kubemg"].Queries[0].Expr, body.Result.Query)
	}
}

// No Grafana, or a datasource with no uid in it, means no link — rather than a
// link that opens Explore on somebody else's default datasource, where a PromQL
// expression is an error message instead of a chart.
func TestNoGrafanaMeansNoExploreLink(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "pw", db.RoleAdmin)
	cluster := env.store.addCluster("prod-eu", db.EnvProd)
	token := env.tokenFor(t, admin)
	backend := promServer(t)

	env.do(t, http.MethodPut,
		"/api/v1/clusters/"+itoa(cluster.ID)+"/observability/sources/metrics", token,
		directSourcePayload(backend.URL))
	// A Grafana with no datasource uid registered against the source.
	env.do(t, http.MethodPut, consolePath(cluster.ID, db.ConsoleGrafana), token,
		map[string]any{"url": "https://grafana.example.com"})

	rec := env.do(t, http.MethodGet,
		"/api/v1/clusters/"+itoa(cluster.ID)+
			"/observability/metrics/query?metric=cluster_cpu&range=1h", token, nil)
	if strings.Contains(rec.Body.String(), "grafana.example.com") {
		t.Fatalf("expected no Explore link without a datasource uid: %s", rec.Body.String())
	}
}
