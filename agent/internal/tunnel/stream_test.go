package tunnel

import (
	"context"
	"errors"
	"testing"

	"github.com/kubemg/kubemg/agent/internal/protocol"
)

// bidirectionalStream is what an exec or a port-forward gets: a stream with an
// input queue the bastion pushes into.
func bidirectionalStream() *stream {
	_, cancel := context.WithCancel(context.Background())
	return newStream("s1-1", cancel, true)
}

// A session that stops draining its own input has to lose itself rather than
// stall the tunnel's read loop, which every other session on the cluster shares.
// What changed is that it now says why: the silent close this used to be reads
// as a crash, and an operator cannot tell a dropped session from a dropped
// cluster.
func TestOverrunningTheFrameQueueClosesWithACause(t *testing.T) {
	s := bidirectionalStream()

	// Nothing is draining, so the queue fills at its frame bound.
	for i := 0; i < inputQueueFrames+1; i++ {
		s.push(protocol.StreamData{Data: []byte("x")})
	}

	select {
	case <-s.closed:
	default:
		t.Fatal("a session that overran its input queue must be closed")
	}
	if !errors.Is(s.err(), errInputBacklog) {
		t.Fatalf("the close must carry a reason, got %v", s.err())
	}
}

// The byte ceiling is the one that protects the pod: a port-forward frame can be
// 32 KB, so a frame count alone would let a few stalled sessions reach the
// container's memory limit.
func TestOverrunningTheByteQueueClosesWithACause(t *testing.T) {
	s := bidirectionalStream()

	// Well inside the frame bound, well past the byte bound.
	chunk := make([]byte, inputQueueBytes/4)
	for i := 0; i < 8; i++ {
		s.push(protocol.StreamData{Data: chunk})
	}

	select {
	case <-s.closed:
	default:
		t.Fatal("a session past its byte budget must be closed")
	}
	if !errors.Is(s.err(), errInputBacklog) {
		t.Fatalf("the close must carry a reason, got %v", s.err())
	}
}

// The budget is a running total, not a high-water mark. A long session moving
// far more than the ceiling in aggregate must never be closed for it — only one
// that is genuinely holding that much unsent at one moment.
func TestASessionThatKeepsUpIsNeverClosed(t *testing.T) {
	s := bidirectionalStream()

	chunk := make([]byte, 32<<10)
	for i := 0; i < 400; i++ {
		s.push(protocol.StreamData{Data: chunk})

		select {
		case queued := <-s.toCluster:
			s.take(queued)
		default:
			t.Fatalf("chunk %d was not queued", i)
		}
	}

	select {
	case <-s.closed:
		t.Fatalf("a draining session was closed: %v", s.err())
	default:
	}
	if queued := s.queued.Load(); queued != 0 {
		t.Fatalf("the byte budget leaked: %d bytes still accounted for", queued)
	}
}

// push runs on the tunnel's read loop, which every stream on the socket shares.
// Blocking it — even briefly, even to be kind to one slow session — is the
// head-of-line stall the queues exist to remove.
func TestPushNeverBlocksOnAFullQueue(t *testing.T) {
	s := bidirectionalStream()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < inputQueueFrames*2; i++ {
			s.push(protocol.StreamData{Data: []byte("x")})
		}
	}()

	select {
	case <-done:
	case <-context.Background().Done():
	}
	// Reaching here at all is the assertion: a blocking push would deadlock the
	// test rather than fail it, which the timeout on `go test` reports.
}

// A one-way stream — a watch, a followed log — has no input queue at all, and a
// stray data frame for one must not panic or account for bytes nobody sent.
func TestPushIsANoOpForAOneWayStream(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	s := newStream("s1-2", cancel, false)

	s.push(protocol.StreamData{Data: []byte("unexpected")})

	select {
	case <-s.closed:
		t.Fatal("a one-way stream must not close over a frame it ignores")
	default:
	}
	if queued := s.queued.Load(); queued != 0 {
		t.Fatalf("a one-way stream accounted for %d bytes", queued)
	}
}
