// Package auditpolicy holds the runtime decision about what reaches the audit
// table and what gets recorded.
//
// It exists as a package of its own because of where it has to be read. The
// setting lives in the database and is edited from the Settings page, which is
// the HTTP layer's business; the decision is consulted once per proxied call and
// once per opened shell, which is the gateway's hot path. Neither side may
// depend on the other, and the hot path must never take a database round trip to
// find out whether to write a row — so the value is resolved by the HTTP layer,
// published here as an immutable snapshot, and read lock-free.
//
// The zero-value Policy records everything. That matters more than it looks: a
// server whose settings could not be read, or one wired before the refresher has
// run, keeps a complete trail rather than a silent one.
package auditpolicy

import (
	"sort"
	"strings"
	"sync/atomic"
)

// Verbs an operator may switch off. These are the ordinary request verbs, and
// the reason to switch any of them off is volume: on a busy fleet `list` and
// `get` are the overwhelming majority of the trail, and an auditor reading it
// is almost never reading them.
//
// The list is deliberately not "every verb VerbFor can produce". See Snapshot.
var Verbs = []string{
	"get",
	"list",
	"watch",
	"create",
	"update",
	"patch",
	"delete",
	"log",
	"exec",
	"attach",
	"portforward",
}

// alwaysRecorded are the verbs no setting can suppress. Two kinds of thing are
// on this list and both are here for the same reason: a control that can hide
// them is not an audit control, it is a way to act without a trail.
//
//   - replay / recording-get / recording-delete: watching or destroying a
//     recording of somebody else's production shell. A surveillance capability
//     with no trail of its own is the first thing an auditor asks about, so the
//     capability's own records are not negotiable.
//   - secret-reveal: a Secret's value leaving the cluster for a browser. It is
//     the one read that hands out a credential rather than describing one, and a
//     setting that hides it would turn the capability into a way to read every
//     password in a namespace with nothing to show for it afterwards.
//   - jit-*: somebody being granted a stronger role than they hold, and for how
//     long. These are the *fewest* rows in the table and the ones an auditor opens
//     first — a suppressible privilege escalation record would make the whole
//     approval workflow decorative.
//
// Every verb here is also absent from Verbs above, so `suppressible` would answer
// false for it anyway. Listing them is belt and braces: it makes the intent
// explicit, and it survives somebody later adding one of these to Verbs.
var alwaysRecorded = map[string]bool{
	"replay":           true,
	"recording-get":    true,
	"recording-delete": true,
	"secret-reveal":    true,
	"jit-request":      true,
	"jit-approve":      true,
	"jit-reject":       true,
	"jit-revoke":       true,
	"jit-expire":       true,
}

// Snapshot is one resolved policy. It is replaced wholesale rather than mutated,
// so a reader always sees a coherent set rather than a half-applied change.
type Snapshot struct {
	// verbs is the set that reaches the table. Nil means "every verb", which is
	// distinct from an empty set meaning "none" — a distinction the settings layer
	// preserves, because an operator clearing the field wants the default back and
	// an operator unticking every box wants what they asked for.
	verbs map[string]bool
	// recordSessions is whether interactive sessions are teed into a recording.
	// It can only ever turn recording off: a process started without a recording
	// directory has nowhere to write, and no database row changes that.
	recordSessions bool
}

// NewSnapshot resolves a policy from the enabled verb list and the session
// recording switch. A nil verb slice means every verb; an empty non-nil slice
// means none of the suppressible ones.
func NewSnapshot(verbs []string, recordSessions bool) Snapshot {
	snapshot := Snapshot{recordSessions: recordSessions}
	if verbs == nil {
		return snapshot
	}
	snapshot.verbs = make(map[string]bool, len(verbs))
	for _, verb := range verbs {
		if verb = strings.ToLower(strings.TrimSpace(verb)); verb != "" {
			snapshot.verbs[verb] = true
		}
	}
	return snapshot
}

// EnabledVerbs returns the suppressible verbs this snapshot keeps, sorted. It is
// nil when every verb is kept, which is how the settings API reports "unset"
// rather than "all of them ticked".
func (s Snapshot) EnabledVerbs() []string {
	if s.verbs == nil {
		return nil
	}
	out := make([]string, 0, len(s.verbs))
	for verb := range s.verbs {
		out = append(out, verb)
	}
	sort.Strings(out)
	return out
}

// RecordSessions reports whether interactive sessions are being recorded.
func (s Snapshot) RecordSessions() bool { return s.recordSessions }

// Records reports whether an audit record with this verb, status and error
// should be persisted.
//
// Three things override the verb selection, and they are what keep this from
// being a way to act unobserved:
//
//   - a refusal or an error is always recorded. The rows an auditor opens first
//     are the ones that did not succeed, and there is no volume argument for
//     dropping them — on a healthy fleet there are almost none.
//   - a streaming call is always recorded. An exec, an attach, a port-forward or
//     a followed log is a session rather than a request; it is the most sensitive
//     line in the trail and the rarest.
//   - the verbs in alwaysRecorded, which are KubeMG auditing itself.
func (s Snapshot) Records(verb string, status int, failed bool, streaming bool) bool {
	if s.verbs == nil {
		return true
	}
	if failed || status >= 400 || streaming {
		return true
	}
	verb = strings.ToLower(strings.TrimSpace(verb))
	if alwaysRecorded[verb] {
		return true
	}
	// A verb nothing can suppress — one this build does not know about — is
	// recorded. An unknown verb is the last thing to start dropping silently.
	if !suppressible(verb) {
		return true
	}
	return s.verbs[verb]
}

// suppressible reports whether a verb is one the setting governs at all.
func suppressible(verb string) bool {
	for _, known := range Verbs {
		if known == verb {
			return true
		}
	}
	return false
}

// Policy is the published snapshot, safe to read from any goroutine.
type Policy struct {
	current atomic.Pointer[Snapshot]
}

// New returns a policy that records everything, which is what a server does
// until the settings have been read.
func New() *Policy {
	p := &Policy{}
	p.Store(NewSnapshot(nil, true))
	return p
}

// Store publishes a snapshot. It replaces whatever was there.
func (p *Policy) Store(snapshot Snapshot) {
	if p == nil {
		return
	}
	p.current.Store(&snapshot)
}

// Snapshot returns the current policy. A nil Policy — which is what a test or a
// server wired without one has — records everything.
func (p *Policy) Snapshot() Snapshot {
	if p == nil {
		return NewSnapshot(nil, true)
	}
	if current := p.current.Load(); current != nil {
		return *current
	}
	return NewSnapshot(nil, true)
}

// Records is the hot-path question: should this record be persisted?
func (p *Policy) Records(verb string, status int, failed bool, streaming bool) bool {
	return p.Snapshot().Records(verb, status, failed, streaming)
}

// RecordSessions is the other hot-path question, asked once when a shell opens.
func (p *Policy) RecordSessions() bool { return p.Snapshot().RecordSessions() }
