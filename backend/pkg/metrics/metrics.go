// Package metrics provides Prometheus instrumentation for the KubeMG server.
// It exposes three signal types: HTTP request metrics (count, latency,
// in-flight), database query metrics (latency, total per operation), and a
// build-info gauge so dashboards can correlate anomalies with deploys.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gorm.io/gorm"
)

// Metrics carries all Prometheus instruments for one server process.
type Metrics struct {
	httpRequestsTotal    *prometheus.CounterVec
	httpRequestDuration  *prometheus.HistogramVec
	httpRequestsInFlight prometheus.Gauge
	dbQueryDuration      *prometheus.HistogramVec
	dbQueriesTotal       *prometheus.CounterVec
	buildInfo            *prometheus.GaugeVec
	gatherer             prometheus.Gatherer
}

// New registers all KubeMG metrics against reg and returns the handle.
// Use Default() to instrument a production server against the standard registry.
func New(reg prometheus.Registerer, gath prometheus.Gatherer) *Metrics {
	m := &Metrics{gatherer: gath}

	m.httpRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kubemg_http_requests_total",
		Help: "Total HTTP requests by method, route pattern, and response status.",
	}, []string{"method", "path", "status"})

	m.httpRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kubemg_http_request_duration_seconds",
		Help:    "HTTP request latency by method and route pattern.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	}, []string{"method", "path"})

	m.httpRequestsInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kubemg_http_requests_in_flight",
		Help: "Number of HTTP requests currently being served.",
	})

	m.dbQueryDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kubemg_db_query_duration_seconds",
		Help:    "Database query latency by GORM operation type.",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
	}, []string{"operation"})

	m.dbQueriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kubemg_db_queries_total",
		Help: "Total database queries by operation type and error status.",
	}, []string{"operation", "error"})

	m.buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kubemg_build_info",
		Help: "Build metadata; value is always 1. Use label values for version info.",
	}, []string{"version"})

	reg.MustRegister(
		m.httpRequestsTotal,
		m.httpRequestDuration,
		m.httpRequestsInFlight,
		m.dbQueryDuration,
		m.dbQueriesTotal,
		m.buildInfo,
	)

	return m
}

// Default returns a Metrics instance registered against the standard Prometheus
// registry, which already carries Go runtime and process collectors.
func Default() *Metrics {
	return New(prometheus.DefaultRegisterer, prometheus.DefaultGatherer)
}

// NewStandalone creates a self-contained registry with Go and process collectors
// included. Useful in tests and environments where the default registry is not
// appropriate.
func NewStandalone() (*Metrics, *prometheus.Registry) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return New(reg, reg), reg
}

// RegisterBuildInfo records the binary's version into the build-info gauge.
// Call once at startup after the version string is known.
func (m *Metrics) RegisterBuildInfo(version string) {
	m.buildInfo.WithLabelValues(version).Set(1)
}

// Middleware returns a Gin handler that records per-request HTTP metrics.
// It uses c.FullPath() for the path label so wildcard route patterns
// (e.g. /api/v1/clusters/:id) do not cause unbounded label cardinality.
func (m *Metrics) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		m.httpRequestsInFlight.Inc()

		c.Next()

		m.httpRequestsInFlight.Dec()

		path := c.FullPath()
		if path == "" {
			// Requests that did not match any route (404s from the SPA fallback
			// or unknown API paths) are grouped rather than exploding cardinality.
			path = "unmatched"
		}

		m.httpRequestsTotal.
			WithLabelValues(c.Request.Method, path, strconv.Itoa(c.Writer.Status())).
			Inc()
		m.httpRequestDuration.
			WithLabelValues(c.Request.Method, path).
			Observe(time.Since(start).Seconds())
	}
}

// Handler returns the HTTP handler that serves the /metrics scrape endpoint.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.gatherer, promhttp.HandlerOpts{})
}

const startTimeKey = "metrics:start"

// RegisterGORMCallbacks attaches query timing callbacks to db.
// Call once after db.Open, before creating the Store.
func (m *Metrics) RegisterGORMCallbacks(db *gorm.DB) {
	for _, op := range []string{"create", "query", "update", "delete", "row", "raw"} {
		m.registerForOp(db, op)
	}
}

func (m *Metrics) registerForOp(db *gorm.DB, op string) {
	before := "metrics:before:" + op
	after := "metrics:after:" + op
	afterFn := m.dbAfter(op)

	switch op {
	case "create":
		db.Callback().Create().Before("gorm:create").Register(before, dbBefore)
		db.Callback().Create().After("gorm:create").Register(after, afterFn)
	case "query":
		db.Callback().Query().Before("gorm:query").Register(before, dbBefore)
		db.Callback().Query().After("gorm:query").Register(after, afterFn)
	case "update":
		db.Callback().Update().Before("gorm:update").Register(before, dbBefore)
		db.Callback().Update().After("gorm:update").Register(after, afterFn)
	case "delete":
		db.Callback().Delete().Before("gorm:delete").Register(before, dbBefore)
		db.Callback().Delete().After("gorm:delete").Register(after, afterFn)
	case "row":
		db.Callback().Row().Before("gorm:row").Register(before, dbBefore)
		db.Callback().Row().After("gorm:row").Register(after, afterFn)
	case "raw":
		db.Callback().Raw().Before("gorm:raw").Register(before, dbBefore)
		db.Callback().Raw().After("gorm:raw").Register(after, afterFn)
	}
}

func dbBefore(db *gorm.DB) {
	db.InstanceSet(startTimeKey, time.Now())
}

func (m *Metrics) dbAfter(op string) func(*gorm.DB) {
	return func(db *gorm.DB) {
		startVal, ok := db.InstanceGet(startTimeKey)
		if !ok {
			return
		}
		start, ok := startVal.(time.Time)
		if !ok {
			return
		}

		errLabel := "false"
		if db.Error != nil {
			errLabel = "true"
		}

		m.dbQueryDuration.WithLabelValues(op).Observe(time.Since(start).Seconds())
		m.dbQueriesTotal.WithLabelValues(op, errLabel).Inc()
	}
}
