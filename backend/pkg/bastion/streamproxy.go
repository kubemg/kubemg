package bastion

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// newClientUpgrader builds the upgrader that accepts the browser terminal's
// WebSocket, and kubectl's for an exec or a port-forward.
//
// It deliberately declares no Subprotocols of its own: the subprotocol the
// client is told is the one the *cluster* agreed to, echoed through the
// response header. Letting the upgrader choose independently would let this end
// answer v5 while the API server is speaking v4, and the two framings differ.
//
// CheckOrigin is built per-Proxy rather than shared as a package var, because
// only the proxy knows this server's configured origins. The caller is already
// authenticated by JWT before reaching here, so origin is not the primary
// control — but leaving it unchecked drops a defense-in-depth layer that
// matters most precisely when the JWT arrived over a query string (see
// auth.QueryTokenParam) rather than a header. A request with no Origin header
// at all is let through unconditionally: every non-browser caller (kubectl
// exec, kubectl port-forward) sends none, only a browser does on a
// cross-origin upgrade.
func newClientUpgrader(allowedOrigins []string) websocket.Upgrader {
	return websocket.Upgrader{
		HandshakeTimeout: 15 * time.Second,
		ReadBufferSize:   32 << 10,
		WriteBufferSize:  32 << 10,
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true
			}
			if len(allowedOrigins) == 0 {
				// Nothing configured to check against — the state every caller
				// before this option existed, and every test env not wiring one,
				// is in.
				return true
			}
			return originAllowed(allowedOrigins, origin)
		},
	}
}

// ChannelSubprotocols are the Kubernetes exec/attach channel protocols, newest
// first. v5 adds a close signal on stdin; v4 is what every current API server
// speaks.
var ChannelSubprotocols = []string{
	"v5.channel.k8s.io",
	"v4.channel.k8s.io",
	"channel.k8s.io",
}

// PortForwardSubprotocols are the WebSocket framings for port-forward, newest
// first. v2 carries many ports over one socket, which is what makes a single
// tunnelled session enough; the original carries one port per connection.
var PortForwardSubprotocols = []string{
	"v2.portforward.k8s.io",
	"portforward.k8s.io",
}

// streamIdleTimeout bounds how long a stream may sit with no traffic at all.
// A watch on a quiet namespace is legitimately silent, so this is generous —
// it exists to reap abandoned sessions, not to police useful ones.
const streamIdleTimeout = 4 * time.Hour

// serveBodyStream replays a long-lived response body — a watch, or a followed
// log — to the client as it arrives. The response is flushed per chunk, which
// is the whole point: a buffered watch is indistinguishable from a hung one.
func (p *Proxy) serveBodyStream(c *gin.Context, tunnel *Tunnel, event *Event, header map[string][]string) {
	stream, head, err := tunnel.OpenStream(c.Request.Context(), &StreamOpen{
		Method: c.Request.Method,
		Path:   event.Path,
		Header: header,
	})
	if err != nil {
		status, message := tunnelFailure(err)
		p.fail(c, event, status, message)
		return
	}
	defer stream.Close(nil)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		p.fail(c, event, http.StatusInternalServerError, "this server cannot stream responses")
		return
	}

	for name, values := range head.Header {
		if isHopByHop(name) || strings.EqualFold(name, "Content-Length") {
			continue
		}
		for _, value := range values {
			c.Writer.Header().Add(name, value)
		}
	}
	c.Status(head.Status)
	flusher.Flush()

	// The stream is established: record it now, because a watch may run for
	// hours and an audit trail that only lands at the end is not a trail.
	event.Status = head.Status
	event.Phase = PhaseOpen
	event.Duration = time.Since(event.At)
	p.auditor.Record(c.Request.Context(), *event)

	var sent int64
	idle := time.NewTimer(streamIdleTimeout)
	defer idle.Stop()

	for {
		select {
		case chunk, open := <-stream.Chunks():
			if !open {
				p.recordStreamClose(c, event, sent, 0, stream.Err())
				return
			}
			if _, err := c.Writer.Write(chunk.Data); err != nil {
				// The client hung up — normal for `kubectl get -w` under Ctrl-C.
				p.recordStreamClose(c, event, sent, 0, nil)
				return
			}
			sent += int64(len(chunk.Data))
			stream.Consumed(len(chunk.Data))
			flusher.Flush()

			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(streamIdleTimeout)

		case <-idle.C:
			p.recordStreamClose(c, event, sent, 0, ErrStreamIdle)
			return

		case <-c.Request.Context().Done():
			p.recordStreamClose(c, event, sent, 0, nil)
			return
		}
	}
}

