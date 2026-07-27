package bastion

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/kubemg/kubemg/backend/pkg/auth"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

// tunnelStore is an in-memory stand-in for the persistence layer, satisfying
// both the tunnel listener's and the proxy's needs.
type tunnelStore struct {
	mu       sync.Mutex
	users    map[uint]*db.User
	clusters map[uint]*db.Cluster
	access   map[uint]map[uint]db.UserClusterAccess
	states   []db.AgentState
}

func newTunnelStore() *tunnelStore {
	return &tunnelStore{
		users:    map[uint]*db.User{},
		clusters: map[uint]*db.Cluster{},
		access:   map[uint]map[uint]db.UserClusterAccess{},
	}
}

func (s *tunnelStore) ClusterByAgentToken(_ context.Context, token string) (*db.Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cluster := range s.clusters {
		if cluster.AgentToken != "" && cluster.AgentToken == token {
			return cluster, nil
		}
	}
	return nil, db.ErrNotFound
}

func (s *tunnelStore) ClusterByID(_ context.Context, id uint) (*db.Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cluster, ok := s.clusters[id]; ok {
		return cluster, nil
	}
	return nil, db.ErrNotFound
}

func (s *tunnelStore) UserByID(_ context.Context, id uint) (*db.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if user, ok := s.users[id]; ok {
		return user, nil
	}
	return nil, db.ErrNotFound
}

func (s *tunnelStore) AccessForUser(_ context.Context, userID uint) (map[uint]db.UserClusterAccess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[uint]db.UserClusterAccess{}
	for clusterID, grant := range s.access[userID] {
		out[clusterID] = grant
	}
	return out, nil
}

func (s *tunnelStore) UpdateClusterAgent(_ context.Context, id uint, state db.AgentState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cluster, ok := s.clusters[id]
	if !ok {
		return db.ErrNotFound
	}
	s.states = append(s.states, state)
	if state.Connected {
		cluster.Status = db.StatusHealthy
		cluster.AgentVersion = state.AgentVersion
		return nil
	}
	cluster.Status = db.StatusUnhealthy
	return nil
}

func (s *tunnelStore) recordedStates() []db.AgentState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]db.AgentState(nil), s.states...)
}

// recordingAuditor keeps audit events in memory so tests can assert on the
// trail rather than on log text.
type recordingAuditor struct {
	mu      sync.Mutex
	events_ []Event
}

func (a *recordingAuditor) Record(_ context.Context, event Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events_ = append(a.events_, event)
}

func (a *recordingAuditor) events() []Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Event(nil), a.events_...)
}

// harness wires a bastion, a proxy and an HTTP server the way main.go does.
type harness struct {
	t       *testing.T
	server  *httptest.Server
	store   *tunnelStore
	gateway *Server
	jwt     *auth.Manager
	audit   *recordingAuditor
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	store := newTunnelStore()
	auditor := &recordingAuditor{}
	gateway := NewServer(ServerOptions{Store: store})
	proxy := NewProxy(ProxyOptions{
		Store:    store,
		Registry: gateway.Registry(),
		Auditor:  auditor,
	})
	manager := auth.NewManager("test-secret", time.Hour)

	router := gin.New()
	router.GET("/agent/v1/tunnel", gateway.HandleAgent)
	router.Any("/api/v1/clusters/:id/proxy/*path", auth.RequireAuth(manager), proxy.Handle)

	h := &harness{
		t:       t,
		server:  httptest.NewServer(router),
		store:   store,
		gateway: gateway,
		jwt:     manager,
		audit:   auditor,
	}
	t.Cleanup(h.server.Close)
	return h
}

func (h *harness) addCluster(id uint, name, token string) *db.Cluster {
	cluster := &db.Cluster{
		ID:             id,
		Name:           name,
		ConnectionMode: db.ModeAgent,
		AgentToken:     token,
		Status:         db.StatusPending,
	}
	h.store.mu.Lock()
	h.store.clusters[id] = cluster
	h.store.mu.Unlock()
	return cluster
}

