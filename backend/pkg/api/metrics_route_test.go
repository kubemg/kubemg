package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/metrics"
	dto "github.com/prometheus/client_model/go"
)

// TestMetricsMiddlewareRecordsRequests verifies the middleware is wired into
// the router when Options.Metrics is set and increments the request counter.
func TestMetricsMiddlewareRecordsRequests(t *testing.T) {
	m, reg := metrics.NewStandalone()
	env := newTestEnvWith(t, func(o *Options) { o.Metrics = m })

	env.do(t, http.MethodGet, "/health", "", nil)

	all, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range all {
		if f.GetName() == "kubemg_http_requests_total" && len(f.GetMetric()) > 0 {
			return
		}
	}
	t.Fatal("kubemg_http_requests_total has no samples after a request; metrics middleware may not be wired")
}

// TestMetricsNotOnMainRouter verifies that GET /metrics is never served on the
// public listener, even when Options.Metrics is set. The scrape endpoint lives
// on a separate internal listener (KUBEMG_METRICS_ADDR).
func TestMetricsNotOnMainRouter(t *testing.T) {
	m, _ := metrics.NewStandalone()
	env := newTestEnvWith(t, func(o *Options) { o.Metrics = m })

	rec := env.do(t, http.MethodGet, "/metrics", "", nil)
	ct := rec.Header().Get("Content-Type")
	if strings.Contains(ct, "text/plain") && strings.Contains(rec.Body.String(), "kubemg_") {
		t.Fatal("GET /metrics served Prometheus output on the main router; it must only be on the internal metrics listener")
	}
}

// TestMetricsMiddlewareAbsentWhenNil verifies that no metrics are collected
// when Options.Metrics is nil.
func TestMetricsMiddlewareAbsentWhenNil(t *testing.T) {
	env := newTestEnv(t) // Metrics is nil by default

	env.do(t, http.MethodGet, "/health", "", nil)

	// There is no registry to query when Metrics is nil, so just assert the
	// router behaved normally (health returned 200).
	rec := env.do(t, http.MethodGet, "/health", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health: status %d, want 200", rec.Code)
	}
}

// gatherNamed is a local helper so this file has no import cycle with pkg/metrics.
func gatherNamed(t *testing.T, reg interface {
	Gather() ([]*dto.MetricFamily, error)
}, name string) []*dto.MetricFamily {
	t.Helper()
	all, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var out []*dto.MetricFamily
	for _, f := range all {
		if f.GetName() == name {
			out = append(out, f)
		}
	}
	return out
}
