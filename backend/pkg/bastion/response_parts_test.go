package bastion

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// dialWithHello completes the handshake with the given capabilities and hands
// back the raw socket and the welcome, for tests that speak the wire format
// themselves.
func dialWithHello(t *testing.T, h *harness, token string, capabilities []string) (*websocket.Conn, Welcome) {
	t.Helper()

	url := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/agent/v1/tunnel"
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if err := writeJSON(conn, Message{Type: MessageHello, Hello: &Hello{
		ProtocolVersion: ProtocolVersion,
		AgentVersion:    "test-agent",
		Capabilities:    capabilities,
	}}); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	var welcome Message
	if err := conn.ReadJSON(&welcome); err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	if welcome.Type != MessageWelcome || welcome.Welcome == nil {
		t.Fatalf("expected a welcome frame, got %q", welcome.Type)
	}
	return conn, *welcome.Welcome
}

// The server answers with what it accepts, and only that: an agent newer than
// the server may offer something the server has never heard of, and must not be
// told it can use it.
func TestTheWelcomeNamesOnlyAcceptedCapabilities(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")

	_, welcome := dialWithHello(t, h, "kmg_valid", []string{"teleport", CapabilityResponseParts})
	if len(welcome.Capabilities) != 1 || welcome.Capabilities[0] != CapabilityResponseParts {
		t.Fatalf("welcome capabilities = %v, want only %q", welcome.Capabilities, CapabilityResponseParts)
	}
}

// An agent from before capabilities existed offers none and is offered none, so
// it keeps sending whole responses — which the rest of this suite proves still
// work.
func TestAnAgentOfferingNothingIsOfferedNothing(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")

	_, welcome := dialWithHello(t, h, "kmg_valid", nil)
	if len(welcome.Capabilities) != 0 {
		t.Fatalf("welcome capabilities = %v, want none", welcome.Capabilities)
	}
}

// The point of the change, end to end: a body sent as parts reaches the caller
// as the one body it always was, in order, with the final frame's own bytes last.
func TestAResponseSentInPartsArrivesWhole(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	admin := h.addUser(10, "admin", db.SystemRoleAdmin)

	conn, _ := dialWithHello(t, h, "kmg_valid", []string{CapabilityResponseParts})
	go func() {
		for {
			var msg Message
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			if msg.Type != MessageRequest {
				continue
			}
			for _, part := range []string{`{"kind":`, `"PodList",`} {
				_ = writeJSON(conn, Message{Type: MessageResponsePart, ID: msg.ID,
					ResponsePart: &ResponsePart{Data: []byte(part)}})
			}
			_ = writeJSON(conn, Message{Type: MessageResponse, ID: msg.ID, Response: &Response{
				Status: http.StatusOK,
				Header: map[string][]string{"Content-Type": {"application/json"}},
				Body:   []byte(`"items":[]}`),
			}})
		}
	}()
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	resp := h.proxyRequest(http.MethodGet, "/api/v1/clusters/1/proxy/api/v1/pods", h.token(admin))
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	if string(body) != `{"kind":"PodList","items":[]}` {
		t.Fatalf("the parts were not joined in order: %s", body)
	}
}

// Parts past the ceiling are not delivered as a truncated body: half a list
// presented as the whole one is worse than an error. The ceiling is the one a
// whole response always had, so parts change how a body travels and nothing
// about how large one may be.
func TestPartsPastTheLimitBecomeAnError(t *testing.T) {
	parts := &assembly{}
	parts.add([]byte("12345"), 8)
	parts.add([]byte("67890"), 8)

	resp := &Response{Status: http.StatusOK, Body: []byte("tail")}
	parts.finish(resp, 8)
	if resp.Error == "" || resp.Body != nil {
		t.Fatalf("an oversized body must become an error, got %+v", resp)
	}
}

