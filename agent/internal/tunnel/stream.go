package tunnel

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"

	"github.com/kubemg/kubemg/agent/internal/protocol"
)

// chunkSize is how much of a streaming body is forwarded at a time. Big enough
// that a busy watch is not death by a thousand frames, small enough that a
// quiet one still feels live.
const chunkSize = 32 << 10

// stream is one long-lived call the agent is servicing on behalf of the
// bastion. Exactly one of body or socket is in play, decided by StreamOpen.
type stream struct {
	id     string
	cancel context.CancelFunc

	// toCluster carries what the user typed, for an upgraded session. It is nil
	// for a one-way body stream, where the bastion never sends data.
	toCluster chan protocol.StreamData

	closeOnce sync.Once
	closed    chan struct{}
}

func newStream(id string, cancel context.CancelFunc, bidirectional bool) *stream {
	s := &stream{id: id, cancel: cancel, closed: make(chan struct{})}
	if bidirectional {
		s.toCluster = make(chan protocol.StreamData, 64)
	}
	return s
}

func (s *stream) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.cancel()
	})
}

// push hands a chunk to the session without blocking the tunnel's read loop.
// A session that cannot keep up with its own input is broken; dropping it is
// better than stalling every other stream on the socket.
func (s *stream) push(chunk protocol.StreamData) {
	if s.toCluster == nil {
		return
	}
	select {
	case <-s.closed:
	case s.toCluster <- chunk:
	default:
		s.close()
	}
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

	// Cluster to bastion: whatever the session is printing.
	for {
		kind, payload, err := conn.ReadMessage()
		if err != nil {
			message := ""
			if ctx.Err() == nil && !websocket.IsCloseError(
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

// writer serialises frames onto the socket. gorilla allows one writer at a
// time, and streams are written from many goroutines at once.
type writer struct {
	mu     sync.Mutex
	conn   *websocket.Conn
	logger *slog.Logger
}

func (w *writer) send(msg protocol.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return writeMessage(w.conn, msg)
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