func (h *harness) addUser(id uint, username, systemRole string) *db.User {
	user := &db.User{ID: id, Username: username, SystemRole: systemRole, IsActive: true}
	user.Normalize()
	h.store.mu.Lock()
	h.store.users[id] = user
	h.store.mu.Unlock()
	return user
}

func (h *harness) grant(userID, clusterID uint, role string, namespaces []string) {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	if h.store.access[userID] == nil {
		h.store.access[userID] = map[uint]db.UserClusterAccess{}
	}
	h.store.access[userID][clusterID] = db.UserClusterAccess{
		UserID:     userID,
		ClusterID:  clusterID,
		K8sRole:    role,
		Namespaces: db.JoinNamespaces(namespaces),
	}
}

func (h *harness) token(user *db.User) string {
	h.t.Helper()
	token, _, err := h.jwt.Generate(user.ID, user.Username, user.Role)
	if err != nil {
		h.t.Fatalf("generate token: %v", err)
	}
	return token
}

// fakeAgent is a test double for the in-cluster agent: it dials the tunnel and
// answers every proxied request with whatever the test told it to.
type fakeAgent struct {
	conn    *websocket.Conn
	writeMu sync.Mutex

	// seen captures the requests the bastion pushed down the tunnel, so a test
	// can assert on the impersonation headers the agent would have replayed.
	mu       sync.Mutex
	seen     []Request
	opened   []StreamOpen
	received []StreamData
	// closedStreams counts stream_close frames arriving from the bastion.
	closedStreams int
	// onStream services a stream_open. Nil means this agent refuses streams.
	onStream func(*fakeAgent, string, StreamOpen)
	done     chan struct{}
}

// dialAgent connects a fake agent that serves ordinary request/response pairs
// and refuses streams.
func (h *harness) dialAgent(token string, respond func(Request) Response) (*fakeAgent, error) {
	h.t.Helper()
	return h.dialStreamingAgent(token, respond, nil)
}

// dialStreamingAgent connects a fake agent and completes the handshake, with a
// handler for stream_open frames.
func (h *harness) dialStreamingAgent(
	token string,
	respond func(Request) Response,
	onStream func(*fakeAgent, string, StreamOpen),
) (*fakeAgent, error) {
	h.t.Helper()

	url := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/agent/v1/tunnel"
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}

	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		return nil, err
	}

	hello := Message{Type: MessageHello, Hello: &Hello{
		ProtocolVersion:   ProtocolVersion,
		AgentVersion:      "test-agent",
		KubernetesVersion: "v1.31.4",
	}}
	if err := writeJSON(conn, hello); err != nil {
		return nil, err
	}

	agent := &fakeAgent{conn: conn, onStream: onStream, done: make(chan struct{})}

	// Wait for the welcome so the caller knows registration completed.
	var welcome Message
	if err := conn.ReadJSON(&welcome); err != nil {
		return nil, err
	}
	if welcome.Type != MessageWelcome {
		h.t.Fatalf("expected a welcome frame, got %q", welcome.Type)
	}

	go agent.serve(respond)
	h.t.Cleanup(agent.close)
	return agent, nil
}

func (a *fakeAgent) serve(respond func(Request) Response) {
	defer close(a.done)
	for {
		var msg Message
		if err := a.conn.ReadJSON(&msg); err != nil {
			return
		}

		switch msg.Type {
		case MessageRequest:
			if msg.Request == nil {
				continue
			}
			a.mu.Lock()
			a.seen = append(a.seen, *msg.Request)
			a.mu.Unlock()

			resp := respond(*msg.Request)
			if err := writeJSON(a.conn, Message{
				Type: MessageResponse, ID: msg.ID, Response: &resp,
			}); err != nil {
				return
			}

		case MessageStreamOpen:
			if msg.StreamOpen == nil {
				continue
			}
			a.mu.Lock()
			a.opened = append(a.opened, *msg.StreamOpen)
			handler := a.onStream
			a.mu.Unlock()

			if handler == nil {
				writeJSON(a.conn, Message{Type: MessageStreamStart, ID: msg.ID,
					StreamStart: &StreamStart{Error: "this agent serves no streams"}})
				continue
			}
			go handler(a, msg.ID, *msg.StreamOpen)

		case MessageStreamData:
			if msg.StreamData == nil {
				continue
			}
			a.mu.Lock()
			a.received = append(a.received, *msg.StreamData)
			a.mu.Unlock()

		case MessageStreamClose:
			a.mu.Lock()
			a.closedStreams++
			a.mu.Unlock()
		}
	}
}

