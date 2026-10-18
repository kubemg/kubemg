package bastion

import (
	"context"
	"sync/atomic"
)

/*
 * One inbound request, as the audit trail sees it.
 *
 * The trail promises every attempt anyone made, refusals included. The proxy
 * keeps that promise for everything it carries, but the API layer refuses a
 * great deal before anything reaches it — a namespace outside the grant, a
 * cluster-wide list for a scoped grant, a kind KubeMG will not author — and
 * those answers used to be recorded nowhere. The fix is one middleware that
 * writes a row for a 4xx (see api.auditRefusals), and the one thing it cannot
 * know on its own is whether the refusal it is looking at is already in the
 * trail: a cluster's own 403 handed back through Call was recorded by Call, and
 * recording it again would put one refusal in the trail twice.
 *
 * So the request carries an Attempt, and every record written under that
 * request's context notes itself on it. A refusal already recorded means the
 * middleware stays quiet; none means the answer was KubeMG's own and nobody has
 * written it down yet.
 */

// RefusedByKubeMG marks a record whose refusal KubeMG made itself, before or
// instead of asking the cluster. An empty RefusedBy on a refused record means
// the cluster (or the tunnel to it) gave the answer.
const RefusedByKubeMG = "kubemg"

type attemptKey struct{}

// Attempt is the audit state of one inbound request.
type Attempt struct {
	refusalRecorded atomic.Bool
}

// WithAttempt starts tracking a request. Records written under the returned
// context, from any goroutine, note themselves on the Attempt.
func WithAttempt(ctx context.Context) (context.Context, *Attempt) {
	attempt := &Attempt{}
	return context.WithValue(ctx, attemptKey{}, attempt), attempt
}

// RefusalRecorded reports whether a refused or failed call has already been
// written to the trail under this request.
func (a *Attempt) RefusalRecorded() bool {
	return a != nil && a.refusalRecorded.Load()
}

func noteRecorded(ctx context.Context, event Event) {
	if ctx == nil {
		return
	}
	attempt, _ := ctx.Value(attemptKey{}).(*Attempt)
	if attempt == nil {
		return
	}
	if event.Status >= 400 || event.Error != "" {
		attempt.refusalRecorded.Store(true)
	}
}

// notingAuditor notes each record on its request's Attempt before passing it on.
type notingAuditor struct {
	inner Auditor
}

func (n notingAuditor) Record(ctx context.Context, event Event) {
	noteRecorded(ctx, event)
	n.inner.Record(ctx, event)
}

// NoteAttempts wraps an auditor so its records mark their request's Attempt.
// Nil stays nil, so a server wired without an auditor still reads as having
// none.
func NoteAttempts(auditor Auditor) Auditor {
	switch auditor.(type) {
	case nil:
		return nil
	case notingAuditor:
		return auditor
	}
	return notingAuditor{inner: auditor}
}

// StripCredentials removes KubeMG's own credential parameters from a raw query
// string, so a refusal recorded against a WebSocket route never carries the
// ticket that opened it.
func StripCredentials(rawQuery string) string {
	return strippedQuery(rawQuery)
}
