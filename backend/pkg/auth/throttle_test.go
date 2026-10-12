package auth

import (
	"fmt"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

var (
	testAccount = ThrottlePolicy{Free: 3, Base: 10 * time.Second, Max: 40 * time.Second, Forget: time.Hour}
	testSource  = ThrottlePolicy{Free: 10, Base: time.Minute, Max: time.Hour, Forget: time.Hour}
)

func testThrottle(limit int) (*LoginThrottle, *clock) {
	c := &clock{t: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)}
	return NewLoginThrottleWith(testAccount, testSource, limit, c.now), c
}

// fail runs one whole failed check and returns the wait Begin answered with.
func fail(th *LoginThrottle, account, source string) time.Duration {
	attempt, wait := th.Begin(account, source)
	if wait > 0 {
		return wait
	}
	attempt.Fail()
	return 0
}

func waitFor(th *LoginThrottle, account, source string) time.Duration {
	attempt, wait := th.Begin(account, source)
	attempt.Release()
	return wait
}

func TestThrottleLetsTheFreeAttemptsThrough(t *testing.T) {
	th, _ := testThrottle(100)
	for i := 0; i < 3; i++ {
		if w := fail(th, "a:alice", "10.0.0.1"); w != 0 {
			t.Fatalf("attempt %d: wait %v, want none", i+1, w)
		}
	}
	if w := waitFor(th, "a:alice", "10.0.0.1"); w != 0 {
		t.Fatalf("three free failures should not lock yet, wait %v", w)
	}
}

func TestThrottleBacksOffAndCaps(t *testing.T) {
	th, c := testThrottle(100)
	for i := 0; i < 3; i++ {
		fail(th, "a:alice", "10.0.0.1")
	}
	for _, want := range []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 40 * time.Second} {
		if w := fail(th, "a:alice", "10.0.0.1"); w != 0 {
			t.Fatalf("the attempt after a lapsed lock should run, wait %v", w)
		}
		if w := waitFor(th, "a:alice", "10.0.0.1"); w != want {
			t.Fatalf("wait = %v, want %v", w, want)
		}
		c.advance(want)
	}
}

// Checks still running count against the allowance, so a burst sent at once is
// not a burst checked at once. This is the race a check-then-record throttle has.
func TestThrottleCountsChecksInFlight(t *testing.T) {
	th, _ := testThrottle(100)
	var running []*Attempt
	for i := 0; i < 20; i++ {
		if attempt, wait := th.Begin("a:alice", fmt.Sprintf("10.0.0.%d", i)); wait == 0 {
			running = append(running, attempt)
		}
	}
	// Three free failures and the one that starts the lock.
	if len(running) != testAccount.Free+1 {
		t.Fatalf("%d checks admitted at once, want %d", len(running), testAccount.Free+1)
	}
	for _, attempt := range running {
		attempt.Fail()
	}
	if w := waitFor(th, "a:alice", "10.0.0.99"); w == 0 {
		t.Fatal("the burst's failures should have locked the account")
	}
}

// Past the free allowance a key's checks run one at a time, each failure
// re-locking it, so a lapsed lock admits one guess and not a burst.
func TestThrottleSerialisesChecksPastTheAllowance(t *testing.T) {
	th, c := testThrottle(100)
	for i := 0; i < 4; i++ {
		fail(th, "a:alice", "")
	}
	c.advance(time.Minute)
	first, wait := th.Begin("a:alice", "")
	if wait != 0 {
		t.Fatalf("a lapsed lock should admit a check, wait %v", wait)
	}
	if _, wait := th.Begin("a:alice", ""); wait == 0 {
		t.Fatal("a second concurrent check past the allowance should wait")
	}
	first.Release()
	if w := waitFor(th, "a:alice", ""); w != 0 {
		t.Fatalf("a released check should free the slot, wait %v", w)
	}
}

// The account is what is locked, wherever the next guess comes from — otherwise
// a guesser only needs a second address.
func TestThrottleLocksTheAccountFromEveryAddress(t *testing.T) {
	th, _ := testThrottle(100)
	for i := 0; i < 4; i++ {
		fail(th, "a:alice", "10.0.0.1")
	}
	if w := waitFor(th, "a:alice", "192.0.2.7"); w == 0 {
		t.Fatal("a locked account must be locked from another address too")
	}
	if w := waitFor(th, "a:bob", "192.0.2.7"); w != 0 {
		t.Fatalf("another account from another address is unaffected, wait %v", w)
	}
}

