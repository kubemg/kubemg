package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/metrics"
)

func TestMetricsRouteIsReachable(t *testing.T) {
	m, _ := metrics.NewStandalone()
	env := newTestEnvWith(t, func(o *Options) { o.Metrics = m })

	rec := env.do(t, http.MethodGet, "/metrics", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics: status %d, want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Fatalf("GET /metrics: Content-Type %q, want text/plain", ct)
	}
}

func TestMetricsRouteRequiresNoAuth(t *testing.T) {
	m, _ := metrics.NewStandalone()
	env := newTestEnvWith(t, func(o *Options) { o.Metrics = m })

	rec := env.do(t, http.MethodGet, "/metrics", "", nil)
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Fatalf("GET /metrics: status %d; the scrape endpoint must not require auth", rec.Code)
	}
}

func TestMetricsRouteNotShadowedBySPAFallback(t *testing.T) {
	m, _ := metrics.NewStandalone()
	env := newTestEnvWith(t, func(o *Options) { o.Metrics = m })

	rec := env.do(t, http.MethodGet, "/metrics", "", nil)
	ct := rec.Header().Get("Content-Type")
	if strings.Contains(ct, "text/html") {
		t.Fatalf("GET /metrics: Content-Type %q looks like the SPA fallback, not the metrics handler", ct)
	}
}

func TestMetricsRouteAbsentWhenNil(t *testing.T) {
	// Metrics is nil by default in the test env; the route must not be registered.
	env := newTestEnv(t)

	rec := env.do(t, http.MethodGet, "/metrics", "", nil)
	if rec.Code == http.StatusOK && strings.Contains(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatal("GET /metrics returned Prometheus output but Options.Metrics is nil")
	}
}
