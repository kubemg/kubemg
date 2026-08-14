package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/kubemg/kubemg/agent/internal/protocol"
)

// chunkSize is how much of a streaming body is forwarded at a time. Big enough
// that a busy watch is not death by a thousand frames, small enough that a
// quiet one still feels live.
const chunkSize = 32 << 10

// Bounds on what may sit waiting to reach the cluster for one session.
//
// Both are needed, and for different reasons. The frame count keeps a session
// that stopped draining from holding an unbounded queue; the byte ceiling is
// what actually protects the pod, because a port-forward frame can be 32 KB and
// a count alone would let a handful of stalled sessions reach the memory limit.
// The old ceiling was 64 frames with no byte bound, which a paste into a
// terminal or an ordinary port-forward burst reached — and reaching it dropped
// the session with nothing said about why.
const (
	inputQueueFrames = 256
	inputQueueBytes  = 4 << 20
)

// errInputBacklog is what a session is closed with when it outruns its own
// input queue. It exists so the operator gets a reason: this used to be a silent
// close, which reads as a crash rather than as a session that was pushed harder
// than the tunnel could carry.
var errInputBacklog = errors.New(
	"the session fell too far behind its own input to keep up",
)

// stream is one long-lived call the agent is servicing on behalf of the
// bastion. Exactly one of body or socket is in play, decided by StreamOpen.
type stream struct {
	id     string
	cancel context.CancelFunc

	// toCluster carries what the user typed, for an upgraded session. It is nil
	// for a one-way body stream, where the bastion never sends data.
	toCluster chan protocol.StreamData
	// queued is how many bytes are sitting in toCluster.
	queued atomic.Int64

	closeOnce sync.Once
	closed    chan struct{}

	// failure explains an abnormal close, so the bastion can put it in the close
	// frame instead of hanging up silently.
	mu      sync.Mutex
	failure error
}

func newStream(id string, cancel context.CancelFunc, bidirectional bool) *stream {
	s := &stream{id: id, cancel: cancel, closed: make(chan struct{})}
	if bidirectional {
		s.toCluster = make(chan protocol.StreamData, inputQueueFrames)
	}
	return s
}

func (s *stream) close() { s.closeWith(nil) }

// closeWith ends the stream, keeping the first cause given.
func (s *stream) closeWith(cause error) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.failure = cause
		s.mu.Unlock()

		close(s.closed)
		s.cancel()
	})
}

// err reports why the stream ended, or nil for a clean end.
func (s *stream) err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}

// push hands a chunk to the session without ever blocking the tunnel's read
// loop — blocking here would stall every other stream on the socket, which is
// the shared fate the queue in front of the writer exists to remove. A session
// that outruns both of its bounds loses itself, and is told why.
func (s *stream) push(chunk protocol.StreamData) {
	if s.toCluster == nil {
		return
	}

	select {
	case <-s.closed:
		return
	default:
	}

	if s.queued.Add(int64(len(chunk.Data))) > inputQueueBytes {
		s.queued.Add(-int64(len(chunk.Data)))
		s.closeWith(errInputBacklog)
		return
	}

	select {
	case <-s.closed:
		s.queued.Add(-int64(len(chunk.Data)))
	case s.toCluster <- chunk:
	default:
		s.queued.Add(-int64(len(chunk.Data)))
		s.closeWith(errInputBacklog)
	}
}

// take reads the next chunk to write to the cluster, releasing its share of the
// byte budget as it goes.
func (s *stream) take(chunk protocol.StreamData) {
	s.queued.Add(-int64(len(chunk.Data)))
}

// openStream services a stream_open frame. It runs on its own goroutine, so a
// watch that lasts an hour does not hold up anything else.
func (c *Client) openStream(ctx context.Context, id string, open protocol.StreamOpen, out *writer) {
	ctx, cancel := context.WithCancel(ctx)

	s := newStream(id, cancel, open.Upgrade)
	c.streams.add(id, s)
	defer func() {
		c.streams.remove(id)
		s.close()
	}()

	if open.Upgrade {
		c.runUpgradeStream(ctx, s, open, out)
		return
	}
	c.runBodyStream(ctx, s, open, out)
}

