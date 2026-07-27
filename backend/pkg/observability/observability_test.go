package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func metricsTarget() Target {
	return Target{
		Kind:       db.SourceMetrics,
		Provider:   db.ProviderVictoriaMetrics,
		AccessMode: db.AccessInCluster,

		ServiceNamespace: "monitoring",
		ServiceName:      "vmselect-vm",
		ServicePort:      "8481",
		PathPrefix:       "/select/0/prometheus",
		AuthMode:         db.AuthNone,
	}
}

// The Service proxy path is what makes an in-cluster datasource work without
// exposing anything, so it is worth pinning exactly.
func TestInClusterRequestPathIsTheServiceProxySubresource(t *testing.T) {
	got := metricsTarget().requestPath("/api/v1/query?query=1")
	want := "/api/v1/namespaces/monitoring/services/http:vmselect-vm:8481/proxy" +
		"/select/0/prometheus/api/v1/query?query=1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestDirectRequestPathJoinsPrefixAndTrimsSlashes(t *testing.T) {
	target := Target{
		Kind:       db.SourceMetrics,
		Provider:   db.ProviderPrometheus,
		AccessMode: db.AccessDirect,
		URL:        "https://metrics.example.com/",
		PathPrefix: "prometheus/",
		AuthMode:   db.AuthNone,
	}
	got := target.requestPath("/api/v1/query?query=1")
	want := "https://metrics.example.com/prometheus/api/v1/query?query=1"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestValidateRejectsTheWrongShapeForEachMode(t *testing.T) {
	cases := map[string]Target{
		"direct without an address": {
			Kind: db.SourceMetrics, Provider: db.ProviderPrometheus,
			AccessMode: db.AccessDirect, AuthMode: db.AuthNone,
		},
		"direct with a bare host": {
			Kind: db.SourceMetrics, Provider: db.ProviderPrometheus,
			AccessMode: db.AccessDirect, URL: "metrics.example.com", AuthMode: db.AuthNone,
		},
		"in-cluster without a service": {
			Kind: db.SourceMetrics, Provider: db.ProviderPrometheus,
			AccessMode: db.AccessInCluster, AuthMode: db.AuthNone,
		},
		"in-cluster without a port": {
			Kind: db.SourceMetrics, Provider: db.ProviderPrometheus,
			AccessMode:       db.AccessInCluster,
			ServiceNamespace: "monitoring", ServiceName: "prometheus", AuthMode: db.AuthNone,
		},
		"a logs provider serving metrics": {
			Kind: db.SourceMetrics, Provider: db.ProviderLoki,
			AccessMode: db.AccessDirect, URL: "https://loki.example.com", AuthMode: db.AuthNone,
		},
		"basic auth without a username": {
			Kind: db.SourceMetrics, Provider: db.ProviderPrometheus,
			AccessMode: db.AccessDirect, URL: "https://metrics.example.com",
			AuthMode: db.AuthBasic,
		},
	}

	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			if err := target.Validate(); err == nil {
				t.Fatal("expected the target to be rejected")
			}
		})
	}

	if err := metricsTarget().Validate(); err != nil {
		t.Fatalf("expected a well-formed target to pass, got %v", err)
	}
}

// A probe has to be a real read: something listening on the port is not the
// same thing as the backend an operator thinks they configured.
func TestProbeReadsTheProviderAPIAndReportsItsVersion(t *testing.T) {
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/v1/query":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
		case "/api/v1/status/buildinfo":
			_, _ = w.Write([]byte(`{"status":"success","data":{"version":"v1.102.0"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	target := Target{
		Kind: db.SourceMetrics, Provider: db.ProviderVictoriaMetrics,
		AccessMode: db.AccessDirect, URL: server.URL, AuthMode: db.AuthNone,
	}

	result := Probe(context.Background(), target, nil)
	if !result.Reachable {
		t.Fatalf("expected the datasource to be reachable, got %q", result.Message)
	}
	if result.Version != "v1.102.0" {
		t.Fatalf("expected the reported version, got %q", result.Version)
	}
	if len(asked) == 0 || !strings.HasPrefix(asked[0], "/api/v1/query") {
		t.Fatalf("expected the query API to be read first, got %v", asked)
	}
}

func TestProbeSendsTheCredential(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"status":"success"}`))
	}))
	defer server.Close()

	target := Target{
		Kind: db.SourceMetrics, Provider: db.ProviderPrometheus,
		AccessMode: db.AccessDirect, URL: server.URL,
		AuthMode: db.AuthBearer, Credential: "s3cret",
	}
	if result := Probe(context.Background(), target, nil); !result.Reachable {
		t.Fatalf("expected the datasource to be reachable, got %q", result.Message)
	}
	if authorization != "Bearer s3cret" {
		t.Fatalf("expected the bearer token to be sent, got %q", authorization)
	}
}

