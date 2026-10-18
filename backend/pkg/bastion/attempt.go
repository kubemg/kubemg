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
 * request's context notes its status on it. The middleware stays quiet only
 * when the status it is answering with is one already recorded — the cluster's
 * answer being handed back. Any recorded refusal at all is not enough: a
 * handler that reads first and refuses after (Helm discovery 404s an absent
 * group, then the pre-flight refuses a cluster-scoped object with 403; a fan-out
 * where one namespace is denied, then an answer of KubeMG's own) would hide its
 * own refusal behind an unrelated one, and a caller could pick the request
 * shape that does that. What remains is a same-status collision, which loses
 * the second reason but never the fact of a refusal.
 */

// RefusedByKubeMG marks a record whose refusal KubeMG made itself, before or
// instead of asking the cluster. An empty RefusedBy on a refused record means
// the cluster (or the tunnel to it) gave the answer.
const RefusedByKubeMG = "kubemg"

type attemptKey struct{}

// Attempt is the audit state of one inbound request: which 4xx statuses have
// already been written to the trail under it.
type Attempt struct {
	recorded [100]atomic.Bool
}

// WithAttempt starts tracking a request. Records written under the returned
// context, from any goroutine, note themselves on the Attempt.
func WithAttempt(ctx context.Context) (context.Context, *Attempt) {
	attempt := &Attempt{}
	return context.WithValue(ctx, attemptKey{}, attempt), attempt
}

// RefusalRecorded reports whether a refusal with this status has already been
// written to the trail under this request.
func (a *Attempt) RefusalRecorded(status int) bool {
	if a == nil || status < 400 || status > 499 {
		return false
	}
	return a.recorded[status-400].Load()
}

func noteRecorded(ctx context.Context, event Event) {
	if ctx == nil {
		return
	}
	attempt, _ := ctx.Value(attemptKey{}).(*Attempt)
	if attempt == nil {
		return
	}
	if event.Status >= 400 && event.Status <= 499 {
		attempt.recorded[event.Status-400].Store(true)
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
