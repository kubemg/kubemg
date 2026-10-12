package auth

import (
	"sync"
	"time"
)

/*
 * Slowing down password guessing.
 *
 * Every route that checks a password — the local sign-in, the directory
 * sign-in, the change-password form's current password — answers a guess in
 * the time a bcrypt comparison takes, and nothing used to stop the next one.
 * The throttle counts failures twice over: per *account*, which is what stops a
 * guesser working through one person's password, and per *source address*,
 * which is what stops one address spraying a common password across many
 * accounts. Either being locked refuses the attempt, and the password is not
 * checked while it is — a locked route that still compared would answer the
 * guess, only later.
 *
 * Four rules shape it:
 *
 *   - **It is a delay, not a lockout.** A few attempts are free, then each
 *     further failure locks the key for twice as long as the last, up to a
 *     ceiling. A permanent lock would hand anybody who knows a username a way
 *     to keep its owner out; a capped delay costs the owner minutes and a
 *     guesser most of their rate.
 *   - **An attempt is reserved before the password is checked**, and an
 *     attempt still being checked counts against the free allowance. A check
 *     takes as long as bcrypt does, and counting only finished failures would
 *     let a guesser send a few hundred at once and have every one of them
 *     checked before the first was recorded. So a key never has more checks in
 *     flight than it has free attempts left, and once those are spent its
 *     attempts run one at a time, each failure re-locking it.
 *   - **An unknown username counts exactly like a known one.** The account key
 *     is the name as typed, not a row, so a 429 says nothing about whether the
 *     account exists — the sign-in route's one-answer rule still holds.
 *   - **A success clears the account, never the source.** Otherwise one valid
 *     login, interleaved, would reset an address that is guessing at everybody
 *     else.
 *
 * State is held in this process. KubeMG runs one replica, and a second would
 * only give a guesser a second budget rather than none; a restart forgets the
 * counts, which costs a guesser's progress nothing it could not have spent
 * anyway at the ceiling.
 *
 * Each table is bounded, and the bound is not a way to switch the throttle off.
 * A count is only ever forgotten once its key has been quiet for Forget —
 * dropping a recent one early, locked or not, would hand its key a fresh
 * allowance, and flooding the table with names would be the way to ask for
 * one. A key with no failures holds no entry at all. Past the bound, every key
 * that cannot be tracked shares one overflow entry, so guessing past it is
 * throttled collectively rather than not at all. The cost of that choice is
 * stated rather than hidden: once more than MaxThrottleKeys names have failed
 * within Forget, a person whose name is not already tracked shares the
 * overflow entry's lock. That needs hundreds of thousands of failed sign-ins
 * an hour, and the throttle is then failing closed on purpose. Making room is
 * a sweep of the whole table, so it runs at most once every sweepEvery: a
 * full table answers a new key in constant time rather than in a scan.
 */

// ThrottlePolicy is how one key is slowed down.
type ThrottlePolicy struct {
	// Free is how many consecutive failures pass before the first lock.
	Free int
	// Base is the first lock; each further failure doubles it.
	Base time.Duration
	// Max caps a single lock.
	Max time.Duration
	// Forget is how long a key must be quiet before its count is dropped.
	Forget time.Duration
}

// The policies the routes use. An account gets few free tries because one
// person mistypes a handful of times, not dozens; an address gets more because
// several people can share one (an office NAT, a bastion host).
var (
	AccountThrottle = ThrottlePolicy{Free: 5, Base: 30 * time.Second, Max: 15 * time.Minute, Forget: time.Hour}
	SourceThrottle  = ThrottlePolicy{Free: 20, Base: time.Minute, Max: time.Hour, Forget: time.Hour}
)

// MaxThrottleKeys bounds each table — a few tens of megabytes at the bound.
// See the package comment for what happens past it.
const MaxThrottleKeys = 200_000

// sweepEvery is how often a full table may be swept for keys gone quiet.
const sweepEvery = 30 * time.Second

