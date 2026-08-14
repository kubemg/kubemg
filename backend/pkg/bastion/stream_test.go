package bastion

import (
	"errors"
	"testing"
	"time"
)

// deliveringTunnel is a tunnel that can accept a stream's close frame without a
// socket behind it — deliver() calls Stream.Close(), and Close() always tells
// the agent, so the tunnel under test needs somewhere for that frame to land.
func deliveringTunnel() *Tunnel {
	return &Tunnel{
		ClusterID: 1,
		out:       make(chan []byte, 8),
		queueWait: 20 * time.Millisecond,
		pending:   map[string]chan *Response{},
		streams:   map[string]*Stream{},
		closed:    make(chan struct{}),
	}
}

// The load-bearing case this fix exists for: a bulk transfer that arrives
// faster than a byte-count ceiling of 256 chunks would allow must not be
// killed as long as the running total a consumer has not yet caught up on
// stays under the byte bound. Regression test for the `kubectl cp` failure
// found in manual e2e verification — a 150MB download died at ~16MB under the
// old 256-chunk-only bound with no consumer stall at all.
//
// Delivery and consumption happen in lockstep here, on one goroutine, rather
// than racing a background reader against the pushes below: deliver() runs on
// the tunnel's single read loop in production too, so nothing about this test
// needs two goroutines, and a background reader only made the test flaky —
// whether it got scheduled in time before the loop finished pushing was down
// to the Go runtime, not to the bound being exercised.
func TestABurstUnderTheByteBoundSurvivesWithAConsumerDraining(t *testing.T) {
	tunnel := deliveringTunnel()
	stream := newStream("s1", tunnel)
	tunnel.streams["s1"] = stream

	chunk := make([]byte, 32<<10) // the agent's own chunk size
	const chunks = 1500           // ~48MB total, well past the old 256*32KB ceiling
	const batch = 400             // ~12.5MB per batch, comfortably under the byte bound

	for i := 0; i < chunks; i++ {
		stream.deliver(StreamData{Data: chunk})
		select {
		case <-stream.closed:
			t.Fatalf("closed after %d/%d chunks, before the byte bound should have been reached: %v", i+1, chunks, stream.Err())
		default:
		}

		if (i+1)%batch == 0 {
			for j := 0; j < batch; j++ {
				got := <-stream.Chunks()
				stream.Consumed(len(got.Data))
			}
		}
	}
}

// A consumer that never reads at all is what the byte bound exists to catch —
// it must still lose its stream, just at the byte ceiling rather than the old
// 256-chunk one.
func TestAStalledConsumerStillLosesItsStreamAtTheByteBound(t *testing.T) {
	tunnel := deliveringTunnel()
	stream := newStream("s1", tunnel)
	tunnel.streams["s1"] = stream

	chunk := make([]byte, 32<<10)
	closed := false
	for i := 0; i < streamBufferBytes/len(chunk)+2; i++ {
		stream.deliver(StreamData{Data: chunk})
		select {
		case <-stream.closed:
			closed = true
		default:
		}
		if closed {
			break
		}
	}

	if !closed {
		t.Fatal("a stream with nobody draining it must close once it exceeds the byte bound")
	}
	if !errors.Is(stream.Err(), ErrStreamBacklog) {
		t.Fatalf("expected ErrStreamBacklog, got %v", stream.Err())
	}
}

// deliver() must never block, no matter which bound a stalled consumer trips —
// blocking here would stall the tunnel's one read loop, shared by every stream
// on the cluster. This is the same property registry_queue_test.go pins for
// the write side.
func TestDeliverNeverBlocksOnAStalledConsumer(t *testing.T) {
	tunnel := deliveringTunnel()
	stream := newStream("s1", tunnel)
	tunnel.streams["s1"] = stream

	chunk := make([]byte, 32<<10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < streamBufferBytes/len(chunk)+10; i++ {
			stream.deliver(StreamData{Data: chunk})
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deliver() blocked on a stalled consumer")
	}
}
