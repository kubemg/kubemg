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
 * Three rules shape it:
 *
 *   - **It is a delay, not a lockout.** A few attempts are free, then each
 *     further failure locks the key for twice as long as the last, up to a
 *     ceiling. A permanent lock would hand anybody who knows a username a way
 *     to keep its owner out; a capped delay costs the owner minutes and a
 *     guesser most of their rate.
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
 * anyway at the ceiling. Both maps are bounded: an entry is forgotten once its
 * key has been quiet for a while, and past the bound a new key is not tracked
 * rather than an old one dropped — evicting a locked key would be the bypass.
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

// maxThrottleKeys bounds each map. See the package comment for what happens past it.
const maxThrottleKeys = 50_000

type throttleEntry struct {
	failures    int
	lastFailure time.Time
	lockedUntil time.Time
}

type throttleTable struct {
	policy  ThrottlePolicy
	entries map[string]*throttleEntry
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
	return NewLoginThrottleWith(AccountThrottle, SourceThrottle, time.Now)
}

// NewLoginThrottleWith returns a throttle with the given policies and clock.
func NewLoginThrottleWith(account, source ThrottlePolicy, now func() time.Time) *LoginThrottle {
	return &LoginThrottle{
		now:      now,
		accounts: throttleTable{policy: account, entries: map[string]*throttleEntry{}},
		sources:  throttleTable{policy: source, entries: map[string]*throttleEntry{}},
	}
}

// Wait reports how long the caller must wait before account may be tried again
// from source. Zero means try now. An empty key is not throttled on that side.
func (t *LoginThrottle) Wait(account, source string) time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	return max(t.accounts.wait(account, now), t.sources.wait(source, now))
}

// Fail records a failed password check against both keys.
func (t *LoginThrottle) Fail(account, source string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.accounts.fail(account, now)
	t.sources.fail(source, now)
}

// Succeed clears the account's count. The source's is kept on purpose.
func (t *LoginThrottle) Succeed(account string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.accounts.entries, account)
}

func (tt *throttleTable) wait(key string, now time.Time) time.Duration {
	if key == "" {
		return 0
	}
	entry, ok := tt.entries[key]
	if !ok || !now.Before(entry.lockedUntil) {
		return 0
	}
	return entry.lockedUntil.Sub(now)
}

func (tt *throttleTable) fail(key string, now time.Time) {
	if key == "" {
		return
	}
	entry, ok := tt.entries[key]
	if ok && now.Sub(entry.lastFailure) >= tt.policy.Forget {
		entry.failures = 0
	}
	if !ok {
		if len(tt.entries) >= maxThrottleKeys {
			tt.prune(now)
			if len(tt.entries) >= maxThrottleKeys {
				return
			}
		}
		entry = &throttleEntry{}
		tt.entries[key] = entry
	}
	entry.failures++
	entry.lastFailure = now
	if over := entry.failures - tt.policy.Free; over > 0 {
		entry.lockedUntil = now.Add(tt.policy.lock(over))
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

// prune drops every key that has been quiet past Forget and is not locked.
func (tt *throttleTable) prune(now time.Time) {
	for key, entry := range tt.entries {
		if now.Sub(entry.lastFailure) >= tt.policy.Forget && !now.Before(entry.lockedUntil) {
			delete(tt.entries, key)
		}
	}
}