// send writes a frame from the agent's side, serialised like the real one.
func (a *fakeAgent) send(msg Message) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return writeJSON(a.conn, msg)
}

func (a *fakeAgent) streamOpens() []StreamOpen {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]StreamOpen(nil), a.opened...)
}

func (a *fakeAgent) streamReceived() []StreamData {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]StreamData(nil), a.received...)
}

func (a *fakeAgent) close() { _ = a.conn.Close() }

func (a *fakeAgent) requests() []Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Request(nil), a.seen...)
}

func writeJSON(conn *websocket.Conn, msg Message) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, payload)
}

// okResponse answers every request with a fixed 200.
func okResponse(body string) func(Request) Response {
	return func(Request) Response {
		return Response{
			Status: http.StatusOK,
			Header: map[string][]string{"Content-Type": {"application/json"}},
			Body:   []byte(body),
		}
	}
}

func (h *harness) proxyRequest(method, path, token string) *http.Response {
	h.t.Helper()

	req, err := http.NewRequest(method, h.server.URL+path, nil)
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatalf("proxy request: %v", err)
	}
	h.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestAgentHandshakeMarksTheClusterHealthy(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")

	if _, err := h.dialAgent("kmg_valid", okResponse("{}")); err != nil {
		t.Fatalf("dial agent: %v", err)
	}

	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	states := h.store.recordedStates()
	if len(states) == 0 || !states[0].Connected {
		t.Fatalf("expected a connected state to be recorded, got %+v", states)
	}
	if states[0].AgentVersion != "test-agent" || states[0].KubernetesVersion != "v1.31.4" {
		t.Fatalf("handshake details were not recorded: %+v", states[0])
	}
}

func TestAgentTunnelRejectsAnUnknownToken(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")

	if _, err := h.dialAgent("kmg_wrong", okResponse("{}")); err == nil {
		t.Fatal("an agent with an unknown token must not get a tunnel")
	}
	if h.gateway.Registry().Len() != 0 {
		t.Fatal("a rejected agent must not appear in the registry")
	}
}

func TestAgentTunnelRejectsADirectModeCluster(t *testing.T) {
	h := newHarness(t)
	cluster := h.addCluster(1, "prod-eu", "kmg_valid")
	cluster.ConnectionMode = db.ModeDirect

	if _, err := h.dialAgent("kmg_valid", okResponse("{}")); err == nil {
		t.Fatal("a direct-mode cluster must not accept an agent tunnel")
	}
}

func TestAgentDisconnectMarksTheClusterUnhealthy(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")

	agent, err := h.dialAgent("kmg_valid", okResponse("{}"))
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	agent.close()
	waitFor(t, func() bool { return !h.gateway.Registry().Connected(1) })

	states := h.store.recordedStates()
	last := states[len(states)-1]
	if last.Connected {
		t.Fatalf("expected a disconnect to be recorded, got %+v", last)
	}
	if last.StatusMessage == "" {
		t.Fatal("a dropped tunnel should say why the cluster is unreachable")
	}
}

func TestProxyForwardsWithImpersonation(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	user := h.addUser(10, "devops", db.SystemRoleUser)
	h.grant(10, 1, db.K8sRoleEdit, nil)

	agent, err := h.dialAgent("kmg_valid", okResponse(`{"kind":"PodList"}`))
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	resp := h.proxyRequest(http.MethodGet,
		"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods", h.token(user))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	seen := agent.requests()
	if len(seen) != 1 {
		t.Fatalf("expected exactly one request through the tunnel, got %d", len(seen))
	}
	if seen[0].Path != "/api/v1/namespaces/team-a/pods" {
		t.Fatalf("the API path was not preserved: %q", seen[0].Path)
	}
	if got := seen[0].Header["Impersonate-User"]; len(got) != 1 || got[0] != "devops" {
		t.Fatalf("Impersonate-User = %v", got)
	}
	groups := seen[0].Header["Impersonate-Group"]
	if len(groups) != 2 || groups[0] != "kubemg:edit" {
		t.Fatalf("Impersonate-Group = %v", groups)
	}
}

