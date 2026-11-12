package tunnel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kubemg/kubemg/agent/internal/kube"
	"github.com/kubemg/kubemg/agent/internal/protocol"
)

// echoExec stands in for an API server's exec endpoint: it accepts the
// upgrade and sends every message back as it came.
func echoExec(t *testing.T) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{Subprotocols: []string{"v4.channel.k8s.io"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			kind, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(kind, payload); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// echoedOverTheTunnel runs a real agent against a stand-in bastion that grants
// the capabilities in grant, opens an exec session, sends one chunk down it and
// returns the hello's capabilities and the frame the echo came back in.
func echoedOverTheTunnel(t *testing.T, grant []string, chunk protocol.StreamData) ([]string, int, protocol.StreamData) {
	t.Helper()
	api := echoExec(t)

	type result struct {
		offered []string
		kind    int
		chunk   protocol.StreamData
		err     string
	}
	results := make(chan result, 1)
	upgrader := websocket.Upgrader{}
	bastion := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		fail := func(msg string) { results <- result{err: msg} }

		var hello protocol.Message
		if err := conn.ReadJSON(&hello); err != nil || hello.Hello == nil {
			fail("no hello")
			return
		}
		_ = conn.WriteJSON(protocol.Message{Type: protocol.MessageWelcome, Welcome: &protocol.Welcome{
			ProtocolVersion: protocol.ProtocolVersion, ClusterID: 7, ClusterName: "edge",
			HeartbeatSeconds: 30, Capabilities: grant,
		}})
		_ = conn.WriteJSON(protocol.Message{Type: protocol.MessageStreamOpen, ID: "7-1", StreamOpen: &protocol.StreamOpen{
			Method: http.MethodGet, Path: "/api/v1/namespaces/x/pods/y/exec?command=sh",
			Upgrade: true, Subprotocols: []string{"v4.channel.k8s.io"},
		}})
		var start protocol.Message
		if err := conn.ReadJSON(&start); err != nil || start.StreamStart == nil || start.StreamStart.Error != "" {
			fail("the session did not start")
			return
		}

		if slices.Contains(grant, protocol.CapabilityBinaryStreamData) {
			frame, _ := protocol.EncodeStreamData("7-1", chunk)
			_ = conn.WriteMessage(websocket.BinaryMessage, frame)
		} else {
			_ = conn.WriteJSON(protocol.Message{Type: protocol.MessageStreamData, ID: "7-1", StreamData: &chunk})
		}

		kind, payload, err := conn.ReadMessage()
		if err != nil {
			fail("no echo")
			return
		}
		var echoed protocol.StreamData
		if kind == websocket.BinaryMessage {
			var id string
			id, echoed, err = protocol.DecodeStreamData(payload)
			if err != nil || id != "7-1" {
				fail("a malformed binary echo")
				return
			}
		} else {
			var msg protocol.Message
			if json.Unmarshal(payload, &msg) != nil || msg.StreamData == nil {
				fail("a malformed JSON echo")
				return
			}
			echoed = *msg.StreamData
		}
		results <- result{offered: hello.Hello.Capabilities, kind: kind, chunk: echoed}
	}))
	t.Cleanup(bastion.Close)

	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("sa-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	kubeClient, err := kube.New(kube.Options{APIURL: api.URL, InsecureSkipVerify: true, TokenPath: tokenPath})
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(Options{BastionURL: bastion.URL, Token: "kmg_test", Kube: kubeClient})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = client.Run(ctx) }()

	select {
	case got := <-results:
		if got.err != "" {
			t.Fatal(got.err)
		}
		return got.offered, got.kind, got.chunk
	case <-time.After(10 * time.Second):
		t.Fatal("the session never echoed")
	}
	return nil, 0, protocol.StreamData{}
}

func TestAnAgentOffersBinaryStreamData(t *testing.T) {
	offered, _, _ := echoedOverTheTunnel(t, nil, protocol.StreamData{Data: []byte("\x00ls\n"), Binary: true})
	if !slices.Contains(offered, protocol.CapabilityBinaryStreamData) {
		t.Fatalf("hello offered %v", offered)
	}
}

func TestSessionBytesTravelAsBinaryFramesWhenGranted(t *testing.T) {
	sent := protocol.StreamData{Data: []byte("\x00\xff\x01raw bytes"), Binary: true}
	_, kind, echoed := echoedOverTheTunnel(t,
		[]string{protocol.CapabilityResponseParts, protocol.CapabilityBinaryStreamData}, sent)
	if kind != websocket.BinaryMessage {
		t.Fatalf("the echo came back as message type %d, want binary", kind)
	}
	if string(echoed.Data) != string(sent.Data) || !echoed.Binary {
		t.Fatalf("echo = %q (binary %t), want %q", echoed.Data, echoed.Binary, sent.Data)
	}
}

func TestABastionThatDoesNotGrantItGetsJSON(t *testing.T) {
	sent := protocol.StreamData{Data: []byte("\x00\xff\x01raw bytes"), Binary: true}
	_, kind, echoed := echoedOverTheTunnel(t, []string{protocol.CapabilityResponseParts}, sent)
	if kind != websocket.TextMessage {
		t.Fatalf("the echo came back as message type %d, want text", kind)
	}
	if string(echoed.Data) != string(sent.Data) || !echoed.Binary {
		t.Fatalf("echo = %q (binary %t), want %q", echoed.Data, echoed.Binary, sent.Data)
	}
}