// One address spraying one password across many accounts never trips any one
// account's count, so the source has to carry its own.
func TestThrottleLocksASprayingSource(t *testing.T) {
	th, _ := testThrottle(100)
	for i := 0; i < 11; i++ {
		fail(th, fmt.Sprintf("a:user%d", i), "10.0.0.1")
	}
	if w := waitFor(th, "a:zed", "10.0.0.1"); w == 0 {
		t.Fatal("an address past its free failures must be locked for any account")
	}
	if w := waitFor(th, "a:zed", "10.0.0.2"); w != 0 {
		t.Fatalf("another address is unaffected, wait %v", w)
	}
}

func TestThrottleSuccessClearsTheAccountButNotTheSource(t *testing.T) {
	th, _ := testThrottle(100)
	for i := 0; i < 3; i++ {
		fail(th, "a:alice", "10.0.0.1")
	}
	for i := 0; i < 7; i++ {
		fail(th, fmt.Sprintf("a:user%d", i), "10.0.0.1")
	}
	attempt, _ := th.Begin("a:alice", "10.0.0.1")
	attempt.Succeed()
	if w := waitFor(th, "a:alice", "10.0.0.9"); w != 0 {
		t.Fatalf("a success should clear the account, wait %v", w)
	}
	fail(th, "a:bob", "10.0.0.1")
	if w := waitFor(th, "a:carol", "10.0.0.1"); w == 0 {
		t.Fatal("the source must stay counted across another account's success")
	}
}

func TestThrottleForgetsAQuietKey(t *testing.T) {
	th, c := testThrottle(100)
	for i := 0; i < 3; i++ {
		fail(th, "a:alice", "")
	}
	c.advance(2 * time.Hour)
	fail(th, "a:alice", "")
	if w := waitFor(th, "a:alice", ""); w != 0 {
		t.Fatalf("failures an hour apart should start a fresh count, wait %v", w)
	}
}

// Filling the table must not be a way to switch the throttle off. A locked key
// is never the one dropped to make room.
func TestThrottleNeverDropsALockedKeyToMakeRoom(t *testing.T) {
	th, _ := testThrottle(4)
	for i := 0; i < 4; i++ {
		fail(th, "a:alice", "")
	}
	for i := 0; i < 50; i++ {
		fail(th, fmt.Sprintf("a:spray%d", i), "")
	}
	if w := waitFor(th, "a:alice", ""); w == 0 {
		t.Fatal("a locked key was dropped to make room")
	}
}

// A table full of locked keys is an attack, and what cannot be tracked then
// shares one entry rather than going uncounted.
func TestThrottleCountsUntrackableKeysTogether(t *testing.T) {
	th, _ := testThrottle(2)
	for _, key := range []string{"a:alice", "a:bob"} {
		for i := 0; i < 4; i++ {
			fail(th, key, "")
		}
	}
	locked := false
	for i := 0; i < 10 && !locked; i++ {
		locked = fail(th, fmt.Sprintf("a:spray%d", i), "") > 0
	}
	if !locked {
		t.Fatal("guesses past a full table of locked keys went unthrottled")
	}
	// The shared entry is not cleared by a success through it.
	attempt, wait := th.Begin("a:someone-else", "")
	if wait == 0 {
		attempt.Succeed()
		t.Fatal("the overflow entry should still be locked")
	}
}

func TestSettlingAnAttemptTwiceCountsOnce(t *testing.T) {
	th, _ := testThrottle(100)
	attempt, _ := th.Begin("a:alice", "")
	attempt.Fail()
	attempt.Fail()
	attempt.Release()
	for i := 0; i < 2; i++ {
		fail(th, "a:alice", "")
	}
	if w := waitFor(th, "a:alice", ""); w != 0 {
		t.Fatalf("a doubly-settled attempt counted twice, wait %v", w)
	}
}

func TestNilThrottleIsOff(t *testing.T) {
	var th *LoginThrottle
	attempt, wait := th.Begin("a:alice", "10.0.0.1")
	if wait != 0 {
		t.Fatalf("a nil throttle must never wait, got %v", wait)
	}
	attempt.Fail()
	attempt.Succeed()
	attempt.Release()
}
