package bastion

import (
	"errors"
	"testing"
	"time"
)

// queuedTunnel is a tunnel with no socket and no writer draining it, which is
// what a stalled peer looks like from the sending side. Everything asserted
// below is about the queueing rules rather than the wire, and those rules are
// the whole point of the change: a cluster's tunnel is one socket shared by
// every session against it, so what happens when one sender outruns it decides
// whether the others keep working.
func queuedTunnel(depth int, wait time.Duration) *Tunnel {
	return &Tunnel{
		ClusterID: 1,
		out:       make(chan []byte, depth),
		queueWait: wait,
		pending:   map[string]chan *Response{},
		streams:   map[string]*Stream{},
		closed:    make(chan struct{}),
	}
}

// The load-bearing assertion. A sender that cannot get its frame out gives up on
// *its own frame* — it does not take the tunnel down, which is what the old
// write path did when the keepalive ping was the write that timed out: every
// session on the cluster died because one transfer was slow.
func TestAFullQueueFailsOneSenderAndLeavesTheTunnelUp(t *testing.T) {
	tunnel := queuedTunnel(1, 20*time.Millisecond)
	tunnel.out <- []byte("occupied")

	err := tunnel.enqueue([]byte("no room"))
	if !errors.Is(err, ErrTunnelBacklog) {
		t.Fatalf("a full queue must report a backlog, got %v", err)
	}

	select {
	case <-tunnel.closed:
		t.Fatal("a full queue must never close the tunnel")
	default:
	}

	// And the tunnel is still usable the moment there is room again.
	<-tunnel.out
	if err := tunnel.enqueue([]byte("room now")); err != nil {
		t.Fatalf("the tunnel must still accept frames: %v", err)
	}
}

// Waiting for room is the backpressure. A sender that finds the queue full must
// not fail immediately — the writer is usually a moment from draining one — so
// this is the case that keeps a burst from becoming a broken call.
func TestASenderWaitsForRoomRatherThanFailingAtOnce(t *testing.T) {
	tunnel := queuedTunnel(1, 2*time.Second)
	tunnel.out <- []byte("occupied")

	go func() {
		time.Sleep(20 * time.Millisecond)
		<-tunnel.out
	}()

	started := time.Now()
	if err := tunnel.enqueue([]byte("waits")); err != nil {
		t.Fatalf("a sender must wait for room, got %v", err)
	}
	if waited := time.Since(started); waited < 10*time.Millisecond {
		t.Fatalf("the sender did not wait for room at all (%s)", waited)
	}
}

// A closed tunnel answers immediately rather than waiting out the queue timeout.
// Every caller — Do, OpenStream, Stream.Send — is already selecting on t.closed,
// and making them sit through a ten-second wait first would turn an agent that
// dropped into a console that hangs.
func TestAClosedTunnelDoesNotWaitForRoom(t *testing.T) {
	tunnel := queuedTunnel(1, time.Minute)
	tunnel.out <- []byte("occupied")
	close(tunnel.closed)

	started := time.Now()
	err := tunnel.enqueue([]byte("too late"))
	if !errors.Is(err, ErrTunnelClosed) {
		t.Fatalf("a closed tunnel must say so, got %v", err)
	}
	if waited := time.Since(started); waited > 5*time.Second {
		t.Fatalf("a closed tunnel waited %s before answering", waited)
	}
}

// The ordinary path: room in the queue, no waiting, and frames come out in the
// order they went in. Ordering is not incidental — a stream's data frames and
// its close frame reordered would end a session before its last output.
func TestFramesLeaveInTheOrderTheyWereQueued(t *testing.T) {
	tunnel := queuedTunnel(4, time.Second)

	for _, frame := range []string{"first", "second", "third"} {
		if err := tunnel.enqueue([]byte(frame)); err != nil {
			t.Fatalf("queue %s: %v", frame, err)
		}
	}

	for _, want := range []string{"first", "second", "third"} {
		if got := string(<-tunnel.out); got != want {
			t.Fatalf("frames left out of order: got %q, want %q", got, want)
		}
	}
}
