// Package auditforward pushes the complete audit trail to an external
// collector — a SIEM that cannot come and tail this server's own log stream.
//
// It is the third consumer of the same records, beside the structured log and
// the database table, and it is modelled on the second: a bounded queue, a
// background drain, and a drop rather than a block when the queue is full. A
// slow SIEM must never become a slow kubectl, which is the same trade the
// database sink already makes and for the same reason.
//
// The verb selection is deliberately *not* applied here. `audit_verbs` narrows
// what reaches the queryable table, which is a storage decision; narrowing what
// leaves for a SIEM would be an audit decision, and the structured log does not
// make it either.
package auditforward

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/bastion"
	"github.com/kubemg/kubemg/backend/pkg/db"
)

const (
	// queueSize matches the database sink's. The trail arrives at the same rate
	// for both, and a forwarder allowed to buffer far more would simply hold a
	// larger backlog of records nobody has yet noticed are not arriving.
	queueSize = 4096
	batchSize = 128
	// flushInterval bounds how long a record waits. A SIEM's value is largely in
	// how quickly a refusal shows up on somebody's screen, so this is short.
	flushInterval = 2 * time.Second
	// reloadInterval refreshes the destination list. A save through the console
	// reloads immediately; this tick exists for the other replica, whose memory
	// knows nothing about a change saved through its sibling.
	reloadInterval = 30 * time.Second
	// healthInterval rate-limits the delivery-health write. Recording every
	// flush would mean a row update every two seconds per destination, which
	// buys nothing: what an operator needs to know is whether it is working
	// *now*, not the timestamp of the last of nine hundred identical successes.
	healthInterval = time.Minute
	// healthWriteTimeout bounds the delivery-health row update.
	healthWriteTimeout = 5 * time.Second
)

// connKey is the part of a destination a held connection depends on. Comparing
// whole rows here would be wrong in a way that is easy to miss: writing
// delivery health bumps updated_at, so every successful flush would look like
// an edit and drop the connection it had just used.
func connKey(dest db.AuditForwarder) string {
	return fmt.Sprintf("%s|%s|%d|%t|%t|%s",
		dest.Protocol, dest.Host, dest.Port,
		dest.OctetCounting, dest.TLSInsecureSkipVerify, dest.TLSCABundle)
}

// Store is the slice of the database this package needs.
type Store interface {
	ListAuditForwarders(ctx context.Context) ([]db.AuditForwarder, error)
	RecordAuditForwarderAttempt(ctx context.Context, id uint, status, message string) error
}

// Options configures a Forwarder.
type Options struct {
	Store  Store
	Logger *slog.Logger
}

// Forwarder ships audit records to every enabled destination.
type Forwarder struct {
	store  Store
	logger *slog.Logger

	queue chan record
	done  chan struct{}

	// active is read on the hot path so that a server with no destination
	// configured — which is every server until somebody configures one — does
	// no work at all per proxied call.
	active atomic.Bool

	reload  chan struct{}
	dropped atomic.Int64

	// senders are keyed by destination id and hold their connections open
	// across flushes. Only the drain goroutine touches them.
	senders map[uint]*sender
	// health remembers the last outcome written per destination, so an
	// unchanged status is not rewritten on every flush.
	health map[uint]healthState

	mu    sync.Mutex
	dests []db.AuditForwarder
}

type healthState struct {
	status  string
	message string
	at      time.Time
}

// New builds a forwarder. Run must be started for it to deliver anything.
func New(opts Options) *Forwarder {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Forwarder{
		store:   opts.Store,
		logger:  logger,
		queue:   make(chan record, queueSize),
		done:    make(chan struct{}),
		reload:  make(chan struct{}, 1),
		senders: map[uint]*sender{},
		health:  map[uint]healthState{},
	}
}

// Record enqueues an audit event. It never blocks and never fails a caller.
func (f *Forwarder) Record(ctx context.Context, event bastion.Event) {
	if f == nil || !f.active.Load() {
		return
	}
	rec, ok := encode(ctx, event)
	if !ok {
		return
	}
	select {
	case f.queue <- rec:
	default:
		// Log the first drop and then every thousandth, so a sustained outage
		// does not turn the log itself into the problem.
		if n := f.dropped.Add(1); n == 1 || n%1000 == 0 {
			f.logger.Error("audit forward queue is full, dropping records",
				slog.Int64("dropped_total", n),
			)
		}
	}
}

// Reload asks the drain to re-read its destinations now. It is what a save
// through the console calls, and it never blocks: a reload already pending is
// the same reload.
func (f *Forwarder) Reload() {
	if f == nil {
		return
	}
	select {
	case f.reload <- struct{}{}:
	default:
	}
}

// Dropped is how many records never left. It is not the same as a delivery
// failure — these were discarded before any destination saw them — and it is
// worth reading separately for exactly that reason.
func (f *Forwarder) Dropped() int64 {
	if f == nil {
		return 0
	}
	return f.dropped.Load()
}

