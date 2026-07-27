package bastion

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// clientUpgrader accepts the browser terminal's WebSocket. The Kubernetes
// channel subprotocols are echoed back so xterm-style clients and kubectl both
// see the negotiation they expect.
var clientUpgrader = websocket.Upgrader{
	HandshakeTimeout: 15 * time.Second,
	ReadBufferSize:   32 << 10,
	WriteBufferSize:  32 << 10,
	Subprotocols:     ChannelSubprotocols,
	// The caller is already authenticated by JWT before reaching here; there is
	// no cookie to protect, so origin is not the control that matters.
	CheckOrigin: func(*http.Request) bool { return true },
}

// ChannelSubprotocols are the Kubernetes exec/attach channel protocols, newest
// first. v5 adds a close signal on stdin; v4 is what every current API server
// speaks.
var ChannelSubprotocols = []string{
	"v5.channel.k8s.io",
	"v4.channel.k8s.io",
	"channel.k8s.io",
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
// target cluster — this is `kubectl exec` and the browser terminal. Bytes are
// piped verbatim in both directions: the Kubernetes channel protocol multiplexes
// stdin/stdout/stderr itself, so KubeMG stays out of the payload.
func (p *Proxy) serveUpgradeStream(c *gin.Context, tunnel *Tunnel, event *Event, header map[string][]string) {
	requested := websocket.Subprotocols(c.Request)
	if len(requested) == 0 {
		requested = ChannelSubprotocols
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
	conn, err := clientUpgrader.Upgrade(c.Writer, c.Request, responseHeader)
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

	var fromClient, fromCluster int64
	done := make(chan struct{})

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
