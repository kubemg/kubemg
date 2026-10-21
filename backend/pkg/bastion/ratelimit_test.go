package bastion

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// frozenLimiter is a limiter on a clock that only moves when the test moves it,
// and whose waits return at once while recording how long they would have been.
type frozenLimiter struct {
	*RateLimiter
	mu       sync.Mutex
	at       time.Time
	waits    []time.Duration
	outcomes []string
}

func newFrozenLimiter(limit RateLimit) *frozenLimiter {
	f := &frozenLimiter{RateLimiter: NewRateLimiter(limit), at: time.Unix(1_700_000_000, 0)}
	f.now = func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.at
	}
	f.sleep = func(_ context.Context, d time.Duration) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.waits = append(f.waits, d)
		return nil
	}
	f.OnThrottle(func(outcome string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.outcomes = append(f.outcomes, outcome)
	})
	return f
}

func (f *frozenLimiter) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.at = f.at.Add(d)
}

func (f *frozenLimiter) wait(userID uint, username string, clusterID uint) error {
	return f.Wait(context.Background(), userID, username, clusterID, "prod-eu")
}

// The budget is spent at once while it lasts, then paced, and refused only once
// the queue is deeper than the wait — with the time until it would be served.
func TestRateLimiterPacesThenRefuses(t *testing.T) {
	f := newFrozenLimiter(RateLimit{PerSecond: 1, Burst: 2})

	for i := range 2 {
		if err := f.wait(10, "alice", 1); err != nil {
			t.Fatalf("call %d within the burst: %v", i, err)
		}
	}
	if len(f.waits) != 0 {
		t.Fatalf("calls within the burst must not wait, waited %v", f.waits)
	}

	// Two more are owed one and two seconds: paced, not refused.
	for i := range 2 {
		if err := f.wait(10, "alice", 1); err != nil {
			t.Fatalf("paced call %d: %v", i, err)
		}
	}
	if len(f.waits) != 2 || f.waits[0] != time.Second || f.waits[1] != 2*time.Second {
		t.Fatalf("waits = %v, want 1s then 2s", f.waits)
	}

	// A third would wait three seconds, past maxThrottleWait.
	err := f.wait(10, "alice", 1)
	var throttled *ThrottledError
	if !errors.As(err, &throttled) {
		t.Fatalf("err = %v, want a ThrottledError", err)
	}
	if throttled.RetryAfterSeconds() != 3 || throttled.Limit != (RateLimit{PerSecond: 1, Burst: 2}) {
		t.Fatalf("refusal = %+v (retry %ds)", throttled, throttled.RetryAfterSeconds())
	}
	if got := f.outcomes; len(got) != 3 || got[2] != ThrottleRefused || got[0] != ThrottleDelayed {
		t.Fatalf("outcomes = %v, want delayed, delayed, refused", got)
	}

	// The refused call spent nothing: once the queue has drained by a second,
	// the next caller is owed two seconds again, not three.
	f.advance(time.Second)
	if err := f.wait(10, "alice", 1); err != nil {
		t.Fatalf("after a second: %v", err)
	}
	if last := f.waits[len(f.waits)-1]; last != 2*time.Second {
		t.Fatalf("wait after the refusal = %v, want 2s (the refusal must give its token back)", last)
	}
}

// The resource being protected is a cluster's API server, and KubeMG's own
// reads must not share an operator's budget.
func TestRateLimiterBucketsByIdentityAndCluster(t *testing.T) {
	f := newFrozenLimiter(RateLimit{PerSecond: 1, Burst: 1})
	if err := f.wait(10, "alice", 1); err != nil {
		t.Fatal(err)
	}
	for _, other := range []struct {
		user    uint
		name    string
		cluster uint
	}{
		{11, "bob", 1},                 // another person, same cluster
		{10, "alice", 2},               // the same person, another cluster
		{0, "kubemg:alarm-watcher", 1}, // a service identity
		{0, "kubemg:event-watcher", 1}, // a different service identity
	} {
		if err := f.wait(other.user, other.name, other.cluster); err != nil {
			t.Fatalf("%+v: %v", other, err)
		}
	}
	if len(f.waits) != 0 {
		t.Fatalf("no bucket should have been shared, waited %v", f.waits)
	}
	// A saved account is its id, not its name: a rename does not reset it.
	if err := f.wait(10, "alice-renamed", 1); err != nil {
		t.Fatal(err)
	}
	if len(f.waits) != 1 {
		t.Fatal("the same account under a new name must spend the same bucket")
	}
}

// Lowering the budget reaches buckets that already exist, not newcomers only.
func TestRateLimiterStoreReachesExistingBuckets(t *testing.T) {
	f := newFrozenLimiter(RateLimit{PerSecond: 100, Burst: 100})
	if err := f.wait(10, "alice", 1); err != nil {
		t.Fatal(err)
	}
	f.Store(RateLimit{PerSecond: 1, Burst: 1})
	if f.Limit() != (RateLimit{PerSecond: 1, Burst: 1}) {
		t.Fatalf("Limit = %+v", f.Limit())
	}
	_ = f.wait(10, "alice", 1)
	_ = f.wait(10, "alice", 1)
	if len(f.waits) == 0 {
		t.Fatal("a lowered burst must pace an existing bucket")
	}
}

