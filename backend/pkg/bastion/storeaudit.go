package bastion

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/auditpolicy"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

// Audit buffering. Records are batched because a proxied call must never wait
// on a database round trip — the trail is written beside the request, not in
// front of it.
const (
	auditQueueSize     = 4096
	auditBatchSize     = 128
	auditFlushInterval = 2 * time.Second
	auditWriteTimeout  = 10 * time.Second
)

// AuditSink persists a batch of audit records.
type AuditSink interface {
	AppendAuditEvents(ctx context.Context, events []db.AuditEvent) error
}

// StoreAuditor persists the audit trail asynchronously. Record never blocks:
// if the queue is full the record is dropped and the drop is itself logged, so
// a database outage degrades the trail rather than the gateway.
//
// Dropping is the honest trade here. The alternative — blocking the proxy —
// would turn a slow database into an outage for every kubectl in the fleet, and
// the structured-log auditor still has the record either way.
type StoreAuditor struct {
	sink   AuditSink
	logger *slog.Logger
	// policy decides which verbs reach the table. It is applied *here* and not in
	// the SlogAuditor on purpose: the selection exists to keep a queryable table
	// from filling with reads nobody queries, and the structured log — which is
	// what a SIEM tails — stays complete either way. Turning down the table is a
	// storage decision; turning down the log would be an audit decision.
	policy *auditpolicy.Policy

	queue chan db.AuditEvent

	dropped    atomic.Int64
	suppressed atomic.Int64
	done       chan struct{}
}

// NewStoreAuditor builds the persistent auditor. Run must be started for it to
// write anything. A nil policy records every verb.
func NewStoreAuditor(sink AuditSink, logger *slog.Logger, policy *auditpolicy.Policy) *StoreAuditor {
	if logger == nil {
		logger = slog.Default()
	}
	return &StoreAuditor{
		sink:   sink,
		logger: logger,
		policy: policy,
		queue:  make(chan db.AuditEvent, auditQueueSize),
		done:   make(chan struct{}),
	}
}

// Record enqueues an audit event. It is safe from any goroutine and never
// blocks.
func (a *StoreAuditor) Record(ctx context.Context, event Event) {
	if !a.policy.Records(event.Verb, event.Status, event.Error != "", event.Streaming) {
		a.suppressed.Add(1)
		return
	}
	select {
	case a.queue <- toAuditRow(ctx, event):
	default:
		// Log the first drop and then every thousandth, so a sustained outage
		// does not turn the log itself into the problem.
		if n := a.dropped.Add(1); n == 1 || n%1000 == 0 {
			a.logger.Error("audit queue is full, dropping records",
				slog.Int64("dropped_total", n),
			)
		}
	}
}

// Run drains the queue until the context is cancelled, then flushes what is
// left. It blocks and is meant to be started on its own goroutine.
func (a *StoreAuditor) Run(ctx context.Context) {
	defer close(a.done)

	ticker := time.NewTicker(auditFlushInterval)
	defer ticker.Stop()

	batch := make([]db.AuditEvent, 0, auditBatchSize)

	for {
		select {
		case <-ctx.Done():
			// Drain whatever is already queued rather than losing the tail of
			// the trail on shutdown.
			for {
				select {
				case event := <-a.queue:
					batch = append(batch, event)
					if len(batch) >= auditBatchSize {
						batch = a.flush(batch)
					}
					continue
				default:
				}
				break
			}
			a.flush(batch)
			return

		case event := <-a.queue:
			batch = append(batch, event)
			if len(batch) >= auditBatchSize {
				batch = a.flush(batch)
			}

		case <-ticker.C:
			batch = a.flush(batch)
		}
	}
}

// Wait blocks until Run has finished flushing, for an orderly shutdown.
func (a *StoreAuditor) Wait() { <-a.done }

// Suppressed is how many records the verb policy kept out of the table. It is
// not a failure count — it is the setting working — but it is worth being able
// to read, because "the trail looks empty" and "the trail is switched off" are
// answered by very different actions.
func (a *StoreAuditor) Suppressed() int64 { return a.suppressed.Load() }

// flush writes a batch and returns an empty slice to keep filling.
func (a *StoreAuditor) flush(batch []db.AuditEvent) []db.AuditEvent {
	if len(batch) == 0 {
		return batch
	}

	// A detached context: the trail for work that already happened must still
	// be written even when the server is shutting down.
	ctx, cancel := context.WithTimeout(context.Background(), auditWriteTimeout)
	defer cancel()

	if err := a.sink.AppendAuditEvents(ctx, batch); err != nil {
		a.logger.Error("could not persist audit records",
			slog.Int("count", len(batch)),
			slog.String("error", err.Error()),
		)
	}
	return batch[:0]
}

// toAuditRow flattens an in-flight event into its stored form.
//
// The caller's address and user agent come off the **context** rather than off
// the event: they are the same two facts for every record a request produces,
// and this is the one place every record passes through. See source.go for why
// they do not live on Event.
func toAuditRow(ctx context.Context, event Event) db.AuditEvent {
	source := SourceFrom(ctx).Truncate()
	at := event.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return db.AuditEvent{
		At:                 at,
		UserID:             event.UserID,
		Username:           event.Username,
		ClusterID:          event.ClusterID,
		Cluster:            event.Cluster,
		Verb:               event.Verb,
		Method:             event.Method,
		Path:               event.Path,
		Namespace:          event.Namespace,
		Resource:           event.Resource,
		ImpersonatedUser:   event.ImpersonatedUser,
		ImpersonatedGroups: strings.Join(event.ImpersonatedGroups, ","),
		Status:             event.Status,
		DurationMS:         event.Duration.Milliseconds(),
		Streaming:          event.Streaming,
		Phase:              event.Phase,
		BytesOut:           event.BytesOut,
		BytesIn:            event.BytesIn,
		SessionID:          event.SessionID,
		GuardrailPolicy:    event.GuardrailPolicy,
		GuardrailAction:    event.GuardrailAction,
		Error:              event.Error,
		Diff:               string(event.Diff),
		SourceAddr:         source.Addr,
		UserAgent:          source.UserAgent,
	}
}

// MultiAuditor fans one event out to several auditors. The structured log and
// the database are both wanted: the log is what a SIEM already tails, the table
// is what the UI queries.
type MultiAuditor struct {
	auditors []Auditor
}

// NewMultiAuditor combines auditors, skipping nil ones so wiring stays simple.
func NewMultiAuditor(auditors ...Auditor) *MultiAuditor {
	out := make([]Auditor, 0, len(auditors))
	for _, auditor := range auditors {
		if auditor != nil {
			out = append(out, auditor)
		}
	}
	return &MultiAuditor{auditors: out}
}

// Record forwards to every auditor.
func (m *MultiAuditor) Record(ctx context.Context, event Event) {
	for _, auditor := range m.auditors {
		auditor.Record(ctx, event)
	}
}
