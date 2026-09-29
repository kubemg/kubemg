package bastion

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// A full queue drops rather than blocks, and each drop reaches the hook exactly
// once — that hook is how a hole in the trail becomes something an alert sees.
func TestStoreAuditorReportsEachDrop(t *testing.T) {
	auditor := NewStoreAuditor(&queueSink{}, nil, nil) // no Run: the queue can only fill
	var drops atomic.Int64
	auditor.OnDrop(func() { drops.Add(1) })

	done := make(chan struct{})
	var beforeFull int64
	go func() {
		defer close(done)
		for range auditQueueSize {
			auditor.Record(context.Background(), Event{Verb: "get", Status: 200})
		}
		beforeFull = drops.Load()
		auditor.Record(context.Background(), Event{Verb: "get", Status: 200})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record blocked when the queue was full")
	}
	if beforeFull != 0 {
		t.Fatalf("nothing is dropped before the queue fills, hook fired %d times", beforeFull)
	}
	if got := drops.Load(); got != 1 {
		t.Fatalf("one record over the limit is one drop, hook fired %d times", got)
	}
	if got := auditor.QueueLen(); got != auditQueueSize {
		t.Fatalf("QueueLen = %d, want %d", got, auditQueueSize)
	}
}

// Without a hook — metrics switched off — a drop is exactly what it was before:
// logged, counted internally, never a panic.
func TestStoreAuditorDropsWithNoHook(t *testing.T) {
	auditor := NewStoreAuditor(&queueSink{}, nil, nil)
	for range auditQueueSize + 3 {
		auditor.Record(context.Background(), Event{Verb: "get", Status: 200})
	}
	if got := auditor.dropped.Load(); got != 3 {
		t.Fatalf("dropped = %d, want 3", got)
	}
}
