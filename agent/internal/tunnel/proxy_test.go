package tunnel

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// connectProxy is a minimal HTTP CONNECT proxy for tests. It records which
// hosts it forwarded to so assertions can verify the agent went through it.
type connectProxy struct {
	mu      sync.Mutex
	targets []string
	server  *httptest.Server
}

func newConnectProxy(t *testing.T) *connectProxy {
	t.Helper()
	p := &connectProxy{}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "only CONNECT", http.StatusMethodNotAllowed)
			return
		}
		dest, err := net.Dial("tcp", r.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer dest.Close()

		p.mu.Lock()
		p.targets = append(p.targets, r.Host)
		p.mu.Unlock()

		w.WriteHeader(http.StatusOK)
		hijack, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		client, _, err := hijack.Hijack()
		if err != nil {
			return
		}
		defer client.Close()

		done := make(chan struct{})
		go func() { defer close(done); _, _ = io.Copy(dest, client) }()
		_, _ = io.Copy(client, dest)
		<-done
	}))
	t.Cleanup(p.server.Close)
	return p
}

func (p *connectProxy) URL() string { return p.server.URL }

func (p *connectProxy) dialled(host string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, t := range p.targets {
		if t == host || strings.HasPrefix(t, host+":") {
			return true
		}
	}
	return false
}

// bastionStub is a minimal WebSocket server that accepts one connection and
// then closes it — just enough for the dialer test to prove a connection was
// made (possibly through a proxy).
func bastionStub(t *testing.T) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn.Close()
	}))
	t.Cleanup(server.Close)
	return server
}

// alwaysProxy returns a Proxy function that always routes through proxyAddr.
// Unlike http.ProxyFromEnvironment, it does not cache, so it works in tests
// that set env vars after the process starts. (In production, http.ProxyFromEnvironment
// is correct because the env is set before the process begins.)
func alwaysProxy(proxyAddr string) func(*http.Request) (*url.URL, error) {
	u, _ := url.Parse(proxyAddr)
	return func(_ *http.Request) (*url.URL, error) { return u, nil }
}

// neverProxy is a Proxy function that always returns nil (direct connection).
func neverProxy(_ *http.Request) (*url.URL, error) { return nil, nil }

// TestTunnelDialerUsesProxy verifies that setting a Proxy function on the
// dialer routes the tunnel through a CONNECT proxy. The production code sets
// Proxy: http.ProxyFromEnvironment which gorilla/websocket honours the same way.
func TestTunnelDialerUsesProxy(t *testing.T) {
	bastion := bastionStub(t)
	proxy := newConnectProxy(t)
	bastionHost := strings.TrimPrefix(bastion.URL, "http://")

	dialer := &websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
		Proxy:            alwaysProxy(proxy.URL()),
	}

	wsURL := fmt.Sprintf("ws://%s", bastionHost)
	conn, _, err := dialer.DialContext(context.Background(), wsURL, nil)
	if err == nil {
		conn.Close()
	}
	// A CONNECT to bastionHost must have been received — the connection may
	// then fail (e.g. TLS mismatch), but the CONNECT itself proves the proxy
	// was consulted and would have been used.
	if !proxy.dialled(bastionHost) {
		t.Errorf("proxy was not used: CONNECT to %q never arrived (targets: %v)",
			bastionHost, proxy.targets)
	}
}

// TestTunnelDialerBypassesProxyForDirectDial verifies that a nil proxy
// function means the dialer connects directly, without going through any proxy.
func TestTunnelDialerBypassesProxyForDirectDial(t *testing.T) {
	bastion := bastionStub(t)
	proxy := newConnectProxy(t)
	bastionHost := strings.TrimPrefix(bastion.URL, "http://")

	dialer := &websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
		Proxy:            neverProxy,
	}
	wsURL := fmt.Sprintf("ws://%s", bastionHost)
	conn, _, _ := dialer.DialContext(context.Background(), wsURL, nil)
	if conn != nil {
		conn.Close()
	}

	if proxy.dialled(bastionHost) {
		t.Errorf("proxy was contacted even though Proxy function returns nil")
	}
}