// busyWait is what a refusal for "another check of this key is still running"
// asks the caller to wait. It is short because the check it waits on is.
const busyWait = time.Second

// overflowKey is the entry every untrackable key shares; see the package
// comment. No real key can collide with it: route keys always carry a prefix.
const overflowKey = "\x00overflow"

type throttleEntry struct {
	failures    int
	inFlight    int
	lastFailure time.Time
	lockedUntil time.Time
}

type throttleTable struct {
	policy    ThrottlePolicy
	limit     int
	entries   map[string]*throttleEntry
	lastSweep time.Time
}

// LoginThrottle tracks failed password checks per account and per source.
type LoginThrottle struct {
	mu       sync.Mutex
	now      func() time.Time
	accounts throttleTable
	sources  throttleTable
}

// NewLoginThrottle returns a throttle using the package policies.
func NewLoginThrottle() *LoginThrottle {
	return NewLoginThrottleWith(AccountThrottle, SourceThrottle, MaxThrottleKeys, time.Now)
}

// NewLoginThrottleWith returns a throttle with the given policies, table bound
// and clock.
func NewLoginThrottleWith(account, source ThrottlePolicy, limit int, now func() time.Time) *LoginThrottle {
	return &LoginThrottle{
		now:      now,
		accounts: throttleTable{policy: account, limit: limit, entries: map[string]*throttleEntry{}},
		sources:  throttleTable{policy: source, limit: limit, entries: map[string]*throttleEntry{}},
	}
}

// Attempt is one reserved password check. Exactly one of Fail, Succeed or
// Release settles it; the others are then no-ops, so a deferred Release
// settles whatever path the caller left by without counting it.
type Attempt struct {
	throttle *LoginThrottle
	account  string
	source   string
	done     bool
}

