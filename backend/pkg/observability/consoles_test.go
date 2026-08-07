package observability

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func TestNormalizeConsoleURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain host", "https://grafana.example.com", "https://grafana.example.com"},
		{"trailing slash trimmed", "https://grafana.example.com/", "https://grafana.example.com"},
		{"sub path kept", "https://ops.example.com/grafana/", "https://ops.example.com/grafana"},
		{"whitespace trimmed", "  http://argocd.internal:8080  ", "http://argocd.internal:8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeConsoleURL(tc.in)
			if err != nil {
				t.Fatalf("NormalizeConsoleURL(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeConsoleURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeConsoleURLRefuses(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", "   "},
		{"relative", "/grafana"},
		{"no host", "https://"},
		{"not http", "ftp://grafana.example.com"},
		// A URL with a password in it is a credential, and this stores none.
		{"userinfo", "https://admin:hunter2@grafana.example.com"},
		{"query", "https://grafana.example.com/?orgId=2"},
		{"fragment", "https://grafana.example.com/#/dashboards"},
		{"too long", "https://grafana.example.com/" + strings.Repeat("a", maxConsoleURL)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NormalizeConsoleURL(tc.in); err == nil {
				t.Fatalf("NormalizeConsoleURL(%q) was accepted", tc.in)
			}
		})
	}
}

func TestNormalizeConsoleRef(t *testing.T) {
	got, err := NormalizeConsoleRef("  prometheus-uid  ")
	if err != nil {
		t.Fatalf("NormalizeConsoleRef: %v", err)
	}
	if got != "prometheus-uid" {
		t.Fatalf("ref = %q, want %q", got, "prometheus-uid")
	}
	if _, err := NormalizeConsoleRef("apps/default"); err == nil {
		t.Fatal("a path was accepted as an identifier")
	}
	if _, err := NormalizeConsoleRef(strings.Repeat("a", maxConsoleRef+1)); err == nil {
		t.Fatal("an over-long identifier was accepted")
	}
}

