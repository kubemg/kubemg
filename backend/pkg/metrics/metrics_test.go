package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"gorm.io/gorm"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestMiddlewarePassesRequestsThrough(t *testing.T) {
	m, _ := NewStandalone()
	r := gin.New()
	r.Use(m.Middleware())
	r.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
}

func TestMiddlewareRecordsRequestCount(t *testing.T) {
	m, reg := NewStandalone()
	r := gin.New()
	r.Use(m.Middleware())
	r.GET("/clusters/:id", func(c *gin.Context) { c.Status(http.StatusOK) })

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/clusters/42", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/clusters/99", nil))

	families := gatherMetric(t, reg, "kubemg_http_requests_total")
	total := sumCounterFamily(families)
	if total != 2 {
		t.Fatalf("kubemg_http_requests_total = %v; want 2", total)
	}
}

func TestMiddlewareUsesRoutePatternNotActualPath(t *testing.T) {
	m, reg := NewStandalone()
	r := gin.New()
	r.Use(m.Middleware())
	r.GET("/clusters/:id", func(c *gin.Context) { c.Status(http.StatusOK) })

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/clusters/123", nil))

	families := gatherMetric(t, reg, "kubemg_http_requests_total")
	for _, f := range families {
		for _, metric := range f.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "path" && label.GetValue() == "/clusters/123" {
					t.Fatal("path label should be the route pattern /clusters/:id, not the actual path")
				}
			}
		}
	}
}

func TestMiddlewareLabelsUnmatchedRoutes(t *testing.T) {
	m, reg := NewStandalone()
	r := gin.New()
	r.Use(m.Middleware())

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/does-not-exist", nil))

	families := gatherMetric(t, reg, "kubemg_http_requests_total")
	found := false
	for _, f := range families {
		for _, metric := range f.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "path" && label.GetValue() == "unmatched" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("expected unmatched path label for a 404 request")
	}
}

func TestHandlerServesPrometheusFormat(t *testing.T) {
	m, _ := NewStandalone()
	m.RegisterBuildInfo("v0.9.0-test")

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("handler status = %d; want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "kubemg_build_info") {
		t.Fatal("expected kubemg_build_info in metrics output")
	}
	if !strings.Contains(string(body), `version="v0.9.0-test"`) {
		t.Fatal("expected version label in kubemg_build_info")
	}
}

func TestRegisterBuildInfoSetsGaugeToOne(t *testing.T) {
	m, reg := NewStandalone()
	m.RegisterBuildInfo("v1.2.3")

	families := gatherMetric(t, reg, "kubemg_build_info")
	for _, f := range families {
		for _, metric := range f.GetMetric() {
			if metric.GetGauge().GetValue() != 1 {
				t.Fatalf("kubemg_build_info = %v; want 1", metric.GetGauge().GetValue())
			}
		}
	}
}

func TestMiddlewarePanicDoesNotLeakInFlightGauge(t *testing.T) {
	m, reg := NewStandalone()
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(m.Middleware())
	r.GET("/boom", func(c *gin.Context) { panic("boom") })

	for range 3 {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	}

	families := gatherMetric(t, reg, "kubemg_http_requests_in_flight")
	for _, f := range families {
		for _, metric := range f.GetMetric() {
			if v := metric.GetGauge().GetValue(); v != 0 {
				t.Fatalf("kubemg_http_requests_in_flight = %v after panics; want 0", v)
			}
		}
	}
}

func TestMiddlewarePanicStillCountsRequest(t *testing.T) {
	m, reg := NewStandalone()
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(m.Middleware())
	r.GET("/boom", func(c *gin.Context) { panic("boom") })

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))

	families := gatherMetric(t, reg, "kubemg_http_requests_total")
	if sumCounterFamily(families) == 0 {
		t.Fatal("kubemg_http_requests_total should be non-zero after a panicking handler")
	}
}

func TestMiddlewareSkipsInFlightForWebSocketUpgrade(t *testing.T) {
	m, reg := NewStandalone()
	r := gin.New()
	r.Use(m.Middleware())
	// Simulate a WebSocket upgrade: handler blocks until the "connection" closes.
	r.GET("/ws", func(c *gin.Context) { c.Status(http.StatusSwitchingProtocols) })

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	r.ServeHTTP(httptest.NewRecorder(), req)

	families := gatherMetric(t, reg, "kubemg_http_requests_in_flight")
	for _, f := range families {
		for _, metric := range f.GetMetric() {
			if v := metric.GetGauge().GetValue(); v != 0 {
				t.Fatalf("in-flight gauge = %v after WebSocket upgrade; want 0", v)
			}
		}
	}
}

func TestDBAfterDoesNotCountRecordNotFoundAsError(t *testing.T) {
	m, reg := NewStandalone()
	afterFn := m.dbAfter("query")

	// Build a minimal gorm.DB with a Statement so InstanceGet works, a start
	// time so the callback does not return early, and ErrRecordNotFound as the
	// error — which is normal flow and must not increment error="true".
	stmt := &gorm.Statement{}
	stmt.Settings.Store(startTimeKey, time.Now())
	db := &gorm.DB{
		Error:     gorm.ErrRecordNotFound,
		Statement: stmt,
	}
	afterFn(db)

	families := gatherMetric(t, reg, "kubemg_db_queries_total")
	for _, f := range families {
		for _, metric := range f.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "error" && label.GetValue() == "true" {
					t.Fatal("ErrRecordNotFound must not be counted as error=true")
				}
			}
		}
	}
}

// gatherMetric collects all metric families from reg and returns those named n.
func gatherMetric(t *testing.T, reg *prometheus.Registry, name string) []*dto.MetricFamily {
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

// sumCounterFamily returns the total value across all counters in families.
func sumCounterFamily(families []*dto.MetricFamily) float64 {
	var sum float64
	for _, f := range families {
		for _, m := range f.GetMetric() {
			sum += m.GetCounter().GetValue()
		}
	}
	return sum
}
