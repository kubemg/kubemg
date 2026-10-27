package bastion

/*
 * Pacing what one caller drives into one cluster.
 *
 * Every proxied call is inspected and audited; without this, none of it is
 * paced. A kubectl in a loop, a script holding an issued kubeconfig or a
 * controller pointed at the proxy would push a target API server as hard as the
 * tunnel allows, and that cluster's operator would have no lever to pull. The
 * resource being protected is somebody's API server, not this process, so the
 * bucket is per (identity, cluster) rather than per user.
 *
 * The unit is a call down the tunnel, not an HTTP request to KubeMG: one console
 * click on all-namespaces Pods is up to fifty impersonated calls, and a limit
 * counting inbound requests would mis-measure exactly the read that costs most.
 * That is why the check lives in Handle, Call and Watch — everything that reaches
 * a tunnel goes through one of them — and sits after the grant and guardrail
 * checks, which cost a cluster nothing.
 *
 * Over budget, a call first *waits* — up to maxThrottleWait — and only then is
 * refused with 429 and Retry-After. Waiting is what pacing means: the console's
 * fan-out and a Helm install's sequential applies are bounded by design, and a
 * chart refused half way through is a half-installed release. A queue deeper
 * than the wait is a caller asking for more than the budget, and that is the one
 * that hears no.
 *
 * An open stream is never touched. Killing a running exec, `logs -f` or
 * port-forward to shed load is worse than the load; only the handshake spends a
 * token.
 */

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// Defaults for the per-caller, per-cluster budget. They are kubectl's own
// client-side defaults (QPS 50, burst 300 since Kubernetes 1.27): a single
// kubectl never notices the limit, and what it bounds is several of them, or a
// loop, against one cluster.
const (
	DefaultRateLimitPerSecond = 50
	DefaultRateLimitBurst     = 300
)

// maxThrottleWait is how long a call over budget is held before it is refused.
// It is short of every client's own timeout, and long enough that a burst of
// concurrent console reads is paced rather than failed.
const maxThrottleWait = 2 * time.Second

// idleBucketAfter is how long a full bucket is kept. A bucket that has refilled
// is indistinguishable from a new one, so dropping it loses nothing.
const idleBucketAfter = 10 * time.Minute

// Throttle outcomes, as reported to the OnThrottle hook.
const (
	ThrottleDelayed = "delayed"
	ThrottleRefused = "refused"
)

// RateLimit is the budget one identity has against one cluster.
type RateLimit struct {
	PerSecond int
	Burst     int
}

// normalized reads an unset or nonsensical value as the default, so a limiter
// can never be configured into refusing everything.
func (l RateLimit) normalized() RateLimit {
	if l.PerSecond <= 0 {
		l.PerSecond = DefaultRateLimitPerSecond
	}
	if l.Burst <= 0 {
		l.Burst = DefaultRateLimitBurst
	}
	return l
}

type bucketKey struct {
	userID uint
	// name keys an unsaved (ID 0) service identity — the alarm watcher, the
	// event watcher, the shell runner — so each has its own budget and a busy
	// operator never starves KubeMG's own reads, nor the other way round.
	name    string
	cluster uint
}

type bucket struct {
	limiter  *rate.Limiter
	lastUsed time.Time
}

// RateLimiter holds every (identity, cluster) bucket. A nil *RateLimiter paces
// nothing, which is what the tests and a gateway wired without one get.
type RateLimiter struct {
	limit atomic.Pointer[RateLimit]

	mu        sync.Mutex
	buckets   map[bucketKey]*bucket
	lastSweep time.Time

	// now and sleep are the clock, replaceable in tests.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error

	// onThrottle, when set, is told about every delayed or refused call.
	onThrottle func(outcome string)
}

// NewRateLimiter builds a limiter at the given budget.
func NewRateLimiter(limit RateLimit) *RateLimiter {
	l := &RateLimiter{
		buckets: map[bucketKey]*bucket{},
		now:     time.Now,
		sleep:   sleepContext,
	}
	l.Store(limit)
	return l
}

// OnThrottle sets the hook told about every delayed or refused call.
func (l *RateLimiter) OnThrottle(hook func(outcome string)) {
	if l != nil {
		l.onThrottle = hook
	}
}

// Store publishes a new budget. Existing buckets take it on their next call,
// so lowering the limit applies at once rather than to newcomers only.
func (l *RateLimiter) Store(limit RateLimit) {
	if l == nil {
		return
	}
	normalized := limit.normalized()
	if previous := l.limit.Load(); previous != nil && *previous == normalized {
		return
	}
	l.limit.Store(&normalized)

	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, b := range l.buckets {
		b.limiter.SetLimitAt(now, rate.Limit(normalized.PerSecond))
		b.limiter.SetBurstAt(now, normalized.Burst)
	}
}

// Limit is the budget in force.
func (l *RateLimiter) Limit() RateLimit {
	if l == nil {
		return RateLimit{}.normalized()
	}
	return *l.limit.Load()
}

// ThrottledError is a call refused for being over its budget.
type ThrottledError struct {
	RetryAfter time.Duration
	Limit      RateLimit
	Cluster    string
}

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("too many calls to cluster %s: this account may make %d per second (bursts of %d) "+
		"through kubemg; try again in %ds", e.Cluster, e.Limit.PerSecond, e.Limit.Burst, e.RetryAfterSeconds())
}

// RetryAfterSeconds is RetryAfter as the whole seconds a Retry-After header
// carries, never less than one.
func (e *ThrottledError) RetryAfterSeconds() int {
	return max(1, int(math.Ceil(e.RetryAfter.Seconds())))
}

// Wait spends one token from the caller's bucket for a cluster. It returns at
// once when the bucket has one, holds the call up to maxThrottleWait when it
// will have one by then, and otherwise refuses with a *ThrottledError. A caller
// that leaves while waiting gets its context's error and its token back.
func (l *RateLimiter) Wait(ctx context.Context, userID uint, username string, clusterID uint, cluster string) error {
	if l == nil {
		return nil
	}
	now := l.now()
	reservation := l.bucketFor(userID, username, clusterID, now).ReserveN(now, 1)
	delay := reservation.DelayFrom(now)
	if delay == 0 {
		return nil
	}
	if delay > maxThrottleWait {
		reservation.CancelAt(now)
		l.report(ThrottleRefused)
		return &ThrottledError{RetryAfter: delay, Limit: l.Limit(), Cluster: cluster}
	}
	l.report(ThrottleDelayed)
	if err := l.sleep(ctx, delay); err != nil {
		reservation.Cancel()
		return err
	}
	return nil
}

func (l *RateLimiter) report(outcome string) {
	if l.onThrottle != nil {
		l.onThrottle(outcome)
	}
}

func (l *RateLimiter) bucketFor(userID uint, username string, clusterID uint, now time.Time) *rate.Limiter {
	key := bucketKey{userID: userID, cluster: clusterID}
	if userID == 0 {
		key.name = username
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastSweep) > time.Minute {
		l.sweep(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		limit := *l.limit.Load()
		b = &bucket{limiter: rate.NewLimiter(rate.Limit(limit.PerSecond), limit.Burst)}
		l.buckets[key] = b
	}
	b.lastUsed = now
	return b.limiter
}

// sweep drops buckets nobody has used for a while. Called with mu held.
func (l *RateLimiter) sweep(now time.Time) {
	l.lastSweep = now
	for key, b := range l.buckets {
		if now.Sub(b.lastUsed) > idleBucketAfter {
			delete(l.buckets, key)
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