// A 404 from a real server is the most common misconfiguration there is — a
// Prometheus behind a path prefix, or a Service that is not what it looked like.
// It must not read as "unreachable".
func TestProbeExplainsA404RatherThanReportingItAsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	target := Target{
		Kind: db.SourceMetrics, Provider: db.ProviderPrometheus,
		AccessMode: db.AccessDirect, URL: server.URL, AuthMode: db.AuthNone,
	}
	result := Probe(context.Background(), target, nil)
	if result.Reachable {
		t.Fatal("expected a 404 not to count as reachable")
	}
	if !strings.Contains(result.Message, "path prefix") {
		t.Fatalf("expected the message to name the likely cause, got %q", result.Message)
	}
}

func TestProbeRefusesAnInClusterTargetWithNoTunnel(t *testing.T) {
	result := Probe(context.Background(), metricsTarget(), nil)
	if result.Reachable {
		t.Fatal("expected an in-cluster probe with no tunnel to fail")
	}
	if !strings.Contains(result.Message, "agent") {
		t.Fatalf("expected the message to name the missing tunnel, got %q", result.Message)
	}
}

func TestProbeUsesTheTunnelForAnInClusterTarget(t *testing.T) {
	var path string
	tunnel := func(_ context.Context, _, requested string, _ []byte) (int, []byte, error) {
		path = requested
		return http.StatusOK, []byte(`{"status":"success"}`), nil
	}

	result := Probe(context.Background(), metricsTarget(), tunnel)
	if !result.Reachable {
		t.Fatalf("expected the tunnelled probe to succeed, got %q", result.Message)
	}
	if !strings.Contains(path, "/services/http:vmselect-vm:8481/proxy") {
		t.Fatalf("expected the Service proxy path, got %q", path)
	}
}

func TestDiscoverRanksTheConventionalPortHighest(t *testing.T) {
	candidates := Discover([]ServiceRef{
		{Namespace: "monitoring", Name: "prometheus-node-exporter", Ports: []ServicePort{{Port: 9100}}},
		{Namespace: "monitoring", Name: "kube-prometheus-stack-prometheus", Ports: []ServicePort{{Port: 9090}}},
		{Namespace: "vm", Name: "vmselect-cluster", Ports: []ServicePort{{Port: 8481}}},
		{Namespace: "vm", Name: "vmagent-cluster", Ports: []ServicePort{{Port: 8429}}},
		{Namespace: "logs", Name: "loki-gateway", Ports: []ServicePort{{Port: 3100}}},
		{Namespace: "default", Name: "postgres", Ports: []ServicePort{{Port: 5432}}},
	})

	if len(candidates) != 3 {
		t.Fatalf("expected three candidates, got %d: %+v", len(candidates), candidates)
	}
	for _, candidate := range candidates {
		if candidate.Score != 2 {
			t.Fatalf("expected every conventional-port match to score highest, got %+v", candidate)
		}
	}

	found := map[string]Candidate{}
	for _, candidate := range candidates {
		found[candidate.Provider] = candidate
	}
	if _, ok := found[db.ProviderPrometheus]; !ok {
		t.Fatal("expected the kube-prometheus-stack Service to be offered")
	}
	// vmselect serves the Prometheus API per tenant; offering it without the
	// prefix would hand the operator an address that 404s.
	vm, ok := found[db.ProviderVictoriaMetrics]
	if !ok {
		t.Fatal("expected vmselect to be offered")
	}
	if vm.PathPrefix != "/select/0/prometheus" {
		t.Fatalf("expected the tenant prefix on vmselect, got %q", vm.PathPrefix)
	}
	if loki, ok := found[db.ProviderLoki]; !ok || loki.Kind != db.SourceLogs {
		t.Fatalf("expected Loki to be offered as a logs source, got %+v", loki)
	}
}

// A node-exporter answers on /metrics and would look alive while returning
// nothing anyone asked for, which is worse than offering nothing at all.
func TestDiscoverSkipsScrapeTargetsAndWriteEndpoints(t *testing.T) {
	candidates := Discover([]ServiceRef{
		{Namespace: "monitoring", Name: "prometheus-node-exporter", Ports: []ServicePort{{Port: 9100}}},
		{Namespace: "monitoring", Name: "prometheus-operator", Ports: []ServicePort{{Port: 8080}}},
		{Namespace: "vm", Name: "vminsert-cluster", Ports: []ServicePort{{Port: 8480}}},
		{Namespace: "logs", Name: "promtail", Ports: []ServicePort{{Port: 3101}}},
	})
	if len(candidates) != 0 {
		t.Fatalf("expected no candidates, got %+v", candidates)
	}
}