func TestProxyPreservesTheQueryString(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	admin := h.addUser(10, "admin", db.SystemRoleAdmin)

	agent, err := h.dialAgent("kmg_valid", okResponse("{}"))
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	h.proxyRequest(http.MethodGet,
		"/api/v1/clusters/1/proxy/api/v1/pods?labelSelector=app%3Dweb&limit=50", h.token(admin))

	seen := agent.requests()
	if len(seen) != 1 || !strings.Contains(seen[0].Path, "labelSelector=app%3Dweb") {
		t.Fatalf("the query string did not survive the hop: %+v", seen)
	}
}

func TestProxyEnforcesTheNamespaceScope(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	user := h.addUser(10, "devops", db.SystemRoleUser)
	h.grant(10, 1, db.K8sRoleView, []string{"team-a"})

	agent, err := h.dialAgent("kmg_valid", okResponse("{}"))
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	resp := h.proxyRequest(http.MethodGet,
		"/api/v1/clusters/1/proxy/api/v1/namespaces/kube-system/secrets", h.token(user))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
	if len(agent.requests()) != 0 {
		t.Fatal("a refused request must never reach the cluster")
	}
}

func TestProxyRefusesAClusterTheUserCannotSee(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	user := h.addUser(10, "devops", db.SystemRoleUser)

	if _, err := h.dialAgent("kmg_valid", okResponse("{}")); err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	resp := h.proxyRequest(http.MethodGet,
		"/api/v1/clusters/1/proxy/api/v1/pods", h.token(user))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for an ungranted cluster, got %d", resp.StatusCode)
	}
}

func TestProxyReportsAMissingTunnel(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	admin := h.addUser(10, "admin", db.SystemRoleAdmin)

	resp := h.proxyRequest(http.MethodGet,
		"/api/v1/clusters/1/proxy/api/v1/pods", h.token(admin))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when no agent is attached, got %d", resp.StatusCode)
	}
}

func TestProxyRequiresAuthentication(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")

	resp := h.proxyRequest(http.MethodGet, "/api/v1/clusters/1/proxy/api/v1/pods", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestProxyRefusesPortForward(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	admin := h.addUser(10, "admin", db.SystemRoleAdmin)

	if _, err := h.dialAgent("kmg_valid", okResponse("{}")); err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	resp := h.proxyRequest(http.MethodGet,
		"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods/web-0/portforward", h.token(admin))
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("expected 501 for port-forward, got %d", resp.StatusCode)
	}
}

// echoStream answers a stream_open by emitting the given chunks and closing.
// It stands in for a watch or a followed log.
func echoStream(chunks ...string) func(*fakeAgent, string, StreamOpen) {
	return func(a *fakeAgent, id string, _ StreamOpen) {
		_ = a.send(Message{Type: MessageStreamStart, ID: id, StreamStart: &StreamStart{
			Status: http.StatusOK,
			Header: map[string][]string{"Content-Type": {"application/json"}},
		}})
		for _, chunk := range chunks {
			_ = a.send(Message{Type: MessageStreamData, ID: id,
				StreamData: &StreamData{Data: []byte(chunk)}})
		}
		_ = a.send(Message{Type: MessageStreamClose, ID: id, StreamClose: &StreamClose{}})
	}
}

func TestProxyStreamsAWatch(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	admin := h.addUser(10, "admin", db.SystemRoleAdmin)

	agent, err := h.dialStreamingAgent("kmg_valid", okResponse("{}"),
		echoStream(`{"type":"ADDED"}`+"\n", `{"type":"MODIFIED"}`+"\n"))
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	resp := h.proxyRequest(http.MethodGet,
		"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods?watch=true", h.token(admin))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read streamed body: %v", err)
	}
	if got := string(body); got != `{"type":"ADDED"}`+"\n"+`{"type":"MODIFIED"}`+"\n" {
		t.Fatalf("streamed body did not arrive intact: %q", got)
	}

	// A watch must go down the stream path, not be flattened into a request.
	if opens := agent.streamOpens(); len(opens) != 1 {
		t.Fatalf("expected one stream open, got %d", len(opens))
	} else if opens[0].Upgrade {
		t.Fatal("a watch is one-way and must not ask for an upgrade")
	}
	if len(agent.requests()) != 0 {
		t.Fatal("a watch must not be sent as a request/response pair")
	}
}

