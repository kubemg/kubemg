package bastion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Bounds on how much of one stream's output may sit unread before the server
// gives up on its consumer.
//
// Both are needed, and for different reasons — the same shape as the agent's
// own input queue (agent/internal/tunnel/stream.go), which this mirrors. The
// frame count alone used to be the only bound, at 256, and a single ordinary
// `kubectl cp` download reliably tripped it: the agent hands frames to
// deliver() as fast as the cluster produces them, and a client write a few
// dozen milliseconds slower for any reason drains it below what one burst of
// a bulk transfer needs. The byte ceiling is what actually matters for a
// stream carrying real payloads rather than keystrokes; the frame count stays
// as a backstop against a great many tiny chunks.
//
// deliver() runs on the tunnel's one read loop, shared by every stream on that
// cluster, so it can never block waiting for room — that would stall every
// other session on the socket, which is exactly the head-of-line problem the
// outbound queue in registry.go exists to remove on the write side. Raising
// these bounds does not remove the ceiling, only moves it past the burst an
// ordinary bulk transfer produces; a consumer that is genuinely, sustainedly
// slower than its producer still loses its stream, and is told why.
const (
	streamBuffer      = 1024
	streamBufferBytes = 32 << 20
)

// streamOpenTimeout bounds the handshake only. The stream itself is unbounded
// on purpose — a watch on a quiet namespace is meant to sit there.
const streamOpenTimeout = 30 * time.Second

// ErrStreamBacklog is returned when a consumer falls too far behind.
var ErrStreamBacklog = errors.New("stream consumer fell too far behind")

// ErrStreamClosed is returned when a stream has already ended.
var ErrStreamClosed = errors.New("stream is closed")

// ErrStreamIdle ends a session that has carried nothing for a very long time,
// so an abandoned terminal does not hold a tunnel slot forever.
var ErrStreamIdle = errors.New("stream was idle for too long")

// ErrStreamHandshake is returned when the agent never answers a stream open.
var ErrStreamHandshake = errors.New("the agent did not answer the stream open")

// Stream is one long-lived call over a tunnel: a watch, a followed log, or an
// exec session. Chunks arrive on Chunks() until the stream ends; Err() then
// says whether it ended cleanly.
type Stream struct {
	ID     string
	tunnel *Tunnel

	// start carries the response head exactly once.
	start chan *StreamStart
	data  chan StreamData
	// queued is how many bytes are sitting in data, unread.
	queued atomic.Int64

	closeOnce sync.Once
	closed    chan struct{}

	mu   sync.Mutex
	err  error
	sent bool
}

func newStream(id string, tunnel *Tunnel) *Stream {
	return &Stream{
		ID:     id,
		tunnel: tunnel,
		start:  make(chan *StreamStart, 1),
		data:   make(chan StreamData, streamBuffer),
		closed: make(chan struct{}),
	}
}

// Chunks yields data arriving from the agent. It is closed when the stream
// ends, so a plain range terminates naturally. Every chunk read from it must
// be handed to Consumed, which is what lets deliver() know there is room for
// more.
func (s *Stream) Chunks() <-chan StreamData { return s.data }

// Consumed releases a chunk's share of the stream's byte budget. Callers read
// a chunk from Chunks() and hand its length here once they are done with it —
// separate calls, rather than folding this into Chunks() itself, because a
// channel receive cannot also report how many bytes the caller is about to
// free.
func (s *Stream) Consumed(n int) {
	s.queued.Add(-int64(n))
}

// Done is closed when the stream ends, from either side.
func (s *Stream) Done() <-chan struct{} { return s.closed }

// Err reports why the stream ended, or nil for a clean end.
func (s *Stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Send pushes a chunk towards the agent. Only bidirectional streams — exec and
// attach — ever use it.
func (s *Stream) Send(chunk StreamData) error {
	select {
	case <-s.closed:
		return ErrStreamClosed
	default:
	}
	return s.tunnel.send(Message{Type: MessageStreamData, ID: s.ID, StreamData: &chunk})
}

// Close ends the stream and tells the agent to stop. It is safe to call more
// than once; only the first cause is kept.
func (s *Stream) Close(cause error) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.err = cause
		s.mu.Unlock()

		close(s.closed)
		close(s.data)
		s.tunnel.dropStream(s.ID)

		// Best effort: if the tunnel is already gone there is nobody to tell.
		message := ""
		if cause != nil {
			message = cause.Error()
		}
		_ = s.tunnel.send(Message{
			Type:        MessageStreamClose,
			ID:          s.ID,
			StreamClose: &StreamClose{Error: message},
		})
	})
}

// deliver hands a chunk to the consumer without ever blocking the tunnel's read
// loop — see the bounds' own comment for why. A stream that outruns both of
// them loses itself rather than the socket everything else shares.
func (s *Stream) deliver(chunk StreamData) {
	select {
	case <-s.closed:
		return
	default:
	}

	if s.queued.Add(int64(len(chunk.Data))) > streamBufferBytes {
		s.queued.Add(-int64(len(chunk.Data)))
		s.Close(ErrStreamBacklog)
		return
	}

	select {
	case s.data <- chunk:
	default:
		s.queued.Add(-int64(len(chunk.Data)))
		s.Close(ErrStreamBacklog)
	}
}

// OpenStream starts a streaming call and waits for the agent's response head.
// A stream that the agent refuses comes back as an error, so callers never have
// to inspect a half-open stream.
//
// Only the handshake is bounded: once the stream is up it may run for hours,
// but an agent that never answers the open must not pin the caller forever.
func (t *Tunnel) OpenStream(ctx context.Context, open *StreamOpen) (*Stream, *StreamStart, error) {
	select {
	case <-t.closed:
		return nil, nil, ErrTunnelClosed
	default:
	}

	handshake, cancel := context.WithTimeout(ctx, streamOpenTimeout)
	defer cancel()

	t.mu.Lock()
	t.nextID++
	id := fmt.Sprintf("s%d-%d", t.ClusterID, t.nextID)
	stream := newStream(id, t)
	t.streams[id] = stream
	t.mu.Unlock()

	if err := t.send(Message{Type: MessageStreamOpen, ID: id, StreamOpen: open}); err != nil {
		t.dropStream(id)
		return nil, nil, err
	}

	select {
	case head := <-stream.start:
		if head.Error != "" {
			stream.Close(nil)
			return nil, nil, fmt.Errorf("agent refused the stream: %s", head.Error)
		}
		return stream, head, nil
	case <-stream.closed:
		return nil, nil, ErrStreamClosed
	case <-t.closed:
		t.dropStream(id)
		return nil, nil, ErrTunnelClosed
	case <-handshake.Done():
		stream.Close(handshake.Err())
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, ErrStreamHandshake
	}
}

// dropStream deregisters a stream without touching it, so a stream that closed
// itself does not have to coordinate with the read loop.
func (t *Tunnel) dropStream(id string) {
	t.mu.Lock()
	delete(t.streams, id)
	t.mu.Unlock()
}

func (t *Tunnel) lookupStream(id string) (*Stream, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	stream, ok := t.streams[id]
	return stream, ok
}

// closeStreams releases every open stream when the tunnel itself dies.
func (t *Tunnel) closeStreams(cause error) {
	t.mu.Lock()
	streams := make([]*Stream, 0, len(t.streams))
	for _, stream := range t.streams {
		streams = append(streams, stream)
	}
	t.mu.Unlock()

	if cause == nil {
		cause = ErrTunnelClosed
	}
	for _, stream := range streams {
		stream.Close(cause)
	}
}
