package bastion

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// handshakeTimeout bounds how long an agent may take to send its hello after
// the upgrade, so a half-open connection cannot occupy a slot indefinitely.
const handshakeTimeout = 15 * time.Second

// stateTimeout bounds the health writes that bracket a tunnel's life. They run
// on a fresh context because the request context of a hijacked connection is
// no longer meaningful.
const stateTimeout = 5 * time.Second

// Store is the persistence the bastion needs: enough to authenticate an agent
// and to record the tunnel coming and going.
type Store interface {
	ClusterByAgentToken(ctx context.Context, token string) (*db.Cluster, error)
	UpdateClusterAgent(ctx context.Context, id uint, state db.AgentState) error
}

// Server accepts agent tunnels and hands them to the proxy. It owns no HTTP
// listener of its own: the handlers mount on KubeMG's existing router, so
// agents, the API and the UI all arrive on the same port 443.
type Server struct {
	store    Store
	registry *Registry
	logger   *slog.Logger
	upgrader websocket.Upgrader
}

// ServerOptions wires the tunnel listener.
type ServerOptions struct {
	Store Store
	// Registry is shared with the proxy. One is created when omitted.
	Registry *Registry
	Logger   *slog.Logger
}

// NewServer builds the tunnel listener.
func NewServer(opts ServerOptions) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	registry := opts.Registry
	if registry == nil {
		registry = NewRegistry()
	}

	return &Server{
		store:    opts.Store,
		registry: registry,
		logger:   logger,
		upgrader: websocket.Upgrader{
			HandshakeTimeout: handshakeTimeout,
			ReadBufferSize:   32 << 10,
			WriteBufferSize:  32 << 10,
			// Agents are programs, not browsers: there is no origin to police,
			// and the bearer token is what actually authenticates the peer.
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
}

// Registry exposes the connection pool so the proxy and the API can ask which
// clusters are attached.
func (s *Server) Registry() *Registry { return s.registry }

// HandleAgent upgrades an agent's outbound connection into a tunnel and serves
// it until it drops. This is the only inbound-facing part of the architecture,
// and it is still the *agent* that dialled.
func (s *Server) HandleAgent(c *gin.Context) {
	token := bearerToken(c.Request)
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "agent registration token is required"})
		return
	}

	ctx := c.Request.Context()
	cluster, err := s.store.ClusterByAgentToken(ctx, token)
	if errors.Is(err, db.ErrNotFound) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unknown agent registration token"})
		return
	}
	if err != nil {
		s.logger.Error("agent token lookup failed", slog.String("error", err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not verify the registration token"})
		return
	}
	// The lookup already matched, but a case-insensitive column collation would
	// match a token that is not byte-identical. Settle it in constant time.
	if !SameToken(cluster.AgentToken, token) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unknown agent registration token"})
		return
	}
	if !cluster.UsesAgent() {
		c.JSON(http.StatusConflict, gin.H{
			"error": "this cluster is registered for direct API access, not for an agent",
		})
		return
	}

	conn, err := s.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		// Upgrade already wrote its own error response.
		return
	}

	hello, err := readHello(conn)
	if err != nil {
		s.logger.Warn("agent handshake failed",
			slog.String("cluster", cluster.Name),
			slog.String("error", err.Error()),
		)
		closeWith(conn, websocket.ClosePolicyViolation, err.Error())
		return
	}

	s.serveTunnel(conn, cluster, hello)
}