func TestProxyStreamsFollowedLogs(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	admin := h.addUser(10, "admin", db.SystemRoleAdmin)

	agent, err := h.dialStreamingAgent("kmg_valid", okResponse("{}"),
		echoStream("line one\n", "line two\n"))
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	resp := h.proxyRequest(http.MethodGet,
		"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods/web-0/log?follow=true",
		h.token(admin))
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "line one\nline two\n" {
		t.Fatalf("log stream did not arrive intact: %q", body)
	}
	if opens := agent.streamOpens(); len(opens) != 1 {
		t.Fatalf("expected one stream open, got %d", len(opens))
	}
}

func TestProxyStreamCarriesImpersonation(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	user := h.addUser(10, "devops", db.SystemRoleUser)
	h.grant(10, 1, db.K8sRoleView, nil)

	agent, err := h.dialStreamingAgent("kmg_valid", okResponse("{}"), echoStream("x"))
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	resp := h.proxyRequest(http.MethodGet,
		"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods?watch=true", h.token(user))
	_, _ = io.ReadAll(resp.Body)

	opens := agent.streamOpens()
	if len(opens) != 1 {
		t.Fatalf("expected one stream open, got %d", len(opens))
	}
	// A stream must be identified exactly as a plain call is; anything less
	// would be a hole in the audit and impersonation story.
	if got := opens[0].Header["Impersonate-User"]; len(got) != 1 || got[0] != "devops" {
		t.Fatalf("Impersonate-User on the stream = %v", got)
	}
	if got := opens[0].Header["Impersonate-Group"]; len(got) == 0 || got[0] != "kubemg:view" {
		t.Fatalf("Impersonate-Group on the stream = %v", got)
	}
}

func TestProxyStreamHonoursTheNamespaceScope(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	user := h.addUser(10, "devops", db.SystemRoleUser)
	h.grant(10, 1, db.K8sRoleView, []string{"team-a"})

	agent, err := h.dialStreamingAgent("kmg_valid", okResponse("{}"), echoStream("x"))
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	resp := h.proxyRequest(http.MethodGet,
		"/api/v1/clusters/1/proxy/api/v1/namespaces/kube-system/pods?watch=true", h.token(user))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
	if len(agent.streamOpens()) != 0 {
		t.Fatal("a refused watch must never open a stream on the cluster")
	}
}