// runBodyStream replays a response body that keeps arriving — a watch, or a
// followed log. The streaming HTTP client has no overall timeout, because a
// watch that is deliberately quiet is not a stuck one.
func (c *Client) runBodyStream(ctx context.Context, s *stream, open protocol.StreamOpen, out *writer) {
	req, err := http.NewRequestWithContext(ctx, open.Method, c.kube.URL(open.Path), http.NoBody)
	if err != nil {
		out.start(s.id, protocol.StreamStart{Error: "could not build the upstream request: " + err.Error()})
		return
	}
	for name, values := range open.Header {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}

	resp, err := c.kube.DoStream(req)
	if err != nil {
		out.start(s.id, protocol.StreamStart{Error: err.Error()})
		return
	}
	defer resp.Body.Close()

	out.start(s.id, protocol.StreamStart{Status: resp.StatusCode, Header: resp.Header})

	buf := make([]byte, chunkSize)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			// The buffer is reused, so the chunk has to be copied before it
			// goes into a frame that is marshalled on another goroutine.
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if err := out.data(s.id, protocol.StreamData{Data: chunk}); err != nil {
				return
			}
		}
		if readErr != nil {
			message := ""
			if !errors.Is(readErr, io.EOF) && ctx.Err() == nil {
				message = readErr.Error()
			}
			out.close(s.id, message)
			return
		}

		select {
		case <-s.closed:
			return
		case <-ctx.Done():
			return
		default:
		}
	}
}

// runUpgradeStream bridges an interactive session — exec or attach — between
// the bastion and the cluster's API server. Bytes are piped verbatim: the
// Kubernetes channel protocol multiplexes stdin/stdout/stderr itself, and the
// agent has no business looking inside.
func (c *Client) runUpgradeStream(ctx context.Context, s *stream, open protocol.StreamOpen, out *writer) {
	conn, subprotocol, err := c.kube.DialUpgrade(ctx, open.Path, open.Header, open.Subprotocols)
	if err != nil {
		out.start(s.id, protocol.StreamStart{Error: err.Error()})
		return
	}
	defer conn.Close()

	out.start(s.id, protocol.StreamStart{
		Status:      http.StatusSwitchingProtocols,
		Subprotocol: subprotocol,
	})

	done := make(chan struct{})

	// Bastion to cluster: whatever the user is typing.
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.closed:
				return
			case chunk := <-s.toCluster:
				s.take(chunk)
				kind := websocket.TextMessage
				if chunk.Binary {
					kind = websocket.BinaryMessage
				}
				if err := conn.WriteMessage(kind, chunk.Data); err != nil {
					return
				}
			}
		}
	}()

	// A stream ended from the tunnel's side — the bastion hung up, or the input
	// queue overran — has to reach the cluster's socket too. A gorilla connection
	// is not bound to a context, so without this the read below would sit on a
	// session nobody is listening to any more, holding an exec open in the
	// container. Closing the socket is what unblocks it.
	go func() {
		select {
		case <-s.closed:
		case <-ctx.Done():
		}
		// Best effort: this is here purely to unblock the read loop below, and
		// the loop's own error handling is what reports anything worth reporting.
		_ = conn.Close()
	}()

	// Cluster to bastion: whatever the session is printing.
	for {
		kind, payload, err := conn.ReadMessage()
		if err != nil {
			message := ""
			// A stream that closed itself explains itself. Without this the
			// operator gets a socket that simply stopped, which reads as a crash
			// rather than as a session that was pushed harder than it could carry.
			if cause := s.err(); cause != nil {
				message = cause.Error()
			} else if ctx.Err() == nil && !websocket.IsCloseError(
				err, websocket.CloseNormalClosure, websocket.CloseGoingAway,
			) {
				message = err.Error()
			}
			out.close(s.id, message)
			return
		}
		if err := out.data(s.id, protocol.StreamData{
			Data:   payload,
			Binary: kind == websocket.BinaryMessage,
		}); err != nil {
			return
		}

		select {
		case <-done:
			out.close(s.id, "")
			return
		default:
		}
	}
}