// Begin reserves a check of account from source. A zero wait means go ahead
// and settle the returned Attempt; a positive one means refuse, and nothing
// was reserved. An empty key is not throttled on that side. A nil throttle
// reserves everything.
func (t *LoginThrottle) Begin(account, source string) (*Attempt, time.Duration) {
	if t == nil {
		return &Attempt{done: true}, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()

	// Looked up first and created only on admission, so a refused attempt
	// writes nothing — refusals cannot be what fills the table.
	accountKey, accountEntry := t.accounts.lookup(account, now)
	sourceKey, sourceEntry := t.sources.lookup(source, now)
	wait := max(t.accounts.wait(accountEntry, now), t.sources.wait(sourceEntry, now))
	if wait > 0 {
		return nil, wait
	}
	t.accounts.admit(accountKey, accountEntry)
	t.sources.admit(sourceKey, sourceEntry)
	return &Attempt{throttle: t, account: accountKey, source: sourceKey}, 0
}

// Account names the account key the attempt was counted under, for a log
// line: the shared overflow entry reads as such rather than as its sentinel.
func (a *Attempt) Account() string {
	if a == nil {
		return ""
	}
	if a.account == overflowKey {
		return "(untracked: throttle table full)"
	}
	return a.account
}

// Fail settles the attempt as a wrong password and returns the lock it started
// on either side, zero if none.
func (a *Attempt) Fail() time.Duration {
	if a == nil || a.done {
		return 0
	}
	a.done = true
	t := a.throttle
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	return max(t.accounts.fail(a.account, now), t.sources.fail(a.source, now))
}

// Succeed settles the attempt as the right password: the account's count is
// cleared, the source's is kept on purpose.
func (a *Attempt) Succeed() {
	if a == nil || a.done {
		return
	}
	a.done = true
	t := a.throttle
	t.mu.Lock()
	defer t.mu.Unlock()
	// The overflow entry is shared by every key that could not be tracked, so
	// one success there clears nobody's count but its own — which it has not got.
	if entry, ok := t.accounts.entries[a.account]; ok && a.account != overflowKey {
		entry.failures, entry.lockedUntil = 0, time.Time{}
	}
	t.accounts.release(a.account)
	t.sources.release(a.source)
}

// Release settles the attempt as neither — the check could not be made (a
// store or directory error). Nothing is counted.
func (a *Attempt) Release() {
	if a == nil || a.done {
		return
	}
	a.done = true
	t := a.throttle
	t.mu.Lock()
	defer t.mu.Unlock()
	t.accounts.release(a.account)
	t.sources.release(a.source)
}

// lookup finds key's entry, or the overflow entry when the table is full and
// has no room for it. An entry that does not exist yet is returned as nil with
// the key it would be made under. An empty key has no entry.
func (tt *throttleTable) lookup(key string, now time.Time) (string, *throttleEntry) {
	if key == "" {
		return "", nil
	}
	if entry, ok := tt.entries[key]; ok {
		if now.Sub(entry.lastFailure) >= tt.policy.Forget && !now.Before(entry.lockedUntil) {
			entry.failures = 0
		}
		return key, entry
	}
	if len(tt.entries) >= tt.limit && !tt.makeRoom(now) {
		return overflowKey, tt.entries[overflowKey]
	}
	return key, nil
}

// admit counts one more check in flight against key, making its entry if it
// has none yet.
func (tt *throttleTable) admit(key string, entry *throttleEntry) {
	if key == "" {
		return
	}
	if entry == nil {
		entry = &throttleEntry{}
		tt.entries[key] = entry
	}
	entry.inFlight++
}

// wait is how long an entry must wait before another check: its lock, or a
// moment if its free attempts are all spoken for by checks still running.
func (tt *throttleTable) wait(entry *throttleEntry, now time.Time) time.Duration {
	if entry == nil {
		return 0
	}
	if now.Before(entry.lockedUntil) {
		return entry.lockedUntil.Sub(now)
	}
	// Up to Free failures pass, and the one after starts the lock — so a fresh
	// key may have Free+1 checks running, and a key past its allowance one.
	allowed := max(tt.policy.Free+1-entry.failures, 1)
	if entry.inFlight >= allowed {
		return busyWait
	}
	return 0
}

func (tt *throttleTable) fail(key string, now time.Time) time.Duration {
	entry, ok := tt.entries[key]
	if key == "" || !ok {
		return 0
	}
	entry.inFlight = max(entry.inFlight-1, 0)
	entry.failures++
	entry.lastFailure = now
	if over := entry.failures - tt.policy.Free; over > 0 {
		lock := tt.policy.lock(over)
		entry.lockedUntil = now.Add(lock)
		return lock
	}
	return 0
}

// release ends a check that recorded no failure. A key left with no failures
// and nothing in flight holds no entry, so only real counts occupy the table.
func (tt *throttleTable) release(key string) {
	entry, ok := tt.entries[key]
	if !ok {
		return
	}
	entry.inFlight = max(entry.inFlight-1, 0)
	if entry.inFlight == 0 && entry.failures == 0 && key != overflowKey {
		delete(tt.entries, key)
	}
}

// lock is the delay after the nth failure past the free ones: Base, doubling,
// capped at Max.
func (p ThrottlePolicy) lock(n int) time.Duration {
	d := p.Base
	for i := 1; i < n && d < p.Max; i++ {
		d *= 2
	}
	return min(d, p.Max)
}

// makeRoom sweeps out every key that has been quiet past Forget, at most once
// every sweepEvery, and reports whether that left room. Nothing with a recent
// failure, a running check or a live lock is ever dropped.
func (tt *throttleTable) makeRoom(now time.Time) bool {
	if now.Sub(tt.lastSweep) < sweepEvery {
		return false
	}
	tt.lastSweep = now
	for key, entry := range tt.entries {
		if key == overflowKey {
			continue
		}
		if entry.inFlight == 0 && !now.Before(entry.lockedUntil) && now.Sub(entry.lastFailure) >= tt.policy.Forget {
			delete(tt.entries, key)
		}
	}
	return len(tt.entries) < tt.limit
}
