package bastion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Tunnel lifetimes. The agent pings well inside the read deadline, so a tunnel
// that a load balancer silently dropped is noticed within a heartbeat rather
// than hanging until the first proxied request times out.
const (
	heartbeatInterval = 20 * time.Second
	readTimeout       = 60 * time.Second
	writeTimeout      = 10 * time.Second
	// maxFrame caps a single frame. Kubernetes objects are small; the ceiling
	// exists so one agent cannot exhaust the server's memory.
	maxFrame = 16 << 20
)

// outboundQueue is how many frames may wait to go out on a tunnel before the
// goroutine handing one over is made to wait for room.
//
// The queue exists because a cluster's tunnel is one socket shared by every
// session against that cluster, and the write side used to be a mutex every
// sender took in turn. A single write that blocked on a full TCP send buffer —
// a `kubectl cp` upload, a port-forward pushing data, a fast `logs -f` down a
// slow link — therefore held every other session's frames behind it, and if the
// keepalive ping was the write that timed out, the tunnel was torn down and
// every session on that cluster died at once. With a queue the slow write blocks
// one goroutine, the writer's, and everyone else keeps handing frames over.
//
// There are two queues, not one. A cluster's sessions are not all alike: a
// keystroke, a line of `logs -f` and a stream's close are small and someone is
// waiting on each one, while a proxied request — and, coming the other way, a
// list response — is bulk. In one queue a bulk frame that got there first is
// written first, however long it takes. So stream frames go in `out` and are
// written ahead of anything in `bulk`, which only gets a turn every
// urgentBurst frames while urgent ones keep arriving — enough that a busy
// port-forward cannot starve an API call into its timeout.
const outboundQueue = 512

// urgentBurst is how many urgent frames may go out in a row while a bulk frame
// waits. Urgent frames are small (a stream chunk is 32 KB at most from a body
// stream, a terminal frame usually far less), so bulk still gets a meaningful
// share of the socket.
const urgentBurst = 16

// maxResponseBody bounds a response the agent sends in parts, once joined. It is
// the ceiling a whole response always had — one frame of maxFrame — so sending in
// parts changes how a response travels and nothing about how large one may be.
const maxResponseBody = maxFrame

// errResponseTooLarge is the answer to a response whose parts outgrew
// maxResponseBody. The caller gets it as an agent error, the same shape as any
// other response the agent could not deliver.
var errResponseTooLarge = errors.New("the agent's response was larger than the tunnel accepts")

// assemblyBudget bounds the bytes held across *every* response this tunnel is
// joining at once. maxResponseBody bounds one; without this, an agent — or
// whoever holds a leaked registration token — could part-fill every request the
// server has in flight and hold 16 MB against each of them for as long as the
// callers wait. It is what a well-behaved agent can ever have in flight: its 16
// concurrent calls at its own 8 MB cap. A part that would take the tunnel past
// it fails the one response it belongs to, never the tunnel.
const assemblyBudget = 16 * (8 << 20)

// errAssemblyBudget is the answer to a response that arrived while the tunnel
// was already holding assemblyBudget of other partial responses.
var errAssemblyBudget = errors.New("the tunnel is holding too many partial responses to accept another")

// ErrTunnelBacklog is returned when the outbound queue stays full. It is
// deliberately not a tunnel-level failure: one sender that cannot get a frame
// out is one broken call, and killing the socket over it is the shared fate the
// queue exists to remove.
var ErrTunnelBacklog = errors.New("the tunnel's outbound queue is full")

// ErrNoTunnel is returned when a cluster has no agent attached.
var ErrNoTunnel = errors.New("no agent tunnel is attached to this cluster")

// ErrTunnelClosed is returned when the tunnel dropped while a request was in
// flight.
var ErrTunnelClosed = errors.New("agent tunnel closed before the request completed")

