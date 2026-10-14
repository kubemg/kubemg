package main

import (
	"net/http"
	"testing"
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