func TestProxyExecPipesBothDirections(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	admin := h.addUser(10, "admin", db.SystemRoleAdmin)

	// Stand in for a shell: announce the upgrade, then echo whatever is typed.
	session := func(a *fakeAgent, id string, open StreamOpen) {
		subprotocol := ""
		if len(open.Subprotocols) > 0 {
			subprotocol = open.Subprotocols[0]
		}
		_ = a.send(Message{Type: MessageStreamStart, ID: id, StreamStart: &StreamStart{
			Status:      http.StatusSwitchingProtocols,
			Subprotocol: subprotocol,
		}})
		_ = a.send(Message{Type: MessageStreamData, ID: id,
			StreamData: &StreamData{Data: []byte{1, '$', ' '}, Binary: true}})
	}

	agent, err := h.dialStreamingAgent("kmg_valid", okResponse("{}"), session)
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	url := "ws" + strings.TrimPrefix(h.server.URL, "http") +
		"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods/web-0/exec?command=sh&stdin=true"
	header := http.Header{}
	header.Set("Authorization", "Bearer "+h.token(admin))

	dialer := *websocket.DefaultDialer
	dialer.Subprotocols = ChannelSubprotocols

	conn, resp, err := dialer.Dial(url, header)
	if err != nil {
		status := ""
		if resp != nil {
			status = resp.Status
		}
		t.Fatalf("exec upgrade failed: %v (%s)", err, status)
	}
	defer conn.Close()

	// The negotiated channel protocol has to survive both hops, or the client
	// frames stdin on a channel the API server is not reading.
	if conn.Subprotocol() != "v5.channel.k8s.io" {
		t.Fatalf("subprotocol = %q, want the one the agent agreed", conn.Subprotocol())
	}

	kind, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read from session: %v", err)
	}
	if kind != websocket.BinaryMessage {
		t.Fatal("the channel protocol is binary; a text frame would corrupt it")
	}
	if len(payload) != 3 || payload[0] != 1 {
		t.Fatalf("unexpected session output: %v", payload)
	}

	// Now the other direction: what the user types must reach the agent.
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte{0, 'l', 's', '\n'}); err != nil {
		t.Fatalf("write to session: %v", err)
	}
	waitFor(t, func() bool { return len(agent.streamReceived()) > 0 })

	got := agent.streamReceived()[0]
	if !got.Binary || string(got.Data) != string([]byte{0, 'l', 's', '\n'}) {
		t.Fatalf("keystrokes did not reach the cluster intact: %+v", got)
	}
	if opens := agent.streamOpens(); len(opens) != 1 || !opens[0].Upgrade {
		t.Fatalf("exec must open an upgrade stream, got %+v", opens)
	}
}

func TestProxyStreamAuditsOpenAndClose(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	admin := h.addUser(10, "admin", db.SystemRoleAdmin)

	if _, err := h.dialStreamingAgent("kmg_valid", okResponse("{}"), echoStream("a", "b")); err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	resp := h.proxyRequest(http.MethodGet,
		"/api/v1/clusters/1/proxy/api/v1/namespaces/team-a/pods?watch=true", h.token(admin))
	_, _ = io.ReadAll(resp.Body)

	// A session that runs for an hour must not be invisible until it stops, so
	// it is recorded when it opens and again when it ends.
	waitFor(t, func() bool { return len(h.audit.events()) >= 2 })

	events := h.audit.events()
	var opened, closed *Event
	for i := range events {
		switch events[i].Phase {
		case PhaseOpen:
			opened = &events[i]
		case PhaseClose:
			closed = &events[i]
		}
	}
	if opened == nil || closed == nil {
		t.Fatalf("expected an open and a close record, got %+v", events)
	}
	if !opened.Streaming || opened.Verb != "watch" {
		t.Fatalf("the opening record should name the streaming verb: %+v", opened)
	}
	if closed.BytesOut != 2 {
		t.Fatalf("the closing record should carry the bytes served, got %d", closed.BytesOut)
	}
}

func TestSecondAgentDisplacesTheFirst(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")

	first, err := h.dialAgent("kmg_valid", okResponse("{}"))
	if err != nil {
		t.Fatalf("dial first agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	if _, err := h.dialAgent("kmg_valid", okResponse("{}")); err != nil {
		t.Fatalf("dial second agent: %v", err)
	}

	// The displaced agent's socket is closed, but the cluster stays connected:
	// a rolling deployment must not flap the cluster's health.
	select {
	case <-first.done:
	case <-time.After(3 * time.Second):
		t.Fatal("the displaced agent was not hung up on")
	}
	if !h.gateway.Registry().Connected(1) {
		t.Fatal("replacing an agent must not mark the cluster unreachable")
	}
	for _, state := range h.store.recordedStates() {
		if !state.Connected {
			t.Fatal("an agent rollover must not record a disconnect")
		}
	}
}

// waitFor polls until the condition holds, failing the test if it never does.
// The tunnel is registered by a goroutine, so the assertion cannot be immediate.
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the tunnel to settle")
}
