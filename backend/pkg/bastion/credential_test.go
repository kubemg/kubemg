package bastion

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * The agent's credential as the tunnel listener sees it: a displacement is on
 * the record, a retired tunnel is closed and told why, and a sweep closes a
 * tunnel whose token no longer resolves — but never on a store it cannot read.
 */

func TestDisplacementIsRecorded(t *testing.T) {
	h := newHarness(t)
	h.gateway.UseAuditor(h.audit)
	h.addCluster(1, "prod-eu", "kmg_valid")

	if _, err := h.dialAgent("kmg_valid", okResponse("{}")); err != nil {
		t.Fatalf("dial first agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })
	first, _ := h.gateway.Registry().Get(1)

	if _, err := h.dialAgent("kmg_valid", okResponse("{}")); err != nil {
		t.Fatalf("dial second agent: %v", err)
	}
	waitFor(t, func() bool {
		current, ok := h.gateway.Registry().Get(1)
		return ok && current != first
	})

	var displaced []Event
	for _, event := range h.audit.events() {
		if event.Verb == VerbAgentDisplaced {
			displaced = append(displaced, event)
		}
	}
	if len(displaced) != 1 {
		t.Fatalf("expected one displacement record, got %d", len(displaced))
	}
	event := displaced[0]
	if event.ClusterID != 1 || event.Cluster != "prod-eu" || event.Username != AgentActor {
		t.Fatalf("unexpected displacement record: %+v", event)
	}
	if event.Error != "" || event.Status >= 400 {
		t.Fatal("a displacement is not a refusal")
	}
	raw, found := strings.CutPrefix(event.Path, "/agent/v1/tunnel?")
	if !found {
		t.Fatalf("unexpected path: %q", event.Path)
	}
	query, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("parse path query: %v", err)
	}
	for _, key := range []string{"source", "previous_source"} {
		if query.Get(key) == "" {
			t.Errorf("the record should name %s", key)
		}
	}
	for _, key := range []string{"agent_version", "previous_agent_version"} {
		if query.Get(key) != "test-agent" {
			t.Errorf("%s: expected the handshake's version, got %q", key, query.Get(key))
		}
	}
	for _, key := range []string{"connected_at", "previous_connected_at"} {
		if _, err := time.Parse(time.RFC3339, query.Get(key)); err != nil {
			t.Errorf("%s: expected a timestamp, got %q", key, query.Get(key))
		}
	}
	if strings.Contains(event.Path, "kmg_valid") {
		t.Fatal("the record must not carry the credential")
	}
}

func TestRetireClosesTheTunnelAndSaysWhy(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")

	agent, err := h.dialAgent("kmg_valid", okResponse("{}"))
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })
	if !h.gateway.Retire(1) {
		t.Fatal("a tunnel was attached, so Retire should report closing it")
	}
	waitFor(t, func() bool { return !h.gateway.Registry().Connected(1) })
	select {
	case <-agent.done:
	case <-time.After(3 * time.Second):
		t.Fatal("the retired agent was not hung up on")
	}

	states := h.store.recordedStates()
	last := states[len(states)-1]
	if last.Connected || !strings.Contains(last.StatusMessage, "rotated") {
		t.Fatalf("the cluster should say its token was rotated, got %+v", last)
	}
	if h.gateway.Retire(1) {
		t.Fatal("nothing is attached any more")
	}
}

func TestRetireTellsTheAgentWhy(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")

	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(h.server.URL, "http")+"/agent/v1/tunnel",
		map[string][]string{"Authorization": {"Bearer kmg_valid"}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := writeJSON(conn, Message{Type: MessageHello, Hello: &Hello{ProtocolVersion: ProtocolVersion}}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	var welcome Message
	if err := conn.ReadJSON(&welcome); err != nil {
		t.Fatalf("welcome: %v", err)
	}

	h.gateway.Retire(1)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = conn.ReadMessage()
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Text != retiredReason {
		t.Fatalf("the agent should be told its token was rotated, got %v", err)
	}
}

func TestSweepClosesATunnelWhoseTokenWasRotated(t *testing.T) {
	h := newHarness(t)
	cluster := h.addCluster(1, "prod-eu", "kmg_valid")
	h.addCluster(2, "prod-us", "kmg_other")

	if _, err := h.dialAgent("kmg_valid", okResponse("{}")); err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	if _, err := h.dialAgent("kmg_other", okResponse("{}")); err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Len() == 2 })

	// Rotated by another replica: this one only sees the row change.
	h.store.mu.Lock()
	cluster.AgentToken = "kmg_rotated"
	h.store.mu.Unlock()

	h.gateway.SweepCredentials(context.Background())
	waitFor(t, func() bool { return !h.gateway.Registry().Connected(1) })
	if !h.gateway.Registry().Connected(2) {
		t.Fatal("a tunnel whose token still resolves must be left alone")
	}
	if _, err := h.dialAgent("kmg_valid", okResponse("{}")); err == nil {
		t.Fatal("the rotated token must be refused at the handshake")
	}
}

// failingStore is a tunnel store whose token lookup fails, as a database that
// is down would.
type failingStore struct{ *tunnelStore }

func (failingStore) ClusterByAgentToken(context.Context, string) (*db.Cluster, error) {
	return nil, errors.New("database is down")
}

func TestSweepClosesNothingWhenTheStoreIsUnreadable(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	if _, err := h.dialAgent("kmg_valid", okResponse("{}")); err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	h.gateway.store = failingStore{h.store}
	h.gateway.SweepCredentials(context.Background())
	time.Sleep(50 * time.Millisecond)
	if !h.gateway.Registry().Connected(1) {
		t.Fatal("a database blip must not take the fleet's tunnels down")
	}
}
