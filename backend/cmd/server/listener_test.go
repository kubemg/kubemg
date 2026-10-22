package main

import (
	"net/http"
	"testing"
	"time"
)

// The header deadline is the one that must be set, and the whole-request ones
// are the ones that must not: either would cut every exec session, `logs -f`
// and agent tunnel off at the same mark.
func TestNewHTTPServerBoundsHeadersOnly(t *testing.T) {
	srv := newHTTPServer(":0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 {
		t.Fatalf("ReadHeaderTimeout = %v, want a bound", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
		t.Fatalf("ReadTimeout = %v, WriteTimeout = %v; both must stay unset for long-lived streams",
			srv.ReadTimeout, srv.WriteTimeout)
	}
}

// The metrics listener has no stream to protect, so it bounds the whole
// request as well as the headers — with room left for a scrape to finish
// inside Prometheus' own default timeout.
func TestNewMetricsServerBoundsTheWholeRequest(t *testing.T) {
	srv := newMetricsServer(":0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 {
		t.Fatalf("ReadHeaderTimeout = %v, want a bound", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("ReadTimeout = %v, WriteTimeout = %v, IdleTimeout = %v; want all three bounded",
			srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
	if srv.WriteTimeout <= 10*time.Second {
		t.Fatalf("WriteTimeout = %v; want it above Prometheus' default 10s scrape timeout", srv.WriteTimeout)
	}
}

// Giving the metrics listener its deadlines must not hand them to the API's:
// the two are built from one base.
func TestNewMetricsServerLeavesTheAPIServerUnbounded(t *testing.T) {
	_ = newMetricsServer(":0", http.NotFoundHandler())
	api := newHTTPServer(":0", http.NotFoundHandler())
	if api.ReadTimeout != 0 || api.WriteTimeout != 0 || api.IdleTimeout != 0 {
		t.Fatalf("API server ReadTimeout = %v, WriteTimeout = %v, IdleTimeout = %v; want all unset",
			api.ReadTimeout, api.WriteTimeout, api.IdleTimeout)
	}
}

func TestSameListener(t *testing.T) {
	cases := []struct {
		listen, metrics string
		want            bool
	}{
		// The collisions the issue names.
		{":8443", ":8443", true},
		{":8443", "127.0.0.1:8443", true},
		{"0.0.0.0:8443", ":8443", true},
		// The documented, working shape.
		{":8443", "127.0.0.1:9090", false},

		// Either side's wildcard covers the other's specific address.
		{"127.0.0.1:8443", ":8443", true},
		{"[::]:8443", "10.0.0.5:8443", true},
		{"10.0.0.5:8443", "0.0.0.0:8443", true},
		// One specific address, spelt differently.
		{"127.0.0.1:8443", "127.0.0.1:8443", true},
		{"[::1]:8443", "[0:0:0:0:0:0:0:1]:8443", true},
		{"localhost:9090", "127.0.0.1:9090", true},
		{"127.0.0.1:9090", "LOCALHOST:9090", true},
		{"kubemg.internal:8443", "KUBEMG.internal:8443", true},

		// Two specific addresses on one port are two sockets.
		{"127.0.0.1:8443", "10.0.0.5:8443", false},
		{"127.0.0.1:8443", "[::1]:8443", false},
		{"localhost:8443", "10.0.0.5:8443", false},
		// Different hostnames are not resolved.
		{"a.internal:8443", "b.internal:8443", false},

		// Metrics off, or a port the kernel picks each time.
		{":8443", "", false},
		{":0", ":0", false},
		// Not a host:port at all: the bind reports that, not this check.
		{":8443", "8443", false},
	}
	for _, tc := range cases {
		if got := sameListener(tc.listen, tc.metrics); got != tc.want {
			t.Errorf("sameListener(%q, %q) = %v, want %v", tc.listen, tc.metrics, got, tc.want)
		}
	}
}

// The warning is isLoopbackAddr's answer turned around, so anything that
// reading calls loopback-only stays quiet and everything else is warned about.
func TestMetricsReachableOffHost(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:9090": false,
		"localhost:9090": false,
		"[::1]:9090":     false,
		":9090":          true, // every interface — what the Helm chart binds
		"0.0.0.0:9090":   true,
		"[::]:9090":      true,
		"10.0.0.5:9090":  true,
	}
	for addr, want := range cases {
		if got := metricsReachableOffHost(addr); got != want {
			t.Errorf("metricsReachableOffHost(%q) = %v, want %v", addr, got, want)
		}
	}
}