// Tunnel is one live agent connection. Requests are multiplexed over it by
// correlation ID, so a single socket serves every concurrent kubectl session
// against that cluster.
type Tunnel struct {
	ClusterID   uint
	ClusterName string
	// Agent and Kubernetes versions as reported in the handshake.
	AgentVersion      string
	KubernetesVersion string
	ConnectedAt       time.Time
	// SourceAddr is where the agent dialled in from, as the server saw it. It is
	// what tells "the agent reconnected" apart from "somebody else is now the
	// agent" when a displacement is recorded.
	SourceAddr string

	// credential is the registration token this tunnel authenticated with. It is
	// kept so the tunnel can be cut off when that token stops resolving to its
	// cluster — a rotation on another replica, or the cluster being deleted.
	credential string

	conn *websocket.Conn
	// out and bulk carry frames to the one goroutine that writes them. gorilla
	// permits a single concurrent writer, and this is it — see outboundQueue for
	// why these are queues rather than the mutex they replaced, and why two.
	out  chan []byte
	bulk chan []byte
	// urgentRun counts urgent frames written since the last bulk one. Only the
	// writer touches it.
	urgentRun int
	// queueWait is how long a sender waits for room in that queue before giving
	// up on its own frame. It is a field only so a test can shorten it.
	queueWait time.Duration
	// bodyLimit and budget are maxResponseBody and assemblyBudget, as fields
	// only so a test can shorten them.
	bodyLimit int
	budget    int

	// mu guards the two correlation tables, the ID counter and assembled.
	// Streams and request/response pairs share it because they share the ID
	// space.
	mu      sync.Mutex
	pending map[string]*pendingCall
	// assembled is how many bytes of partial responses every pending call holds
	// between them; see assemblyBudget.
	assembled int
	streams   map[string]*Stream
	nextID    uint64

	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error
}

func newTunnel(conn *websocket.Conn, clusterID uint, clusterName string, hello Hello) *Tunnel {
	t := &Tunnel{
		ClusterID:         clusterID,
		ClusterName:       clusterName,
		AgentVersion:      hello.AgentVersion,
		KubernetesVersion: hello.KubernetesVersion,
		ConnectedAt:       time.Now().UTC(),
		conn:              conn,
		out:               make(chan []byte, outboundQueue),
		bulk:              make(chan []byte, outboundQueue),
		queueWait:         writeTimeout,
		bodyLimit:         maxResponseBody,
		budget:            assemblyBudget,
		pending:           map[string]*pendingCall{},
		streams:           map[string]*Stream{},
		closed:            make(chan struct{}),
	}
	// The writer starts with the tunnel rather than with serve(), because the
	// welcome frame is sent before serve() is ever called and a queue with
	// nobody draining it is just a slower mutex.
	go t.writeLoop()
	return t
}

// Do sends a request down the tunnel and waits for the matching response. It
// returns as soon as the caller's context is done, so a client that hung up
// does not pin a slot.
func (t *Tunnel) Do(ctx context.Context, req *Request) (*Response, error) {
	select {
	case <-t.closed:
		return nil, ErrTunnelClosed
	default:
	}

	t.mu.Lock()
	t.nextID++
	id := fmt.Sprintf("%d-%d", t.ClusterID, t.nextID)
	call := &pendingCall{replies: make(chan *Response, 1)}
	t.pending[id] = call
	t.mu.Unlock()

	// Leaving releases whatever the call was holding, answered or not: a body
	// part-sent and never finished is dropped with the caller that gave up on
	// it, rather than held for the life of the tunnel.
	defer t.forget(id)

	if err := t.send(Message{Type: MessageRequest, ID: id, Request: req}); err != nil {
		return nil, err
	}

	select {
	case resp := <-call.replies:
		return resp, nil
	case <-t.closed:
		return nil, ErrTunnelClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// send hands a frame to the tunnel's writer. It returns once the frame is
// queued, not once it is on the wire: a write that fails afterwards closes the
// tunnel, and every caller is already waiting on t.closed for exactly that.
func (t *Tunnel) send(msg Message) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encode %s frame: %w", msg.Type, err)
	}

	lane := t.out
	if msg.Type == MessageRequest {
		lane = t.bulk
	}
	if err := t.enqueueOn(lane, payload); err != nil {
		return fmt.Errorf("queue %s frame: %w", msg.Type, err)
	}
	return nil
}

// enqueue puts a frame on the urgent queue. See enqueueOn.
func (t *Tunnel) enqueue(payload []byte) error { return t.enqueueOn(t.out, payload) }

// enqueueOn puts a frame in front of the writer, waiting for room if the queue
// is full. It is separate from send so the queueing rules can be exercised
// without a socket: what matters about them is that a full queue slows one
// sender and never closes the tunnel, and that is a property, not a wire format.
func (t *Tunnel) enqueueOn(lane chan []byte, payload []byte) error {
	// The common case: room in the queue, no timer, no allocation.
	select {
	case <-t.closed:
		return ErrTunnelClosed
	case lane <- payload:
		return nil
	default:
	}

	// The queue is full, which means the far side is not keeping up. Waiting
	// here *is* the backpressure — it slows the one call that found the queue
	// full rather than every session sharing the socket.
	timer := time.NewTimer(t.queueWait)
	defer timer.Stop()

	select {
	case <-t.closed:
		return ErrTunnelClosed
	case lane <- payload:
		return nil
	case <-timer.C:
		return ErrTunnelBacklog
	}
}