func TestGrafanaExploreCarriesTheQuery(t *testing.T) {
	from := time.Unix(1700000000, 0).UTC()
	to := from.Add(time.Hour)
	expr := `sum(rate(container_cpu_usage_seconds_total{namespace="team-a"}[5m]))`

	link := GrafanaExplore("https://grafana.example.com/", "vm-uid", db.ProviderVictoriaMetrics,
		expr, from, to)
	if link == "" {
		t.Fatal("no link built")
	}

	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Path != "/explore" {
		t.Fatalf("path = %q, want /explore", parsed.Path)
	}
	if parsed.Query().Get("schemaVersion") != "1" {
		t.Fatalf("schemaVersion = %q", parsed.Query().Get("schemaVersion"))
	}
	// orgId is deliberately absent: the link opens in the org the viewer is in.
	if parsed.Query().Has("orgId") {
		t.Fatal("orgId was pinned")
	}

	var panes map[string]struct {
		Datasource string `json:"datasource"`
		Queries    []struct {
			RefID      string `json:"refId"`
			Expr       string `json:"expr"`
			Datasource struct {
				UID  string `json:"uid"`
				Type string `json:"type"`
			} `json:"datasource"`
		} `json:"queries"`
		Range struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"range"`
	}
	if err := json.Unmarshal([]byte(parsed.Query().Get("panes")), &panes); err != nil {
		t.Fatalf("panes: %v", err)
	}
	pane, ok := panes["kubemg"]
	if !ok {
		t.Fatalf("no kubemg pane in %v", panes)
	}
	if pane.Datasource != "vm-uid" {
		t.Fatalf("datasource = %q", pane.Datasource)
	}
	if len(pane.Queries) != 1 || pane.Queries[0].Expr != expr {
		t.Fatalf("queries = %+v", pane.Queries)
	}
	if pane.Queries[0].Datasource.Type != "prometheus" {
		t.Fatalf("query datasource type = %q", pane.Queries[0].Datasource.Type)
	}
	if pane.Range.From != "1700000000000" || pane.Range.To != "1700003600000" {
		t.Fatalf("range = %+v", pane.Range)
	}
}

// A link with no datasource would open Explore on whatever that Grafana
// defaults to, which for a PromQL expression is an error message rather than a
// chart. No link is the better answer.
func TestGrafanaExploreNeedsEveryPart(t *testing.T) {
	now := time.Now()
	cases := []struct{ name, base, uid, expr string }{
		{"no base", "", "uid", "up"},
		{"no datasource", "https://grafana.example.com", "", "up"},
		{"no query", "https://grafana.example.com", "uid", "  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if link := GrafanaExplore(tc.base, tc.uid, db.ProviderPrometheus,
				tc.expr, now, now); link != "" {
				t.Fatalf("built %q", link)
			}
		})
	}
}

func TestGrafanaExploreDatasourceType(t *testing.T) {
	for provider, want := range map[string]string{
		db.ProviderLoki:            "loki",
		db.ProviderVictoriaLogs:    "victorialogs-datasource",
		db.ProviderPrometheus:      "prometheus",
		db.ProviderThanos:          "prometheus",
		db.ProviderMimir:           "prometheus",
		db.ProviderVictoriaMetrics: "prometheus",
	} {
		if got := grafanaDatasourceType(provider); got != want {
			t.Fatalf("grafanaDatasourceType(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestArgoApplication(t *testing.T) {
	got := ArgoApplication("https://argocd.example.com/", "payments-api")
	if got != "https://argocd.example.com/applications/payments-api" {
		t.Fatalf("link = %q", got)
	}
	// The name comes off a label Argo wrote, so it is escaped into its segment
	// rather than trusted to be one.
	if got := ArgoApplication("https://argocd.example.com", "a/b"); got !=
		"https://argocd.example.com/applications/a%2Fb" {
		t.Fatalf("escaped link = %q", got)
	}
	if got := ArgoApplication("https://argocd.example.com", ""); got != "" {
		t.Fatalf("nameless link = %q", got)
	}
}

func TestDatasourceUI(t *testing.T) {
	cases := []struct {
		name   string
		source db.ObservabilitySource
		want   string
	}{
		{
			name: "prometheus graph",
			source: db.ObservabilitySource{
				Provider: db.ProviderPrometheus, AccessMode: db.AccessDirect,
				URL: "https://prom.example.com",
			},
			want: "https://prom.example.com/graph",
		},
		{
			name: "single-node victoriametrics",
			source: db.ObservabilitySource{
				Provider: db.ProviderVictoriaMetrics, AccessMode: db.AccessDirect,
				URL: "https://vm.example.com/",
			},
			want: "https://vm.example.com/vmui",
		},
		{
			// vmselect serves its API under /select/0/prometheus and its UI under
			// /select/0/vmui — the tenant prefix with the last segment swapped.
			name: "vmselect tenant prefix",
			source: db.ObservabilitySource{
				Provider: db.ProviderVictoriaMetrics, AccessMode: db.AccessDirect,
				URL: "https://vmselect.example.com:8481", PathPrefix: "/select/0/prometheus",
			},
			want: "https://vmselect.example.com:8481/select/0/vmui",
		},
		{
			name: "victorialogs",
			source: db.ObservabilitySource{
				Provider: db.ProviderVictoriaLogs, AccessMode: db.AccessDirect,
				URL: "https://vlogs.example.com",
			},
			want: "https://vlogs.example.com/select/vmui",
		},
		{
			// Reached by asking the API server to proxy to a Service — a call
			// KubeMG makes down the tunnel, not an address a browser can open.
			name: "in-cluster has no browser address",
			source: db.ObservabilitySource{
				Provider: db.ProviderPrometheus, AccessMode: db.AccessInCluster,
				ServiceNamespace: "monitoring", ServiceName: "prometheus", ServicePort: "9090",
			},
			want: "",
		},
		{
			name: "loki is queried through grafana",
			source: db.ObservabilitySource{
				Provider: db.ProviderLoki, AccessMode: db.AccessDirect,
				URL: "https://loki.example.com",
			},
			want: "",
		},
		{
			name: "mimir has no ui",
			source: db.ObservabilitySource{
				Provider: db.ProviderMimir, AccessMode: db.AccessDirect,
				URL: "https://mimir.example.com",
			},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DatasourceUI(tc.source); got != tc.want {
				t.Fatalf("DatasourceUI = %q, want %q", got, tc.want)
			}
		})
	}
}
