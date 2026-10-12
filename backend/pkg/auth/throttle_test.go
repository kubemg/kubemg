package auth

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func testThrottle() (*LoginThrottle, *clock) {
	c := &clock{t: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)}
	account := ThrottlePolicy{Free: 3, Base: 10 * time.Second, Max: 40 * time.Second, Forget: time.Hour}
	source := ThrottlePolicy{Free: 10, Base: time.Minute, Max: time.Hour, Forget: time.Hour}
	return NewLoginThrottleWith(account, source, c.now), c
}

func TestThrottleLetsTheFreeAttemptsThrough(t *testing.T) {
	th, _ := testThrottle()
	for i := 0; i < 3; i++ {
		if w := th.Wait("alice", "10.0.0.1"); w != 0 {
			t.Fatalf("attempt %d: wait %v, want none", i+1, w)
		}
		th.Fail("alice", "10.0.0.1")
	}
	if w := th.Wait("alice", "10.0.0.1"); w != 0 {
		t.Fatalf("three free failures should not lock yet, wait %v", w)
	}
}

func TestThrottleBacksOffAndCaps(t *testing.T) {
	th, c := testThrottle()
	for i := 0; i < 3; i++ {
		th.Fail("alice", "10.0.0.1")
	}
	for _, want := range []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 40 * time.Second} {
		th.Fail("alice", "10.0.0.1")
		if w := th.Wait("alice", "10.0.0.1"); w != want {
			t.Fatalf("wait = %v, want %v", w, want)
		}
		c.advance(want)
		if w := th.Wait("alice", "10.0.0.1"); w != 0 {
			t.Fatalf("lock should have lapsed after %v, wait %v", want, w)
		}
	}
}

// The account is what is locked, wherever the next guess comes from — otherwise
// a guesser only needs a second address.
func TestThrottleLocksTheAccountFromEveryAddress(t *testing.T) {
	th, _ := testThrottle()
	for i := 0; i < 4; i++ {
		th.Fail("alice", "10.0.0.1")
	}
	if w := th.Wait("alice", "192.0.2.7"); w == 0 {
		t.Fatal("a locked account must be locked from another address too")
	}
	if w := th.Wait("bob", "192.0.2.7"); w != 0 {
		t.Fatalf("another account from another address is unaffected, wait %v", w)
	}
}

// One address spraying one password across many accounts never trips any one
// account's count, so the source has to carry its own.
func TestThrottleLocksASprayingSource(t *testing.T) {
	th, _ := testThrottle()
	for i := 0; i < 11; i++ {
		th.Fail(string(rune('a'+i)), "10.0.0.1")
	}
	if w := th.Wait("zed", "10.0.0.1"); w == 0 {
		t.Fatal("an address past its free failures must be locked for any account")
	}
	if w := th.Wait("zed", "10.0.0.2"); w != 0 {
		t.Fatalf("another address is unaffected, wait %v", w)
	}
}

func TestThrottleSuccessClearsTheAccountButNotTheSource(t *testing.T) {
	th, _ := testThrottle()
	for i := 0; i < 10; i++ {
		th.Fail("alice", "10.0.0.1")
	}
	th.Succeed("alice")
	if w := th.Wait("alice", "10.0.0.9"); w != 0 {
		t.Fatalf("a success should clear the account, wait %v", w)
	}
	th.Fail("bob", "10.0.0.1")
	if w := th.Wait("carol", "10.0.0.1"); w == 0 {
		t.Fatal("the source must stay counted across another account's success")
	}
}

func TestThrottleForgetsAQuietKey(t *testing.T) {
	th, c := testThrottle()
	for i := 0; i < 3; i++ {
		th.Fail("alice", "")
	}
	c.advance(2 * time.Hour)
	th.Fail("alice", "")
	if w := th.Wait("alice", ""); w != 0 {
		t.Fatalf("failures an hour apart should start a fresh count, wait %v", w)
	}
}

func TestNilThrottleIsOff(t *testing.T) {
	var th *LoginThrottle
	th.Fail("alice", "10.0.0.1")
	th.Succeed("alice")
	if w := th.Wait("alice", "10.0.0.1"); w != 0 {
		t.Fatalf("a nil throttle must never wait, got %v", w)
	}
}
