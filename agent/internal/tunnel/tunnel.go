// Package tunnel holds the agent's outbound connection to the KubeMG bastion.
//
// Everything here dials *out*. The agent opens no port to the network, so
// installing it needs no firewall change, no ingress and no public API server —
// which is the whole reason the architecture is shaped this way.
package tunnel

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kubemg/kubemg/agent/internal/kube"
	"github.com/kubemg/kubemg/agent/internal/protocol"
)

// Connection lifetimes, matched to the server's side of the protocol.
const (
	writeTimeout = 10 * time.Second
	// readTimeout must exceed the server's ping interval, or a healthy but idle
	// tunnel would tear itself down.
	readTimeout = 90 * time.Second
	maxFrame    = 16 << 20
	// handshakeTimeout bounds the dial and the welcome.
	handshakeTimeout = 20 * time.Second
)

// Reconnect backoff. A bastion restart should not stampede every agent in the
// fleet, so the delay grows and carries jitter.
const (
	minBackoff = 1 * time.Second
	maxBackoff = 60 * time.Second
)

// Client maintains one tunnel to the bastion, reconnecting for the life of the
// process.
type Client struct {
	bastionURL string
	token      string
	version    string
	kube       *kube.Client
	logger     *slog.Logger
	tlsConfig  *tls.Config

	// streams tracks the long-lived calls this agent is servicing.
	streams *streamTable

	// connected drives the readiness probe: a pod whose tunnel is down should
	// not report itself ready.
	connected atomic.Bool
	// cluster is the name the bastion bound this tunnel to, for logging.
	mu      sync.Mutex
	cluster string
}

// Options configure the tunnel client.
type Options struct {
	// BastionURL is the KubeMG server's public URL, http(s) — the scheme is
	// swapped to ws(s) when dialling.
	BastionURL string
	// Token is the cluster registration secret.
	Token   string
	Version string
	Kube    *kube.Client
	Logger  *slog.Logger
	// CAPEM is a certificate the bastion is trusted on in addition to the
	// system roots. An on-prem KubeMG often serves a certificate no public CA
	// vouches for; pinning it here is the answer, rather than an
	// insecure-skip-verify switch someone forgets to turn off.
	CAPEM string
	// InsecureSkipVerify drops certificate verification entirely. It exists for
	// running the agent by hand against a development bastion, and it is
	// logged loudly because it makes the tunnel trivially interceptable.
	InsecureSkipVerify bool
}

// New builds a tunnel client.
func New(opts Options) (*Client, error) {
	if strings.TrimSpace(opts.BastionURL) == "" {
		return nil, errors.New("KUBEMG_BASTION_URL is required")
	}
	if strings.TrimSpace(opts.Token) == "" {
		return nil, errors.New("KUBEMG_CLUSTER_TOKEN is required")
	}
	if opts.Kube == nil {
		return nil, errors.New("a kubernetes client is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	tlsConfig, err := bastionTLS(opts.CAPEM, opts.InsecureSkipVerify)
	if err != nil {
		return nil, err
	}
	if opts.InsecureSkipVerify {
		opts.Logger.Warn("bastion certificate verification is disabled; the tunnel can be intercepted")
	}

	return &Client{
		bastionURL: strings.TrimRight(strings.TrimSpace(opts.BastionURL), "/"),
		token:      strings.TrimSpace(opts.Token),
		version:    opts.Version,
		kube:       opts.Kube,
		logger:     opts.Logger,
		tlsConfig:  tlsConfig,
		streams:    newStreamTable(),
	}, nil
}

// bastionTLS builds the trust the tunnel dials with. An empty CA and no
// override means the system roots, which is what a bastion behind a real
// certificate needs and nothing more.
func bastionTLS(caPEM string, insecure bool) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure}
	caPEM = strings.TrimSpace(caPEM)
	if caPEM == "" {
		return cfg, nil
	}

	// Start from the system roots rather than replacing them: a deployment can
	// pin an internal CA without losing the ability to dial a bastion that
	// later moves behind a public one.
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM([]byte(caPEM + "\n")) {
		return nil, errors.New("KUBEMG_BASTION_CA does not contain a PEM certificate")
	}
	cfg.RootCAs = pool
	return cfg, nil
}

