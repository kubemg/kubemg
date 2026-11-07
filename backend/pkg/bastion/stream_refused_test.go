package bastion

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// TestOpenStreamRelaysClusterRefusal checks that OpenStream returns a
// *RefusedStreamError when the agent sends back a StreamStart with both an
// Error and a non-zero Status — the case where the API server refused the
// upgrade (exec/attach) with 403, 401, etc.
func TestOpenStreamRelaysClusterRefusal(t *testing.T) {
	tunnel := &Tunnel{
		ClusterID: 1,
		out:       make(chan []byte, 8),
		bulk:      make(chan []byte, 8),
		queueWait: 10 * time.Millisecond,
		pending:   map[string]*pendingCall{},
		streams:   map[string]*Stream{},
		closed:    make(chan struct{}),
	}

	// Drain the output queue so send() never blocks during the handshake.
	go func() {
		for range tunnel.out {
		}
	}()

	// Simulate the agent answering with a refused StreamStart once the stream
	// has been registered by OpenStream.
	go func() {
		var s *Stream
		for s == nil {
			time.Sleep(time.Millisecond)
			tunnel.mu.Lock()
			for _, v := range tunnel.streams {
				s = v
			}
			tunnel.mu.Unlock()
		}
		s.start <- &StreamStart{
			Status: http.StatusForbidden,
			Error:  `pods "test" is forbidden: cannot create resource "pods/exec"`,
		}
	}()

	_, _, err := tunnel.OpenStream(context.Background(), &StreamOpen{
		Method:  http.MethodGet,
		Path:    "/api/v1/namespaces/default/pods/test/exec",
		Upgrade: true,
	})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	var refused *RefusedStreamError
	if !errors.As(err, &refused) {
		t.Fatalf("expected *RefusedStreamError, got %T: %v", err, err)
	}
	if refused.Status != http.StatusForbidden {
		t.Errorf("Status = %d, want %d", refused.Status, http.StatusForbidden)
	}
	if refused.Message == "" {
		t.Error("Message is empty, want the cluster's forbidden message")
	}
}

// TestOpenStreamTunnelErrorIsNotRefused verifies that an error StreamStart
// with no status code (a tunnel-side failure, not an API server refusal) does
// not produce a *RefusedStreamError, so the caller still falls through to
// tunnelFailure for the right 502/503 response.
func TestOpenStreamTunnelErrorIsNotRefused(t *testing.T) {
	tunnel := &Tunnel{
		ClusterID: 1,
		out:       make(chan []byte, 8),
		bulk:      make(chan []byte, 8),
		queueWait: 10 * time.Millisecond,
		pending:   map[string]*pendingCall{},
		streams:   map[string]*Stream{},
		closed:    make(chan struct{}),
	}
	go func() {
		for range tunnel.out {
		}
	}()

	go func() {
		var s *Stream
		for s == nil {
			time.Sleep(time.Millisecond)
			tunnel.mu.Lock()
			for _, v := range tunnel.streams {
				s = v
			}
			tunnel.mu.Unlock()
		}
		// Status is 0: a tunnel-level error, not a cluster refusal.
		s.start <- &StreamStart{Error: "could not build the upstream request: bad path"}
	}()

	_, _, err := tunnel.OpenStream(context.Background(), &StreamOpen{
		Method: http.MethodGet,
		Path:   "/api/v1/namespaces/default/pods/test/exec",
	})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	var refused *RefusedStreamError
	if errors.As(err, &refused) {
		t.Fatalf("a tunnel error must not be a *RefusedStreamError, got Status=%d", refused.Status)
	}
}