// serveUpgradeStream bridges a client's WebSocket to an upgraded session on the
// target cluster — `kubectl exec`, the browser terminal, and `kubectl
// port-forward` over its WebSocket transport. Bytes are piped verbatim in both
// directions: both the channel protocol and the port-forward one multiplex
// their own substreams behind a leading channel byte, so KubeMG stays out of
// the payload entirely and one bridge serves all three.
//
// offered is what to ask the cluster for when the client named no subprotocol
// of its own; a client that named some gets exactly those forwarded.
//
// An exec or an attach is also recorded when a recorder is configured. The
// recording reads the same frames the bridge is already carrying rather than
// re-requesting anything: it is a tee, not a second session, so a recorded shell
// and an unrecorded one reach the cluster identically.
//
// activity, when supplied, is called for every frame the operator sends. It is
// how the browser shell keeps an idle clock without this package having to know
// what a shell's lifetime is; every other caller passes nil.
func (p *Proxy) serveUpgradeStream(c *gin.Context, tunnel *Tunnel, event *Event,
	header map[string][]string, offered []string, parsed APIPath, activity func(),
) {
	requested := websocket.Subprotocols(c.Request)
	if len(requested) == 0 {
		requested = offered
	}

	stream, head, err := tunnel.OpenStream(c.Request.Context(), &StreamOpen{
		Method:       c.Request.Method,
		Path:         event.Path,
		Header:       header,
		Upgrade:      true,
		Subprotocols: requested,
	})
	if err != nil {
		status, message := tunnelFailure(err)
		p.fail(c, event, status, message)
		return
	}

	// Upgrade the client only once the far side is actually up, so a failure
	// is still an HTTP error the client can read rather than an opaque close.
	responseHeader := http.Header{}
	if head.Subprotocol != "" {
		responseHeader.Set("Sec-WebSocket-Protocol", head.Subprotocol)
	}
	conn, err := p.clientUpgrader.Upgrade(c.Writer, c.Request, responseHeader)
	if err != nil {
		stream.Close(err)
		return
	}
	// Say goodbye properly. Dropping the socket instead makes every finished
	// exec look like a crash to kubectl and to the browser terminal.
	defer conn.Close()
	defer closeClient(conn, stream.Err())
	defer stream.Close(nil)

	event.Status = http.StatusSwitchingProtocols
	event.Phase = PhaseOpen
	event.Duration = time.Since(event.At)
	p.auditor.Record(c.Request.Context(), *event)

	// Recording starts only once the session is genuinely up, so a refused exec
	// leaves no empty recording behind. The context is detached from the request
	// on purpose: the recording is closed out *because* the request ended, and a
	// cancelled context cannot write that closing row.
	sink := p.beginRecording(context.WithoutCancel(c.Request.Context()), event, parsed)
	if sink != nil {
		defer func() { sink.Close(stream.Err()) }()
	}

	// What the operator types is checked line by line. This is the half of the
	// guardrails no cluster-side control can reach: everything done inside an
	// interactive session happens behind one API call that was already allowed.
	guard := newCommandGuard(p.guard, event.ClusterID, parsed, event.Path)

	var fromClient, fromCluster int64
	done := make(chan struct{})
	// A refusal has to be written to the client socket, and only one goroutine
	// may ever write to it — gorilla makes concurrent writes a panic, not a race
	// to be hoped away. So the notice is handed to the loop below, which is the
	// one writer, rather than sent from the reader that discovered it.
	notices := make(chan []byte, 4)

	// Client to cluster. Runs in its own goroutine because both directions of
	// an interactive session are live at once.
	go func() {
		defer close(done)
		for {
			kind, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			fromClient += int64(len(payload))
			recordFromClient(sink, payload)
			if activity != nil {
				activity()
			}

			decision, forward := guard.inspect(payload)
			if decision != nil {
				// Recorded as its own line in the trail. A blocked command inside
				// a session would otherwise leave no trace at all: the session's
				// own two records describe the shell, not what was typed into it.
				p.recordGuardedCommand(c, event, decision)
			}
			if !forward {
				// The command never reaches the cluster — and the line it was
				// typed on is cleared, or the next Enter runs it anyway.
				if guard.hasTTY() {
					if err := stream.Send(StreamData{Data: clearLineFrame(), Binary: true}); err != nil {
						return
					}
				}
				// Dropped rather than blocked if the queue is full: the refusal
				// has already taken effect, and a reader goroutine waiting on a
				// client that is not draining would stop reading its keystrokes.
				select {
				case notices <- guardNoticeFrame(decision):
				default:
				}
				continue
			}

			if err := stream.Send(StreamData{
				Data:   payload,
				Binary: kind == websocket.BinaryMessage,
			}); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case chunk, open := <-stream.Chunks():
			if !open {
				p.recordStreamClose(c, event, fromCluster, fromClient, stream.Err())
				return
			}
			kind := websocket.TextMessage
			if chunk.Binary {
				kind = websocket.BinaryMessage
			}
			if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
				p.recordStreamClose(c, event, fromCluster, fromClient, err)
				return
			}
			if err := conn.WriteMessage(kind, chunk.Data); err != nil {
				p.recordStreamClose(c, event, fromCluster, fromClient, nil)
				return
			}
			fromCluster += int64(len(chunk.Data))
			stream.Consumed(len(chunk.Data))
			recordFromCluster(sink, chunk.Data)

		case notice := <-notices:
			// A guardrail refusal, written on the same socket and by the same
			// goroutine as everything else the operator sees.
			if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
				p.recordStreamClose(c, event, fromCluster, fromClient, err)
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, notice); err != nil {
				p.recordStreamClose(c, event, fromCluster, fromClient, nil)
				return
			}

		case <-done:
			p.recordStreamClose(c, event, fromCluster, fromClient, nil)
			return

		case <-c.Request.Context().Done():
			p.recordStreamClose(c, event, fromCluster, fromClient, nil)
			return
		}
	}
}

// recordStreamClose writes the closing half of a stream's audit trail, carrying
// how long the session lasted and how much moved through it.
func (p *Proxy) recordStreamClose(c *gin.Context, event *Event, out, in int64, cause error) {
	closing := *event
	closing.Phase = PhaseClose
	closing.Duration = time.Since(event.At)
	closing.BytesOut = out
	closing.BytesIn = in
	if cause != nil {
		closing.Error = cause.Error()
	}
	p.auditor.Record(c.Request.Context(), closing)
}

// closeClient sends a WebSocket close frame so the far end can tell a finished
// session from a broken one.
func closeClient(conn *websocket.Conn, cause error) {
	code := websocket.CloseNormalClosure
	reason := ""
	if cause != nil {
		code = websocket.CloseInternalServerErr
		reason = cause.Error()
		// A close frame's payload is capped at 125 bytes including the code.
		if len(reason) > 100 {
			reason = reason[:100]
		}
	}
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason),
		time.Now().Add(writeTimeout),
	)
}

func isHopByHop(name string) bool {
	for _, header := range hopByHopHeaders {
		if strings.EqualFold(header, name) {
			return true
		}
	}
	return false
}
