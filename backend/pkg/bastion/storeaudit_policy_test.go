package bastion

import (
	"context"
	"testing"

	"github.com/kubemg/kubemg/backend/pkg/auditpolicy"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// queueSink counts what the auditor actually enqueued.
type queueSink struct{ rows []db.AuditEvent }

func (s *queueSink) AppendAuditEvents(_ context.Context, events []db.AuditEvent) error {
	s.rows = append(s.rows, events...)
	return nil
}

// drain reads whatever Record enqueued without running the flush loop, so the
// test asserts on the gate rather than on batching timing.
func drain(auditor *StoreAuditor) []db.AuditEvent {
	out := []db.AuditEvent{}
	for {
		select {
		case event := <-auditor.queue:
			out = append(out, event)
		default:
			return out
		}
	}
}

func TestStoreAuditorHonoursTheVerbSelection(t *testing.T) {
	policy := auditpolicy.New()
	policy.Store(auditpolicy.NewSnapshot([]string{"delete"}, true))

	auditor := NewStoreAuditor(&queueSink{}, nil, policy)
	auditor.Record(context.Background(), Event{Verb: "list", Status: 200})
	auditor.Record(context.Background(), Event{Verb: "delete", Status: 200})

	queued := drain(auditor)
	if len(queued) != 1 || queued[0].Verb != "delete" {
		t.Fatalf("only the selected verb should reach the table, got %v", queued)
	}
	if auditor.Suppressed() != 1 {
		t.Fatalf("the suppression should be countable, got %d", auditor.Suppressed())
	}
}

// The floor is the security property: the selection is a volume control, not a
// way to act without a trail.
func TestStoreAuditorAlwaysRecordsTheThingsThatMatter(t *testing.T) {
	policy := auditpolicy.New()
	policy.Store(auditpolicy.NewSnapshot([]string{}, false))

	auditor := NewStoreAuditor(&queueSink{}, nil, policy)
	auditor.Record(context.Background(), Event{Verb: "list", Status: 403})
	auditor.Record(context.Background(), Event{Verb: "get", Error: "no tunnel"})
	auditor.Record(context.Background(), Event{Verb: "exec", Status: 101, Streaming: true})
	auditor.Record(context.Background(), Event{Verb: "replay", Status: 200})
	// The one thing the selection is for.
	auditor.Record(context.Background(), Event{Verb: "list", Status: 200})

	if queued := drain(auditor); len(queued) != 4 {
		t.Fatalf("refusals, sessions and recording access are never suppressed, got %v", queued)
	}
}

func TestNilPolicyRecordsEverything(t *testing.T) {
	auditor := NewStoreAuditor(&queueSink{}, nil, nil)
	auditor.Record(context.Background(), Event{Verb: "list", Status: 200})
	if len(drain(auditor)) != 1 {
		t.Fatal("a server wired without a policy keeps a complete trail")
	}
}