// writeLoop is the tunnel's only writer. A slow write blocks this goroutine and
// nothing else; the queues in front of it are what keep one session's bulk
// transfer from delaying another session's keystroke.
func (t *Tunnel) writeLoop() {
	for {
		payload, ok := t.nextFrame()
		if !ok {
			return
		}
		if err := t.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
			t.closeWith(fmt.Errorf("set write deadline: %w", err))
			return
		}
		if err := t.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			t.closeWith(fmt.Errorf("write frame: %w", err))
			return
		}
	}
}

// nextFrame picks what the writer sends next: an urgent frame whenever there is
// one, except that a waiting bulk frame goes first once urgentBurst urgent ones
// have gone in a row. It blocks until there is something to send, and reports
// false once the tunnel is closed.
func (t *Tunnel) nextFrame() ([]byte, bool) {
	if t.urgentRun >= urgentBurst {
		select {
		case payload := <-t.bulk:
			t.urgentRun = 0
			return payload, true
		default:
		}
	}
	select {
	case payload := <-t.out:
		t.urgentRun++
		return payload, true
	default:
	}
	select {
	case <-t.closed:
		return nil, false
	case payload := <-t.out:
		t.urgentRun++
		return payload, true
	case payload := <-t.bulk:
		t.urgentRun = 0
		return payload, true
	}
}

// serve pumps frames until the socket dies, then releases every waiter. It
// blocks for the life of the connection and owns the read side exclusively.
func (t *Tunnel) serve() error {
	defer t.closeWith(nil)

	t.conn.SetReadLimit(maxFrame)
	if err := t.conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return err
	}
	t.conn.SetPongHandler(func(string) error {
		return t.conn.SetReadDeadline(time.Now().Add(readTimeout))
	})

	go t.keepalive()

	for {
		_, payload, err := t.conn.ReadMessage()
		if err != nil {
			t.closeWith(err)
			return err
		}

		var msg Message
		if err := json.Unmarshal(payload, &msg); err != nil {
			// A frame we cannot parse is a protocol violation, not a transient
			// glitch: drop the tunnel rather than silently ignoring traffic.
			err = fmt.Errorf("malformed frame from agent: %w", err)
			t.closeWith(err)
			return err
		}
		switch msg.Type {
		case MessageResponsePart:
			if msg.ResponsePart == nil {
				continue
			}
			t.addPart(msg.ID, msg.ResponsePart.Data)

		case MessageResponse:
			if msg.Response == nil {
				continue
			}
			if replies, ok := t.complete(msg.ID, msg.Response); ok {
				// Buffered by one and read at most once, so this never blocks
				// even if the caller already gave up.
				select {
				case replies <- msg.Response:
				default:
				}
			}

		case MessageStreamStart:
			stream, ok := t.lookupStream(msg.ID)
			if !ok || msg.StreamStart == nil {
				continue
			}
			select {
			case stream.start <- msg.StreamStart:
			default:
			}

		case MessageStreamData:
			stream, ok := t.lookupStream(msg.ID)
			if !ok || msg.StreamData == nil {
				continue
			}
			// Never blocks: a hopeless consumer loses its own stream rather
			// than stalling every other stream on this socket.
			stream.deliver(*msg.StreamData)

		case MessageStreamClose:
			stream, ok := t.lookupStream(msg.ID)
			if !ok {
				continue
			}
			var cause error
			if msg.StreamClose != nil && msg.StreamClose.Error != "" {
				cause = errors.New(msg.StreamClose.Error)
			}
			stream.Close(cause)
		}
	}
}

// pendingCall is a request waiting on its response, and the part of the body
// that has arrived ahead of it.
type pendingCall struct {
	replies chan *Response
	// body is the parts so far. failure, once set, is the answer the call gets
	// whatever else arrives: a response that cannot be delivered whole is not
	// delivered at all.
	body    []byte
	failure error
}

// addPart files a slice of a response body against the call waiting for it. A
// part for a call nobody waits on any more is dropped.
func (t *Tunnel) addPart(id string, data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()

	call, ok := t.pending[id]
	if !ok || call.failure != nil {
		return
	}
	switch {
	case len(call.body)+len(data) > t.bodyLimit:
		t.failLocked(call, errResponseTooLarge)
	case t.assembled+len(data) > t.budget:
		t.failLocked(call, errAssemblyBudget)
	default:
		call.body = append(call.body, data...)
		t.assembled += len(data)
	}
}

