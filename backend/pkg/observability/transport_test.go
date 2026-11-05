package observability

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// tlsDatasource serves a TLS endpoint and counts the connections dialled to it.
func tlsDatasource(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var dialled atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			dialled.Add(1)
		}
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, &dialled
}

func directTarget(url string, insecure bool) Target {
	return Target{
		Kind:               db.SourceMetrics,
		Provider:           db.ProviderPrometheus,
		AccessMode:         db.AccessDirect,
		URL:                url,
		AuthMode:           db.AuthNone,
		InsecureSkipVerify: insecure,
	}
}

func TestDirectQueriesShareOneConnection(t *testing.T) {
	server, dialled := tlsDatasource(t)
	target := directTarget(server.URL, true)

	for range 3 {
		status, _, err := callLimited(context.Background(), target, "/api/v1/query?query=up",
			nil, maxQueryBody, queryTimeout)
		if err != nil || status != http.StatusOK {
			t.Fatalf("query: status %d, err %v", status, err)
		}
	}
	if got := dialled.Load(); got != 1 {
		t.Fatalf("three queries dialled %d connections, want 1", got)
	}
}

func TestDirectTransportFollowsTheTLSShape(t *testing.T) {
	insecure := directTransport(directTarget("https://a.example", true))
	if insecure != directTransport(directTarget("https://b.example", true)) {
		t.Fatal("two sources with the same TLS shape got different transports")
	}
	if insecure == directTransport(directTarget("https://a.example", false)) {
		t.Fatal("switching verification on kept the transport that skips it")
	}

	// A source switched to verifying is checked on its next call, however
	// recently a connection to the same address was set up without checking.
	server, _ := tlsDatasource(t)
	if _, _, err := callLimited(context.Background(), directTarget(server.URL, true), "/api/v1/query?query=up",
		nil, maxQueryBody, queryTimeout); err != nil {
		t.Fatal(err)
	}
	_, _, err := callLimited(context.Background(), directTarget(server.URL, false), "/api/v1/query?query=up",
		nil, maxQueryBody, queryTimeout)
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("a verified call to a self-signed endpoint got %v, want a certificate error", err)
	}
}