// streamTable tracks the streams this tunnel is servicing.
type streamTable struct {
	mu      sync.Mutex
	streams map[string]*stream
}

func newStreamTable() *streamTable {
	return &streamTable{streams: map[string]*stream{}}
}

func (t *streamTable) add(id string, s *stream) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.streams[id] = s
}

func (t *streamTable) remove(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.streams, id)
}

func (t *streamTable) get(id string) (*stream, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.streams[id]
	return s, ok
}

// closeAll ends every stream, for when the tunnel itself goes away.
func (t *streamTable) closeAll() {
	t.mu.Lock()
	streams := make([]*stream, 0, len(t.streams))
	for _, s := range t.streams {
		streams = append(streams, s)
	}
	t.streams = map[string]*stream{}
	t.mu.Unlock()

	for _, s := range streams {
		s.close()
	}
}

// writer owns the socket's write side. gorilla allows one writer at a time, and
// streams are written from many goroutines at once — so rather than each of them
// taking a lock and waiting out the write in front of it, they hand the frame to
// a queue that one goroutine drains. A slow write then costs the writer, not
// every other session sharing the tunnel.
type writer struct {
	out    chan []byte
	conn   *websocket.Conn
	logger *slog.Logger
	done   chan struct{}
}

// outboundQueue is how many frames may wait to go out before the goroutine
// handing one over is made to wait for room. That wait is the backpressure: it
// slows the one stream that outran the socket instead of the whole tunnel.
const outboundQueue = 512

// errBacklog is a frame that could not be queued. It ends the one call that hit
// it and leaves the tunnel alone.
var errBacklog = errors.New("the tunnel's outbound queue is full")

func newWriter(conn *websocket.Conn, logger *slog.Logger) *writer {
	w := &writer{
		out:    make(chan []byte, outboundQueue),
		conn:   conn,
		logger: logger,
		done:   make(chan struct{}),
	}
	go w.run()
	return w
}

// stop ends the writer. The socket is closed by the pump that owns it.
func (w *writer) stop() { close(w.done) }

// run is the socket's only writer.
func (w *writer) run() {
	for {
		select {
		case <-w.done:
			return
		case payload := <-w.out:
			if err := w.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
				return
			}
			if err := w.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				// The pump's own read will see the same broken socket and end
				// the tunnel; there is nothing useful to do from here.
				return
			}
		}
	}
}

// send queues a frame. It returns once the frame is queued, not once it is on
// the wire — a write that fails afterwards drops the tunnel, and every caller is
// already bound to the tunnel's context.
func (w *writer) send(msg protocol.Message) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	select {
	case <-w.done:
		return errBacklog
	case w.out <- payload:
		return nil
	default:
	}

	timer := time.NewTimer(writeTimeout)
	defer timer.Stop()

	select {
	case <-w.done:
		return errBacklog
	case w.out <- payload:
		return nil
	case <-timer.C:
		return errBacklog
	}
}

func (w *writer) start(id string, head protocol.StreamStart) {
	if err := w.send(protocol.Message{
		Type: protocol.MessageStreamStart, ID: id, StreamStart: &head,
	}); err != nil {
		w.logger.Warn("could not answer a stream open", slog.String("error", err.Error()))
	}
}

func (w *writer) data(id string, chunk protocol.StreamData) error {
	return w.send(protocol.Message{
		Type: protocol.MessageStreamData, ID: id, StreamData: &chunk,
	})
}

func (w *writer) close(id, message string) {
	_ = w.send(protocol.Message{
		Type: protocol.MessageStreamClose, ID: id, StreamClose: &protocol.StreamClose{Error: message},
	})
}