// failLocked gives up on a call's body, releasing what it held. t.mu is held.
func (t *Tunnel) failLocked(call *pendingCall, cause error) {
	t.assembled -= len(call.body)
	call.body = nil
	call.failure = cause
}

// complete joins a call's parts in front of the final frame's own body and
// hands back where to deliver it. A final frame carrying an error keeps it and
// the parts are dropped; a call that failed while assembling gets that failure.
func (t *Tunnel) complete(id string, resp *Response) (chan *Response, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	call, ok := t.pending[id]
	if !ok {
		return nil, false
	}
	body, failure := call.body, call.failure
	t.assembled -= len(body)
	call.body, call.failure = nil, nil

	switch {
	case resp.Error != "":
	case failure != nil:
		*resp = Response{Error: failure.Error()}
	case len(body)+len(resp.Body) > t.bodyLimit:
		*resp = Response{Error: errResponseTooLarge.Error()}
	case len(body) > 0:
		resp.Body = append(body, resp.Body...)
	}
	return call.replies, true
}

// forget drops a call, releasing anything it was still holding.
func (t *Tunnel) forget(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if call, ok := t.pending[id]; ok {
		t.assembled -= len(call.body)
		delete(t.pending, id)
	}
}

// keepalive pings the agent so the socket stays warm through idle proxies and
// so a dead peer trips the read deadline instead of lingering.
//
// The ping goes out through WriteControl, which gorilla documents as safe to
// call concurrently with everything else — so it neither queues behind a data
// frame nor waits for the writer. That matters more than it looks: the ping is
// the one write whose failure tears the tunnel down, and while it shared a lock
// with the data frames, a busy transfer could time it out and take every
// session on the cluster with it. Now a failing ping means the socket really is
// gone.
func (t *Tunnel) keepalive() {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-t.closed:
			return
		case <-ticker.C:
			err := t.conn.WriteControl(
				websocket.PingMessage, nil, time.Now().Add(writeTimeout),
			)
			if err != nil {
				t.closeWith(err)
				return
			}
		}
	}
}

// closeWith shuts the tunnel down once, recording the first cause and
// releasing every stream riding on it.
func (t *Tunnel) closeWith(cause error) {
	t.closeOnce.Do(func() {
		t.closeErr = cause
		close(t.closed)
		_ = t.conn.Close()
		t.closeStreams(cause)
	})
}

// Close tears the tunnel down, releasing everything waiting on it.
func (t *Tunnel) Close() { t.closeWith(nil) }

// Registry tracks which clusters currently have an agent attached.
type Registry struct {
	mu      sync.RWMutex
	tunnels map[uint]*Tunnel
}

// NewRegistry builds an empty connection pool.
func NewRegistry() *Registry {
	return &Registry{tunnels: map[uint]*Tunnel{}}
}

// Add registers a tunnel, returning any tunnel it displaced. A rolling agent
// deployment briefly has two pods dialling in; the newest wins and the caller
// closes the old one.
func (r *Registry) Add(tunnel *Tunnel) *Tunnel {
	r.mu.Lock()
	defer r.mu.Unlock()

	previous := r.tunnels[tunnel.ClusterID]
	r.tunnels[tunnel.ClusterID] = tunnel
	return previous
}

// Remove drops a tunnel only if it is still the registered one, so a displaced
// agent's cleanup cannot evict its replacement. It reports whether it removed.
func (r *Registry) Remove(tunnel *Tunnel) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if current, ok := r.tunnels[tunnel.ClusterID]; !ok || current != tunnel {
		return false
	}
	delete(r.tunnels, tunnel.ClusterID)
	return true
}

// Snapshot returns every live tunnel, for a sweep that must not hold the
// registry's lock while it does I/O.
func (r *Registry) Snapshot() []*Tunnel {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*Tunnel, 0, len(r.tunnels))
	for _, tunnel := range r.tunnels {
		out = append(out, tunnel)
	}
	return out
}

// Get returns the tunnel for a cluster.
func (r *Registry) Get(clusterID uint) (*Tunnel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tunnel, ok := r.tunnels[clusterID]
	return tunnel, ok
}

// Connected reports whether a cluster has an agent attached right now.
func (r *Registry) Connected(clusterID uint) bool {
	_, ok := r.Get(clusterID)
	return ok
}

// Len is the number of live tunnels.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tunnels)
}
