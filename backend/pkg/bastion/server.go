package bastion

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
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

// credentialSweepInterval is how often every live tunnel's credential is
// re-checked against the database. A rotation closes the tunnel it can see at
// once; this is what closes one held by another replica, or one whose cluster
// was deleted, without waiting for the agent to reconnect of its own accord.
const credentialSweepInterval = 30 * time.Second

// Verbs KubeMG records about an agent's own connection. Neither is a Kubernetes
// verb and neither is suppressible — see auditpolicy.
const (
	// VerbAgentDisplaced is a live tunnel being replaced by a newer connection
	// presenting the same credential. A rolling agent Deployment does this for a
	// moment on every upgrade; somebody holding a leaked token does it too, and
	// the record's addresses and versions are what tell the two apart.
	VerbAgentDisplaced = "agent-displaced"
	// VerbAgentTokenRotate is an administrator replacing a cluster's tunnel
	// credential. It is written by the API, and named here beside its sibling.
	VerbAgentTokenRotate = "agent-token-rotate"
)

// AgentActor is the user named on a record the agent's own connection caused.
// The colon makes it a name no stored account can hold (db.CheckUsername
// refuses one), so it cannot be mistaken for a person.
const AgentActor = "kubemg:agent"

// ErrCredentialRetired closes a tunnel whose registration token no longer
// resolves to its cluster: rotated, or the cluster removed.
var ErrCredentialRetired = errors.New("the agent's registration token was rotated or withdrawn")

// retiredReason is what the agent is told on the way out, in its own logs.
// WebSocket close reasons are capped at 123 bytes.
const retiredReason = "registration token rotated: re-apply the agent install package"

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
	// auditor receives the records the tunnel listener writes about itself —
	// today a displacement. Nil records nothing, which is what most tests want.
	auditor Auditor
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

// UseAuditor installs the audit writer. The gateway is built before the
// writer in main (the writer's consumers need the gateway's registry), so it is
// handed over afterwards; call it before the router serves anything.
func (s *Server) UseAuditor(auditor Auditor) { s.auditor = auditor }

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

	source := RequestSource{Addr: c.ClientIP(), UserAgent: c.Request.UserAgent()}.Truncate()
	s.serveTunnel(conn, cluster, hello, source, token)
}