// Run drains the queue until the context is cancelled, then flushes what is
// left. It blocks and is meant to be started on its own goroutine.
func (f *Forwarder) Run(ctx context.Context) {
	defer close(f.done)
	defer f.closeSenders()

	f.refresh(ctx)

	flush := time.NewTicker(flushInterval)
	defer flush.Stop()
	reload := time.NewTicker(reloadInterval)
	defer reload.Stop()

	batch := make([]record, 0, batchSize)

	for {
		select {
		case <-ctx.Done():
			// Drain what is already queued rather than losing the tail of the
			// trail on shutdown.
			for {
				select {
				case rec := <-f.queue:
					batch = append(batch, rec)
					if len(batch) >= batchSize {
						batch = f.flush(batch)
					}
					continue
				default:
				}
				break
			}
			f.flush(batch)
			return

		case rec := <-f.queue:
			batch = append(batch, rec)
			if len(batch) >= batchSize {
				batch = f.flush(batch)
			}

		case <-flush.C:
			batch = f.flush(batch)

		case <-reload.C:
			f.refresh(ctx)

		case <-f.reload:
			f.refresh(ctx)
		}
	}
}

// Wait blocks until Run has finished flushing, for an orderly shutdown.
func (f *Forwarder) Wait() {
	if f == nil {
		return
	}
	<-f.done
}

// refresh re-reads the destination list.
//
// A read failure keeps the previous list rather than emptying it, the rule the
// credential snapshot and the audit policy already follow: a transient database
// blip must not silently stop the trail leaving the platform.
func (f *Forwarder) refresh(ctx context.Context) {
	if f.store == nil {
		return
	}
	all, err := f.store.ListAuditForwarders(ctx)
	if err != nil {
		f.logger.Error("could not read audit forwarders", slog.String("error", err.Error()))
		return
	}
	enabled := make([]db.AuditForwarder, 0, len(all))
	for _, dest := range all {
		if dest.Enabled && dest.Kind == db.ForwarderSyslog {
			enabled = append(enabled, dest)
		}
	}

	f.mu.Lock()
	f.dests = enabled
	f.mu.Unlock()
	f.active.Store(len(enabled) > 0)

	// Drop the held connection of anything that went away or was edited: a
	// sender keeps its destination's settings, so a socket outliving an address
	// change would keep delivering to the old collector.
	keep := make(map[uint]db.AuditForwarder, len(enabled))
	for _, dest := range enabled {
		keep[dest.ID] = dest
	}
	for id, held := range f.senders {
		next, still := keep[id]
		if !still || connKey(next) != connKey(held.dest) {
			held.close()
			delete(f.senders, id)
		}
	}
}

func (f *Forwarder) destinations() []db.AuditForwarder {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dests
}

// flush delivers a batch to every destination and returns an empty slice to
// keep filling. A destination that fails does not stop the others.
func (f *Forwarder) flush(batch []record) []record {
	if len(batch) == 0 {
		return batch
	}
	for _, dest := range f.destinations() {
		held, ok := f.senders[dest.ID]
		if !ok {
			held = newSender(dest)
			f.senders[dest.ID] = held
		}
		if err := held.send(batch); err != nil {
			held.close()
			f.note(dest, db.ForwarderStatusError, err.Error())
			continue
		}
		f.note(dest, db.ForwarderStatusOK, "")
	}
	return batch[:0]
}

// note records delivery health, rate-limited unless the outcome changed. A
// change is always written immediately: "it started failing" is the whole
// reason this column exists.
func (f *Forwarder) note(dest db.AuditForwarder, status, message string) {
	if status == db.ForwarderStatusError {
		f.logger.Error("could not forward audit records",
			slog.String("forwarder", dest.Name),
			slog.String("address", dest.Address()),
			slog.String("error", message),
		)
	}

	now := time.Now()
	last, seen := f.health[dest.ID]
	if seen && last.status == status && last.message == message && now.Sub(last.at) < healthInterval {
		return
	}
	f.health[dest.ID] = healthState{status: status, message: message, at: now}

	if f.store == nil {
		return
	}
	// A detached context: health for work that already happened must still be
	// recorded when the server is shutting down.
	ctx, cancel := context.WithTimeout(context.Background(), healthWriteTimeout)
	defer cancel()
	if err := f.store.RecordAuditForwarderAttempt(ctx, dest.ID, status, message); err != nil {
		f.logger.Error("could not record audit forwarder health",
			slog.String("forwarder", dest.Name),
			slog.String("error", err.Error()),
		)
	}
}

func (f *Forwarder) closeSenders() {
	for id, held := range f.senders {
		held.close()
		delete(f.senders, id)
	}
}

// Probe delivers one synthetic record to a destination and reports what
// happened. It is what the console's Test button calls, and it dials on its own
// rather than borrowing the running sender: an operator testing a destination
// wants to know whether *this configuration* connects, not whether a connection
// opened before their edit is still up.
func Probe(ctx context.Context, dest db.AuditForwarder) error {
	rec, _ := encode(ctx, bastion.Event{
		At:       time.Now().UTC(),
		Username: "kubemg",
		Verb:     "test",
		Method:   "POST",
		Path:     "/api/v1/audit/forwarders/test",
		Status:   200,
	})
	probe := newSender(dest)
	defer probe.close()
	return probe.send([]record{rec})
}