// serveTunnel registers the tunnel, marks the cluster reachable, and blocks
// until the agent goes away.
func (s *Server) serveTunnel(conn *websocket.Conn, cluster *db.Cluster, hello Hello) {
	tunnel := newTunnel(conn, cluster.ID, cluster.Name, hello)

	if displaced := s.registry.Add(tunnel); displaced != nil {
		// A rolling agent deployment: the new pod takes over and the old one is
		// hung up on. No health write here — the cluster stays connected.
		s.logger.Info("replacing agent tunnel",
			slog.String("cluster", cluster.Name),
			slog.String("agent_version", hello.AgentVersion),
		)
		displaced.Close()
	}

	s.recordState(cluster, db.AgentState{
		Connected:         true,
		AgentVersion:      hello.AgentVersion,
		KubernetesVersion: hello.KubernetesVersion,
		At:                time.Now().UTC(),
	})
	s.logger.Info("agent tunnel established",
		slog.String("cluster", cluster.Name),
		slog.Uint64("cluster_id", uint64(cluster.ID)),
		slog.String("agent_version", hello.AgentVersion),
		slog.String("kubernetes_version", hello.KubernetesVersion),
	)

	welcome := Message{Type: MessageWelcome, Welcome: &Welcome{
		ProtocolVersion:  ProtocolVersion,
		ClusterID:        cluster.ID,
		ClusterName:      cluster.Name,
		HeartbeatSeconds: int(heartbeatInterval.Seconds()),
	}}
	if err := tunnel.send(welcome); err != nil {
		s.dropTunnel(tunnel, cluster, err)
		return
	}

	err := tunnel.serve()
	s.dropTunnel(tunnel, cluster, err)
}

// dropTunnel deregisters a tunnel and marks the cluster unreachable, but only
// if this tunnel is still the live one — a displaced agent must not report its
// replacement as down.
func (s *Server) dropTunnel(tunnel *Tunnel, cluster *db.Cluster, cause error) {
	tunnel.Close()
	if !s.registry.Remove(tunnel) {
		return
	}

	message := "the in-cluster agent disconnected"
	if cause != nil && !websocket.IsCloseError(cause, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		message = "the in-cluster agent tunnel dropped unexpectedly"
	}

	s.recordState(cluster, db.AgentState{
		Connected:     false,
		StatusMessage: message,
		At:            time.Now().UTC(),
	})
	s.logger.Info("agent tunnel closed",
		slog.String("cluster", cluster.Name),
		slog.Uint64("cluster_id", uint64(cluster.ID)),
	)
}

func (s *Server) recordState(cluster *db.Cluster, state db.AgentState) {
	ctx, cancel := context.WithTimeout(context.Background(), stateTimeout)
	defer cancel()

	if err := s.store.UpdateClusterAgent(ctx, cluster.ID, state); err != nil {
		s.logger.Error("could not record agent tunnel state",
			slog.String("cluster", cluster.Name),
			slog.Bool("connected", state.Connected),
			slog.String("error", err.Error()),
		)
	}
}

// readHello consumes the agent's opening frame and checks it speaks a protocol
// version this server understands.
func readHello(conn *websocket.Conn) (Hello, error) {
	conn.SetReadLimit(maxFrame)
	if err := conn.SetReadDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return Hello{}, err
	}

	_, payload, err := conn.ReadMessage()
	if err != nil {
		return Hello{}, errors.New("agent sent no handshake")
	}

	var msg Message
	if err := json.Unmarshal(payload, &msg); err != nil {
		return Hello{}, errors.New("agent handshake is not valid JSON")
	}
	if msg.Type != MessageHello || msg.Hello == nil {
		return Hello{}, errors.New("agent did not open with a hello frame")
	}
	if msg.Hello.ProtocolVersion != ProtocolVersion {
		return Hello{}, errors.New("agent speaks an unsupported tunnel protocol version")
	}
	return *msg.Hello, nil
}

// closeWith tells the agent why it is being hung up on, so a misconfigured
// installation reports something better than "connection reset" in its logs.
func closeWith(conn *websocket.Conn, code int, reason string) {
	frame := websocket.FormatCloseMessage(code, reason)
	_ = conn.WriteControl(websocket.CloseMessage, frame, time.Now().Add(writeTimeout))
	_ = conn.Close()
}

// bearerToken pulls the agent's credential off the upgrade request.
func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if header == "" {
		return ""
	}
	scheme, value, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(value)
}