// Connected reports whether the tunnel is currently up.
func (c *Client) Connected() bool { return c.connected.Load() }

// Run dials the bastion and keeps redialling until the context is cancelled.
// A dropped tunnel is normal operation — a node drain, a bastion deploy, a
// load balancer idle timeout — so it is never fatal.
func (c *Client) Run(ctx context.Context) error {
	backoff := minBackoff

	for {
		err := c.connectOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err != nil {
			c.logger.Warn("tunnel closed", slog.String("error", err.Error()))
		} else {
			c.logger.Info("tunnel closed by the bastion")
			// A clean close usually means the bastion is redeploying, so it is
			// worth trying again promptly rather than waiting out a backoff
			// earned by earlier failures.
			backoff = minBackoff
		}

		delay := jitter(backoff)
		c.logger.Info("reconnecting", slog.Duration("in", delay))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// connectOnce holds one tunnel from dial to close.
func (c *Client) connectOnce(ctx context.Context) error {
	endpoint, err := c.wsURL()
	if err != nil {
		return err
	}

	dialer := &websocket.Dialer{
		HandshakeTimeout: handshakeTimeout,
		TLSClientConfig:  c.tlsConfig,
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.token)
	header.Set("User-Agent", "kubemg-agent/"+c.version)

	conn, resp, err := dialer.DialContext(ctx, endpoint, header)
	if err != nil {
		if resp != nil {
			// The bastion's refusals are actionable — a revoked cluster, a
			// wrong token — so surface the status rather than "bad handshake".
			return fmt.Errorf("dial bastion: %w (%s)", err, resp.Status)
		}
		return fmt.Errorf("dial bastion: %w", err)
	}
	defer conn.Close()

	// The version is best-effort: an agent whose RBAC cannot read /version
	// should still form a tunnel.
	kubernetesVersion, err := c.kube.ServerVersion()
	if err != nil {
		c.logger.Warn("could not read the cluster version", slog.String("error", err.Error()))
	}

	hello := protocol.Message{Type: protocol.MessageHello, Hello: &protocol.Hello{
		ProtocolVersion:   protocol.ProtocolVersion,
		AgentVersion:      c.version,
		KubernetesVersion: kubernetesVersion,
	}}
	if err := writeMessage(conn, hello); err != nil {
		return fmt.Errorf("send handshake: %w", err)
	}

	welcome, err := readWelcome(conn)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.cluster = welcome.ClusterName
	c.mu.Unlock()
	c.connected.Store(true)
	defer c.connected.Store(false)

	c.logger.Info("tunnel established",
		slog.String("cluster", welcome.ClusterName),
		slog.Uint64("cluster_id", uint64(welcome.ClusterID)),
		slog.String("bastion", c.bastionURL),
	)

	return c.pump(ctx, conn)
}

// pump serves proxied requests until the socket dies. Each request is handled
// in its own goroutine so a slow API call cannot stall the tunnel, and writes
// are serialised because gorilla permits one writer at a time.
func (c *Client) pump(ctx context.Context, conn *websocket.Conn) error {
	conn.SetReadLimit(maxFrame)
	if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return err
	}
	conn.SetPingHandler(func(payload string) error {
		if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
			return err
		}
		return conn.WriteControl(websocket.PongMessage, []byte(payload), time.Now().Add(writeTimeout))
	})

	out := &writer{conn: conn, logger: c.logger}
	var wg sync.WaitGroup

	// Streams outlive individual frames, so they are torn down before the
	// waitgroup is drained or their goroutines would never notice the socket
	// went away.
	defer wg.Wait()
	defer c.streams.closeAll()

	// Cancelling on return stops in-flight handlers from outliving the socket.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil
			}
			return err
		}

		var msg protocol.Message
		if err := json.Unmarshal(payload, &msg); err != nil {
			c.logger.Warn("ignoring malformed frame", slog.String("error", err.Error()))
			continue
		}

		switch msg.Type {
		case protocol.MessageRequest:
			if msg.Request == nil {
				continue
			}
			wg.Add(1)
			go func(id string, req protocol.Request) {
				defer wg.Done()

				resp := c.forward(ctx, req)
				if err := out.send(protocol.Message{
					Type: protocol.MessageResponse, ID: id, Response: &resp,
				}); err != nil {
					c.logger.Warn("could not answer a proxied request",
						slog.String("error", err.Error()))
				}
			}(msg.ID, *msg.Request)

		case protocol.MessageStreamOpen:
			if msg.StreamOpen == nil {
				continue
			}
			wg.Add(1)
			go func(id string, open protocol.StreamOpen) {
				defer wg.Done()
				c.openStream(ctx, id, open, out)
			}(msg.ID, *msg.StreamOpen)

		case protocol.MessageStreamData:
			if msg.StreamData == nil {
				continue
			}
			if s, ok := c.streams.get(msg.ID); ok {
				// Never blocks the read loop: a session that cannot keep up
				// with its own input loses itself, not the whole tunnel.
				s.push(*msg.StreamData)
			}

		case protocol.MessageStreamClose:
			if s, ok := c.streams.get(msg.ID); ok {
				s.close()
			}
		}
	}
}