// serveTunnel registers the tunnel, marks the cluster reachable, and blocks
// until the agent goes away.
func (s *Server) serveTunnel(
	conn *websocket.Conn, cluster *db.Cluster, hello Hello, source RequestSource, credential string,
) {
	tunnel := newTunnel(conn, cluster.ID, cluster.Name, hello)
	tunnel.SourceAddr = source.Addr
	tunnel.credential = credential

	if displaced := s.registry.Add(tunnel); displaced != nil {
		// Newest wins: a rolling agent deployment briefly has two pods dialling
		// in, and the new one must take over. No health write here — the cluster
		// stays connected. But it is recorded, every time: a second party holding
		// this cluster's token takes the tunnel exactly the same way, and "the
		// agent reconnected" and "somebody else is now the agent" must be
		// distinguishable afterwards. Suppressing the rollover case would need a
		// way to tell the two apart, and there is none that an impostor cannot
		// imitate — so both are recorded truthfully and the reader decides.
		s.logger.Warn("agent tunnel displaced by a newer connection",
			slog.String("cluster", cluster.Name),
			slog.String("agent_version", hello.AgentVersion),
			slog.String("source", source.Addr),
			slog.String("previous_source", displaced.SourceAddr),
			slog.String("previous_agent_version", displaced.AgentVersion),
		)
		displaced.Close()
		s.recordDisplacement(cluster, displaced, tunnel, source)
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
	if errors.Is(tunnel.closeErr, ErrCredentialRetired) {
		message = "the agent's registration token was rotated; re-apply the install package to reconnect"
	} else if cause != nil && !websocket.IsCloseError(cause, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
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

// recordDisplacement writes the audit record for a tunnel being replaced. The
// new connection's address and user agent ride on the context the way every
// other record's do; what has no column of its own — both agents' versions,
// both connection times, the previous address — goes in the path's query, which
// is what the trail, the SIEM forward and an alarm all already carry.
func (s *Server) recordDisplacement(cluster *db.Cluster, previous, next *Tunnel, source RequestSource) {
	if s.auditor == nil {
		return
	}
	query := url.Values{}
	query.Set("agent_version", next.AgentVersion)
	query.Set("connected_at", next.ConnectedAt.Format(time.RFC3339))
	// Also in the source_addr column; repeated here so an alarm, which carries
	// the path but not the column, names who took the tunnel.
	query.Set("source", source.Addr)
	query.Set("previous_source", previous.SourceAddr)
	query.Set("previous_agent_version", previous.AgentVersion)
	query.Set("previous_connected_at", previous.ConnectedAt.Format(time.RFC3339))

	s.auditor.Record(WithSource(context.Background(), source), Event{
		At:        next.ConnectedAt,
		Username:  AgentActor,
		ClusterID: cluster.ID,
		Cluster:   cluster.Name,
		Verb:      VerbAgentDisplaced,
		Method:    http.MethodGet,
		Path:      "/agent/v1/tunnel?" + query.Encode(),
		Resource:  "agent",
		Status:    http.StatusOK,
		// How long the connection that lost had been up. A pod rolled by its own
		// Deployment has usually been up for days; a displacement seconds after
		// a reconnect is worth a second look.
		Duration: next.ConnectedAt.Sub(previous.ConnectedAt),
	})
}

// Retire closes the tunnel attached for a cluster, telling the agent why. It is
// what makes a rotation take effect now rather than at the agent's next
// reconnect: the handshake already refuses the old token, but a tunnel that is
// already up never handshakes again. It reports whether a tunnel was attached
// here — another replica's is closed by RunCredentialSweep.
func (s *Server) Retire(clusterID uint) bool {
	tunnel, ok := s.registry.Get(clusterID)
	if !ok {
		return false
	}
	s.retire(tunnel)
	return true
}

func (s *Server) retire(tunnel *Tunnel) {
	frame := websocket.FormatCloseMessage(websocket.ClosePolicyViolation, retiredReason)
	_ = tunnel.conn.WriteControl(websocket.CloseMessage, frame, time.Now().Add(writeTimeout))
	tunnel.closeWith(ErrCredentialRetired)
}

// RunCredentialSweep re-checks every live tunnel's credential on a tick until
// ctx is done, closing any whose token no longer resolves to its cluster.
func (s *Server) RunCredentialSweep(ctx context.Context) {
	ticker := time.NewTicker(credentialSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.SweepCredentials(ctx)
		}
	}
}

// SweepCredentials is one pass of RunCredentialSweep. A store that cannot be
// read closes nothing: a database blip must not take the whole fleet's tunnels
// down, and the handshake still refuses a retired token on every reconnect.
func (s *Server) SweepCredentials(ctx context.Context) {
	for _, tunnel := range s.registry.Snapshot() {
		lookup, cancel := context.WithTimeout(ctx, stateTimeout)
		cluster, err := s.store.ClusterByAgentToken(lookup, tunnel.credential)
		cancel()
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			s.logger.Error("could not re-check an agent tunnel's credential",
				slog.String("cluster", tunnel.ClusterName),
				slog.String("error", err.Error()),
			)
			continue
		}
		if err == nil && cluster.ID == tunnel.ClusterID && cluster.UsesAgent() &&
			SameToken(cluster.AgentToken, tunnel.credential) {
			continue
		}
		s.logger.Warn("closing an agent tunnel whose registration token was retired",
			slog.String("cluster", tunnel.ClusterName),
			slog.Uint64("cluster_id", uint64(tunnel.ClusterID)),
		)
		s.retire(tunnel)
	}
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
