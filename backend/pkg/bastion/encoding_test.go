package bastion

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

func gzipped(t *testing.T, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(plain); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	return buf.Bytes()
}

func gzipResponse(body []byte) *Response {
	return &Response{
		Status: http.StatusOK,
		Header: map[string][]string{
			"Content-Type":     {"application/json"},
			"Content-Encoding": {"gzip"},
			"Content-Length":   {"42"},
		},
		Body: body,
	}
}

// The caller sees the body the API server meant, and nothing that still claims
// it is compressed.
func TestDecodeBodyUndoesGzip(t *testing.T) {
	resp := gzipResponse(gzipped(t, []byte(`{"kind":"PodList"}`)))
	if err := decodeBody(resp, maxDecodedBody); err != nil {
		t.Fatalf("decodeBody: %v", err)
	}
	if string(resp.Body) != `{"kind":"PodList"}` {
		t.Fatalf("body = %q", resp.Body)
	}
	if headerValue(resp.Header, "Content-Encoding") != "" || headerValue(resp.Header, "Content-Length") != "" {
		t.Fatalf("encoding headers survived: %v", resp.Header)
	}
	if headerValue(resp.Header, "Content-Type") != "application/json" {
		t.Fatalf("an unrelated header was dropped: %v", resp.Header)
	}
}

// A small answer the API server did not compress passes through untouched.
func TestDecodeBodyLeavesAnUncompressedBodyAlone(t *testing.T) {
	resp := &Response{Status: http.StatusOK, Body: []byte(`{}`)}
	if err := decodeBody(resp, maxDecodedBody); err != nil || string(resp.Body) != `{}` {
		t.Fatalf("decodeBody = %v, body %q", err, resp.Body)
	}
}

// The ceiling counts what the body becomes, not what crossed the tunnel: a
// few kilobytes of gzip that expand past it are refused, and a body exactly at
// it is not.
func TestDecodeBodyBoundsTheDecompressedSize(t *testing.T) {
	const limit = 1024
	exact := gzipResponse(gzipped(t, bytes.Repeat([]byte("a"), limit)))
	if err := decodeBody(exact, limit); err != nil {
		t.Fatalf("a body at the limit was refused: %v", err)
	}

	bomb := gzipped(t, make([]byte, maxDecodedBody+1))
	if len(bomb) > 64<<10 {
		t.Fatalf("test premise: the bomb should be small on the wire, got %d bytes", len(bomb))
	}
	if err := decodeBody(gzipResponse(bomb), maxDecodedBody); !errors.Is(err, errDecodedTooLarge) {
		t.Fatalf("decodeBody = %v, want errDecodedTooLarge", err)
	}
}

// A body that claims gzip and is not is an error, never garbage handed on as
// JSON.
func TestDecodeBodyRefusesACorruptStream(t *testing.T) {
	truncated := gzipped(t, []byte(strings.Repeat(`{"kind":"Pod"}`, 100)))
	for _, body := range [][]byte{[]byte("not gzip"), truncated[:len(truncated)/2]} {
		if err := decodeBody(gzipResponse(body), maxDecodedBody); err == nil {
			t.Errorf("decodeBody(%q...) accepted a corrupt stream", body[:min(len(body), 8)])
		}
	}
}

// End to end through the tunnel: Call asks for gzip, the agent passes the
// compressed answer on as it came, and the caller gets it decoded.
func TestCallAsksForGzipAndDecodesTheAnswer(t *testing.T) {
	h := newHarness(t)
	cluster := h.addCluster(1, "prod-eu", "kmg_valid")
	user := h.addUser(10, "alice", db.SystemRoleUser)

	agent, err := h.dialAgent("kmg_valid", func(Request) Response {
		return *gzipResponse(gzipped(t, []byte(`{"kind":"PodList","items":[]}`)))
	})
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	grant := db.UserClusterAccess{UserID: user.ID, ClusterID: cluster.ID, K8sRole: db.K8sRoleView}
	resp, err := h.proxy.Call(context.Background(), user, cluster, grant, http.MethodGet, "/api/v1/pods", nil, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(resp.Body) != `{"kind":"PodList","items":[]}` {
		t.Fatalf("body = %q", resp.Body)
	}

	seen := agent.requests()
	if len(seen) != 1 || headerValue(seen[0].Header, "Accept-Encoding") != "gzip" {
		t.Fatalf("Call did not ask for gzip: %v", seen)
	}
}

// A body past the ceiling fails the call as the agent's own refusal did, and
// the trail records it as the failure it was.
func TestCallRefusesAnAnswerThatExpandsPastTheCeiling(t *testing.T) {
	h := newHarness(t)
	cluster := h.addCluster(1, "prod-eu", "kmg_valid")
	user := h.addUser(10, "alice", db.SystemRoleUser)

	bomb := gzipped(t, make([]byte, maxDecodedBody+1))
	if _, err := h.dialAgent("kmg_valid", func(Request) Response { return *gzipResponse(bomb) }); err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	grant := db.UserClusterAccess{UserID: user.ID, ClusterID: cluster.ID, K8sRole: db.K8sRoleView}
	_, err := h.proxy.Call(context.Background(), user, cluster, grant, http.MethodGet, "/api/v1/pods", nil, nil)
	var callErr *CallError
	if !errors.As(err, &callErr) || callErr.Status != http.StatusBadGateway {
		t.Fatalf("Call = %v, want a 502", err)
	}

	events := h.audit.events()
	if len(events) != 1 || events[0].Status != http.StatusBadGateway || events[0].Error == "" {
		t.Fatalf("audit = %+v, want one failed record", events)
	}
}
