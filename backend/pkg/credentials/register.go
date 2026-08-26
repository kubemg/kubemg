// Package credentials holds the runtime answer to one question the gateway asks
// on every proxied call: has this kubeconfig been withdrawn?
//
// It exists as a package of its own for the reason auditpolicy does, and the
// shape is the same. The register itself is a database table written from the
// HTTP layer; the question is asked in bastion's authorize step, which must not
// take a round trip to answer it — a `kubectl get pods` in a loop would turn one
// indexed select into a select per call, in front of the very path this product
// exists to keep fast. So the revoked set is resolved by the HTTP layer,
// published here as an immutable snapshot, and read lock-free.
//
// The zero value knows of no revocation, and that is the load-bearing decision:
// a server that has not yet read its register, or one whose database is
// unreachable, must fail open on *nothing*. An unreadable register means "no
// revocations are known", never "refuse every credential" — a blip that locks a
// whole fleet out of kubectl is worse than one that briefly honours a withdrawn
// file, which still expires on its own clock.
package credentials

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// touchInterval bounds how often one credential's last-used stamp is written.
// It is the machine token's rule and for the machine token's reason: this sits
// on the hot path, and knowing a kubeconfig was used in the last few minutes
// answers the question the column exists for — which of these is still out there
// — as well as knowing the second would.
const touchInterval = 5 * time.Minute

// touchTimeout bounds the background write. It uses a context of its own
// because the call that triggered it is long gone by the time this matters, and
// an observation must never outlive or delay what it observed.
const touchTimeout = 5 * time.Second

// Snapshot is one resolved revocation set. It is replaced wholesale rather than
// mutated, so a reader always sees a coherent set rather than a half-applied
// change.
type Snapshot struct {
	revoked map[string]bool
}

// NewSnapshot resolves a set from the token ids that have been withdrawn.
func NewSnapshot(tokenIDs []string) Snapshot {
	if len(tokenIDs) == 0 {
		return Snapshot{}
	}
	revoked := make(map[string]bool, len(tokenIDs))
	for _, id := range tokenIDs {
		if id != "" {
			revoked[id] = true
		}
	}
	return Snapshot{revoked: revoked}
}

// Revoked reports whether a credential carrying this token id has been
// withdrawn. An empty id is a credential minted before the register existed;
// it is not revoked, because nothing here can name it.
func (s Snapshot) Revoked(tokenID string) bool {
	return tokenID != "" && s.revoked[tokenID]
}

// Size is how many credentials this snapshot withdraws.
func (s Snapshot) Size() int { return len(s.revoked) }

// Toucher records that a credential was used. It is deliberately narrow: the
// register knows nothing about the store, and a failure here can never fail the
// call it was observing.
type Toucher func(ctx context.Context, tokenID string, at time.Time)

// Register is the published snapshot plus the use recorder, safe to read from
// any goroutine.
type Register struct {
	current atomic.Pointer[Snapshot]

	mu      sync.Mutex
	toucher Toucher
	touched map[string]time.Time
}

// New returns a register that knows of no revocation, which is what a server
// has until its first read of the table.
func New() *Register {
	r := &Register{touched: map[string]time.Time{}}
	r.Store(NewSnapshot(nil))
	return r
}

// Store publishes a snapshot, replacing whatever was there.
func (r *Register) Store(snapshot Snapshot) {
	if r == nil {
		return
	}
	r.current.Store(&snapshot)
}

// Snapshot returns the current set. A nil Register — which is what a test or a
// server wired without one has — revokes nothing.
func (r *Register) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	if current := r.current.Load(); current != nil {
		return *current
	}
	return Snapshot{}
}

// SetToucher installs the writer that records a credential's use. Left unset —
// as the tests and a server wired without persistence leave it — Observe still
// answers the revocation question and records nothing.
func (r *Register) SetToucher(toucher Toucher) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toucher = toucher
}

// Revoked is the hot-path question, asked without recording anything.
func (r *Register) Revoked(tokenID string) bool {
	return r.Snapshot().Revoked(tokenID)
}

// Observe is the same question asked by a call that is about to be served: it
// reports whether the credential has been withdrawn and, if it has not, records
// the use at most once per touchInterval and off this goroutine.
func (r *Register) Observe(tokenID string) bool {
	if r == nil || tokenID == "" {
		return false
	}
	if r.Revoked(tokenID) {
		return true
	}
	r.touch(tokenID, time.Now().UTC())
	return false
}

func (r *Register) touch(tokenID string, now time.Time) {
	r.mu.Lock()
	toucher := r.toucher
	if toucher == nil {
		r.mu.Unlock()
		return
	}
	last, seen := r.touched[tokenID]
	if seen && now.Sub(last) < touchInterval {
		r.mu.Unlock()
		return
	}
	r.touched[tokenID] = now
	r.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), touchTimeout)
		defer cancel()
		toucher(ctx, tokenID, now)
	}()
}