func TestRateLimiterDefaultsAndNil(t *testing.T) {
	if got := NewRateLimiter(RateLimit{}).Limit(); got != (RateLimit{DefaultRateLimitPerSecond, DefaultRateLimitBurst}) {
		t.Fatalf("unset budget = %+v, want the defaults", got)
	}
	if got := NewRateLimiter(RateLimit{PerSecond: -1, Burst: 0}).Limit(); got.PerSecond <= 0 || got.Burst <= 0 {
		t.Fatalf("a nonsensical budget must read as the default, got %+v", got)
	}
	var none *RateLimiter
	if err := none.Wait(context.Background(), 1, "alice", 1, "prod-eu"); err != nil {
		t.Fatalf("a nil limiter paces nothing: %v", err)
	}
	none.Store(RateLimit{PerSecond: 1})
}

// A caller that gives up while paced gets its token back.
func TestRateLimiterCancelledWaitReturnsTheToken(t *testing.T) {
	limiter := NewRateLimiter(RateLimit{PerSecond: 1, Burst: 1})
	ctx := context.Background()
	if err := limiter.Wait(ctx, 10, "alice", 1, "prod-eu"); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := limiter.Wait(cancelled, 10, "alice", 1, "prod-eu"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestRateLimiterDropsIdleBuckets(t *testing.T) {
	f := newFrozenLimiter(RateLimit{PerSecond: 1, Burst: 1})
	_ = f.wait(10, "alice", 1)
	f.advance(idleBucketAfter + 2*time.Minute)
	_ = f.wait(11, "bob", 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, kept := f.buckets[bucketKey{userID: 10, cluster: 1}]; kept {
		t.Fatal("an idle bucket must be dropped by the sweep")
	}
}

// Through the gateway: a console read over budget is refused before the tunnel,
// as a 429 the trail records as kubemg's own refusal, and never reaches the
// agent.
func TestCallOverBudgetIsRefusedAndRecorded(t *testing.T) {
	h := newHarness(t)
	cluster := h.addCluster(1, "prod-eu", "kmg_valid")
	user := h.addUser(10, "alice", db.SystemRoleUser)
	agent, err := h.dialAgent("kmg_valid", func(Request) Response { return *jsonResponse(http.StatusOK, `{}`) })
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	f := newFrozenLimiter(RateLimit{PerSecond: 1, Burst: 1})
	h.proxy.limiter = f.RateLimiter

	grant := db.UserClusterAccess{UserID: user.ID, ClusterID: cluster.ID, K8sRole: db.K8sRoleView}
	var last error
	for range 4 {
		_, last = h.proxy.Call(context.Background(), user, cluster, grant, http.MethodGet,
			"/api/v1/namespaces/payments/pods", nil, nil)
	}
	var callErr *CallError
	if !errors.As(last, &callErr) || callErr.Status != http.StatusTooManyRequests || callErr.RetryAfter != 3 {
		t.Fatalf("fourth call = %v, want a 429 with Retry-After 3", last)
	}

	events := h.audit.events()
	refused := events[len(events)-1]
	if refused.Status != http.StatusTooManyRequests || refused.RefusedBy != RefusedByKubeMG || refused.Error == "" {
		t.Fatalf("refusal record = %+v", refused)
	}
	agent.mu.Lock()
	seen := len(agent.seen)
	agent.mu.Unlock()
	if seen != 3 {
		t.Fatalf("the agent saw %d calls, want 3 — the refused one must not reach the tunnel", seen)
	}
}

// kubectl's path: the refusal is a Kubernetes Status with Retry-After, which
// client-go honours by waiting and retrying on its own.
func TestProxyOverBudgetAnswersAStatusWithRetryAfter(t *testing.T) {
	h := newHarness(t)
	h.addCluster(1, "prod-eu", "kmg_valid")
	user := h.addUser(10, "alice", db.SystemRoleUser)
	h.grant(user.ID, 1, db.K8sRoleView, nil)
	if _, err := h.dialAgent("kmg_valid", func(Request) Response { return *jsonResponse(http.StatusOK, `{}`) }); err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	waitFor(t, func() bool { return h.gateway.Registry().Connected(1) })

	f := newFrozenLimiter(RateLimit{PerSecond: 1, Burst: 1})
	h.proxy.limiter = f.RateLimiter

	token := h.token(user)
	var resp *http.Response
	for range 4 {
		resp = h.proxyRequest(http.MethodGet, "/api/v1/clusters/1/proxy/api/v1/namespaces/payments/pods", token)
	}
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "3" {
		t.Fatalf("status %d, Retry-After %q; want 429 and 3", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	var status struct {
		Kind    string `json:"kind"`
		Reason  string `json:"reason"`
		Code    int    `json:"code"`
		Message string `json:"message"`
		Details struct {
			RetryAfterSeconds int `json:"retryAfterSeconds"`
		} `json:"details"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Kind != "Status" || status.Reason != "TooManyRequests" || status.Code != 429 ||
		status.Details.RetryAfterSeconds != 3 || status.Message == "" {
		t.Fatalf("body = %+v, want a TooManyRequests Status", status)
	}
}