// forward replays one request against the local API server. Failures come back
// as a Response carrying Error rather than as a dropped tunnel: the bastion
// needs to tell the client what went wrong, and the tunnel is still fine.
func (c *Client) forward(ctx context.Context, req protocol.Request) protocol.Response {
	var body *bytes.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	} else {
		body = bytes.NewReader(nil)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, c.kube.URL(req.Path), body)
	if err != nil {
		return protocol.Response{Error: "could not build the upstream request: " + err.Error()}
	}
	for name, values := range req.Header {
		for _, value := range values {
			httpReq.Header.Add(name, value)
		}
	}

	resp, err := c.kube.Do(httpReq)
	if err != nil {
		return protocol.Response{Error: err.Error()}
	}

	payload, err := kube.ReadBody(resp.Body)
	if err != nil {
		return protocol.Response{Error: err.Error()}
	}

	return protocol.Response{
		Status: resp.StatusCode,
		Header: resp.Header,
		Body:   payload,
	}
}

// wsURL turns the configured bastion URL into the tunnel endpoint.
func (c *Client) wsURL() (string, error) {
	parsed, err := url.Parse(c.bastionURL)
	if err != nil {
		return "", fmt.Errorf("KUBEMG_BASTION_URL is not a URL: %w", err)
	}

	switch parsed.Scheme {
	case "https", "wss":
		parsed.Scheme = "wss"
	case "http", "ws":
		parsed.Scheme = "ws"
	default:
		return "", fmt.Errorf("KUBEMG_BASTION_URL has an unsupported scheme %q", parsed.Scheme)
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/agent/v1/tunnel"
	return parsed.String(), nil
}

func readWelcome(conn *websocket.Conn) (protocol.Welcome, error) {
	if err := conn.SetReadDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		return protocol.Welcome{}, err
	}

	_, payload, err := conn.ReadMessage()
	if err != nil {
		return protocol.Welcome{}, fmt.Errorf("bastion did not answer the handshake: %w", err)
	}

	var msg protocol.Message
	if err := json.Unmarshal(payload, &msg); err != nil {
		return protocol.Welcome{}, fmt.Errorf("bastion sent a malformed welcome: %w", err)
	}
	if msg.Type != protocol.MessageWelcome || msg.Welcome == nil {
		return protocol.Welcome{}, fmt.Errorf("expected a welcome frame, got %q", msg.Type)
	}
	if msg.Welcome.ProtocolVersion != protocol.ProtocolVersion {
		return protocol.Welcome{}, fmt.Errorf(
			"bastion speaks tunnel protocol v%d, this agent speaks v%d — upgrade the agent",
			msg.Welcome.ProtocolVersion, protocol.ProtocolVersion,
		)
	}
	return *msg.Welcome, nil
}

func writeMessage(conn *websocket.Conn, msg protocol.Message) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, payload)
}

// jitter spreads reconnects across the fleet so a bastion coming back up is not
// hit by every agent at once.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return minBackoff
	}
	return d/2 + time.Duration(rand.Int63n(int64(d)))
}
