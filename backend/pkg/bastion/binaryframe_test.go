package bastion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// streamDataVector is the frame both sides of the tunnel test against:
// agent/internal/protocol/binary_test.go holds the same bytes.
var streamDataVector = []byte{0x01, 0x01, 0x04, '7', '-', '4', '2', 0x00, 'h', 'i'}

func TestStreamDataFrameMatchesTheAgents(t *testing.T) {
	frame, ok := encodeStreamData("7-42", StreamData{Data: []byte("\x00hi"), Binary: true})
	if !ok || !bytes.Equal(frame, streamDataVector) {
		t.Fatalf("frame = %v, want %v", frame, streamDataVector)
	}
	id, chunk, err := decodeStreamData(streamDataVector)
	if err != nil || id != "7-42" || !chunk.Binary || string(chunk.Data) != "\x00hi" {
		t.Fatalf("decoded %q %+v %v", id, chunk, err)
	}
}

func TestMalformedStreamDataFramesAreRefused(t *testing.T) {
	for name, frame := range map[string][]byte{
		"empty":          {},
		"header only":    {0x01, 0x00},
		"wrong type":     {0x02, 0x00, 0x01, 'a'},
		"unknown flag":   {0x01, 0x02, 0x01, 'a'},
		"no ID":          {0x01, 0x00, 0x00, 'a'},
		"truncated ID":   {0x01, 0x00, 0x05, 'a', 'b'},
		"a JSON message": []byte(`{"type":"stream_data"}`),
	} {
		if _, _, err := decodeStreamData(frame); !errors.Is(err, errMalformedBinaryFrame) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestOnlyStreamDataGoesBinaryAndOnlyWhenNegotiated(t *testing.T) {
	chunk := &StreamData{Data: []byte("x")}
	for _, tc := range []struct {
		offered []string
		msg     Message
		binary  bool
	}{
		{[]string{CapabilityBinaryStreamData}, Message{Type: MessageStreamData, ID: "s1-1", StreamData: chunk}, true},
		{nil, Message{Type: MessageStreamData, ID: "s1-1", StreamData: chunk}, false},
		{[]string{CapabilityResponseParts}, Message{Type: MessageStreamData, ID: "s1-1", StreamData: chunk}, false},
		{[]string{CapabilityBinaryStreamData}, Message{Type: MessageStreamClose, ID: "s1-1", StreamClose: &StreamClose{}}, false},
		{[]string{CapabilityBinaryStreamData}, Message{Type: MessageStreamOpen, ID: "s1-1", StreamOpen: &StreamOpen{}}, false},
	} {
		tunnel := &Tunnel{binaryStreams: slices.Contains(negotiateCapabilities(tc.offered), CapabilityBinaryStreamData)}
		payload, err := tunnel.encode(tc.msg)
		if err != nil {
			t.Fatal(err)
		}
		if got := wireKind(payload) == websocket.BinaryMessage; got != tc.binary {
			t.Errorf("%s offered %v: binary = %t, want %t", tc.msg.Type, tc.offered, got, tc.binary)
		}
	}
}

// rawAgent dials the tunnel as an agent offering capabilities, and returns the
// socket once the welcome has arrived.
func rawAgent(t *testing.T, h *harness, token string, capabilities []string) (*websocket.Conn, Welcome) {
	t.Helper()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(h.server.URL, "http")+"/agent/v1/tunnel", header)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := writeJSON(conn, Message{Type: MessageHello, Hello: &Hello{
		ProtocolVersion: ProtocolVersion, AgentVersion: "test", Capabilities: capabilities,
	}}); err != nil {
		t.Fatal(err)
	}
	var welcome Message
	if err := conn.ReadJSON(&welcome); err != nil || welcome.Welcome == nil {
		t.Fatalf("no welcome: %v", err)
	}
	return conn, *welcome.Welcome
}

// openSession opens an exec-shaped stream from the bastion and answers it from
// the agent's side.
func openSession(t *testing.T, h *harness, conn *websocket.Conn) *Stream {
	t.Helper()
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })
	tunnel, _ := h.gateway.Registry().Get(1)

	type opened struct {
		stream *Stream
		err    error
	}
	done := make(chan opened, 1)
	go func() {
		stream, _, err := tunnel.OpenStream(context.Background(), &StreamOpen{
			Method: http.MethodGet, Path: "/api/v1/namespaces/x/pods/y/exec", Upgrade: true,
		})
		done <- opened{stream, err}
	}()

	var open Message
	if err := conn.ReadJSON(&open); err != nil || open.Type != MessageStreamOpen {
		t.Fatalf("expected a stream_open, got %+v (%v)", open, err)
	}
	if err := writeJSON(conn, Message{Type: MessageStreamStart, ID: open.ID,
		StreamStart: &StreamStart{Status: http.StatusSwitchingProtocols}}); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	return got.stream
}

func TestSessionBytesTravelAsBinaryFramesBothWays(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "edge", "kmg_binary")
	conn, welcome := rawAgent(t, h, "kmg_binary", []string{CapabilityResponseParts, CapabilityBinaryStreamData})
	if !slices.Contains(welcome.Capabilities, CapabilityBinaryStreamData) {
		t.Fatalf("welcome granted %v", welcome.Capabilities)
	}
	stream := openSession(t, h, conn)

	// Towards the cluster: what the user typed.
	if err := stream.Send(StreamData{Data: []byte("\x00ls -la\n"), Binary: true}); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := conn.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage {
		t.Fatalf("the agent got message type %d (%v), want binary", kind, err)
	}
	id, chunk, err := decodeStreamData(payload)
	if err != nil || id != stream.ID || string(chunk.Data) != "\x00ls -la\n" || !chunk.Binary {
		t.Fatalf("the agent decoded %q %q %v", id, chunk.Data, err)
	}

	// From the cluster: the output.
	frame, _ := encodeStreamData(stream.ID, StreamData{Data: []byte("\x01total 0\n"), Binary: true})
	if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-stream.Chunks():
		if string(got.Data) != "\x01total 0\n" || !got.Binary {
			t.Fatalf("the stream got %q (binary %t)", got.Data, got.Binary)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the output never reached the stream")
	}
}

func TestAnAgentThatDoesNotOfferItGetsJSON(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "edge", "kmg_json")
	conn, welcome := rawAgent(t, h, "kmg_json", []string{CapabilityResponseParts})
	if slices.Contains(welcome.Capabilities, CapabilityBinaryStreamData) {
		t.Fatal("granted a capability the agent did not offer")
	}
	stream := openSession(t, h, conn)

	if err := stream.Send(StreamData{Data: []byte("\x00ls\n"), Binary: true}); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := conn.ReadMessage()
	if err != nil || kind != websocket.TextMessage {
		t.Fatalf("the agent got message type %d (%v), want text", kind, err)
	}
	var msg Message
	if err := json.Unmarshal(payload, &msg); err != nil || msg.StreamData == nil || string(msg.StreamData.Data) != "\x00ls\n" {
		t.Fatalf("the agent got %s", payload)
	}
}

// A binary message this side cannot read is a protocol violation, exactly as an
// unparseable JSON frame is.
func TestAMalformedBinaryFrameDropsTheTunnel(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "edge", "kmg_bad")
	conn, _ := rawAgent(t, h, "kmg_bad", []string{CapabilityBinaryStreamData})
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	if err := conn.WriteMessage(websocket.BinaryMessage, []byte{0x07, 0x00}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !h.gateway.Registry().Connected(1) })
}