// The final frame's own body counts towards the ceiling too.
func TestTheFinalFrameCountsTowardsTheLimit(t *testing.T) {
	parts := &assembly{}
	parts.add([]byte("123456"), 8)

	resp := &Response{Status: http.StatusOK, Body: []byte("789")}
	parts.finish(resp, 8)
	if resp.Error == "" {
		t.Fatalf("parts plus the final body past the limit must fail, got %+v", resp)
	}
}

// A final frame that says the agent failed keeps its own explanation; the parts
// before it described a body that is not coming.
func TestAFailedFinalFrameDiscardsItsParts(t *testing.T) {
	parts := &assembly{}
	parts.add([]byte("partial"), 1024)

	resp := &Response{Error: "the API server went away"}
	parts.finish(resp, 1024)
	if resp.Error != "the API server went away" || resp.Body != nil {
		t.Fatalf("a failed response must keep its error and no body, got %+v", resp)
	}
}

// A request is bulk; a stream's frames are what somebody is waiting on. Which
// queue a frame goes in is decided by its type, here, and nowhere else.
func TestRequestsQueueAsBulkAndStreamFramesAsUrgent(t *testing.T) {
	tunnel := queuedTunnel(4, time.Second)

	if err := tunnel.send(Message{Type: MessageRequest, ID: "1-1", Request: &Request{Method: "GET"}}); err != nil {
		t.Fatalf("send request: %v", err)
	}
	if err := tunnel.send(Message{Type: MessageStreamData, ID: "s1-2", StreamData: &StreamData{Data: []byte("ls\n")}}); err != nil {
		t.Fatalf("send stream data: %v", err)
	}

	if len(tunnel.bulk) != 1 || len(tunnel.out) != 1 {
		t.Fatalf("bulk=%d urgent=%d, want one of each", len(tunnel.bulk), len(tunnel.out))
	}
	var msg Message
	if err := json.Unmarshal(<-tunnel.bulk, &msg); err != nil || msg.Type != MessageRequest {
		t.Fatalf("the bulk queue holds %q, want a request", msg.Type)
	}
}

// A keystroke queued behind a list response goes out first. This is the
// head-of-line block the two queues exist to remove.
func TestAnUrgentFrameOvertakesQueuedBulk(t *testing.T) {
	tunnel := queuedTunnel(8, time.Second)
	tunnel.bulk <- []byte("list part 1")
	tunnel.bulk <- []byte("list part 2")
	tunnel.out <- []byte("keystroke")

	if got, _ := tunnel.nextFrame(); string(got) != "keystroke" {
		t.Fatalf("first frame out = %q, want the keystroke", got)
	}
	if got, _ := tunnel.nextFrame(); string(got) != "list part 1" {
		t.Fatalf("second frame out = %q, want the bulk frames in order", got)
	}
}

// And the other half of the rule: a stream that never stops sending cannot
// starve an API call into its timeout. Bulk gets a turn every urgentBurst frames.
func TestBulkGetsATurnUnderSustainedUrgentTraffic(t *testing.T) {
	tunnel := queuedTunnel(urgentBurst*3, time.Second)
	tunnel.bulk <- []byte("bulk")
	for range urgentBurst * 2 {
		tunnel.out <- []byte("urgent")
	}

	for i := 0; i < urgentBurst; i++ {
		if got, _ := tunnel.nextFrame(); string(got) != "urgent" {
			t.Fatalf("frame %d = %q, want urgent frames first", i, got)
		}
	}
	if got, _ := tunnel.nextFrame(); string(got) != "bulk" {
		t.Fatalf("after %d urgent frames got %q, want the waiting bulk frame", urgentBurst, got)
	}
	if got, _ := tunnel.nextFrame(); string(got) != "urgent" {
		t.Fatalf("after the bulk turn got %q, want urgent again", got)
	}
}

// A closed tunnel stops the writer rather than leaving it parked on two empty
// queues.
func TestTheWriterStopsWhenTheTunnelCloses(t *testing.T) {
	tunnel := queuedTunnel(1, time.Second)
	close(tunnel.closed)

	if payload, ok := tunnel.nextFrame(); ok {
		t.Fatalf("a closed tunnel produced a frame: %q", payload)
	}
}
