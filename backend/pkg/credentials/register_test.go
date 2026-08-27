package credentials

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestZeroValueRevokesNothing(t *testing.T) {
	// The load-bearing case: a server that has not read its register yet, or one
	// whose database will not answer, must refuse nothing. A blip that locks a
	// fleet out of kubectl is worse than one that briefly honours a withdrawn
	// file, which is still expiring on its own clock.
	var snapshot Snapshot
	if snapshot.Revoked("any-token") {
		t.Fatal("the zero-value snapshot revoked a credential")
	}
	var register *Register
	if register.Revoked("any-token") || register.Observe("any-token") {
		t.Fatal("a nil register revoked a credential")
	}
	if New().Revoked("any-token") {
		t.Fatal("a fresh register revoked a credential")
	}
}

func TestSnapshotRevokesOnlyWhatItNames(t *testing.T) {
	snapshot := NewSnapshot([]string{"a", "b", ""})
	if snapshot.Size() != 2 {
		t.Fatalf("expected the empty id to be dropped, got size %d", snapshot.Size())
	}
	if !snapshot.Revoked("a") || !snapshot.Revoked("b") {
		t.Fatal("a named credential was not revoked")
	}
	if snapshot.Revoked("c") {
		t.Fatal("an unnamed credential was revoked")
	}
	// A credential minted before the register existed carries no id. Nothing here
	// can name it, so it is not revoked.
	if snapshot.Revoked("") {
		t.Fatal("an id-less credential was revoked")
	}
}

func TestStoreReplacesWholesale(t *testing.T) {
	register := New()
	register.Store(NewSnapshot([]string{"a"}))
	if !register.Revoked("a") {
		t.Fatal("published snapshot was not read back")
	}
	register.Store(NewSnapshot([]string{"b"}))
	if register.Revoked("a") {
		t.Fatal("a republish left the previous set in place")
	}
	if !register.Revoked("b") {
		t.Fatal("the republished set was not read back")
	}
}

func TestObserveTouchesAtMostOncePerInterval(t *testing.T) {
	var (
		mu    sync.Mutex
		calls []string
		done  = make(chan struct{}, 8)
	)
	register := New()
	register.SetToucher(func(_ context.Context, tokenID string, _ time.Time) {
		mu.Lock()
		calls = append(calls, tokenID)
		mu.Unlock()
		done <- struct{}{}
	})

	for range 5 {
		if register.Observe("live") {
			t.Fatal("a credential nothing revoked was reported as revoked")
		}
	}
	// The write is off the request's own goroutine, so wait for the one that was
	// scheduled rather than for a duration.
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("expected one touch across five calls, got %d", len(calls))
	}
}

func TestObserveDoesNotTouchARevokedCredential(t *testing.T) {
	touched := make(chan string, 1)
	register := New()
	register.SetToucher(func(_ context.Context, tokenID string, _ time.Time) { touched <- tokenID })
	register.Store(NewSnapshot([]string{"gone"}))

	if !register.Observe("gone") {
		t.Fatal("a revoked credential was served")
	}
	select {
	case id := <-touched:
		t.Fatalf("a refused call stamped last-used on %q", id)
	default:
	}
}

func TestObserveWithoutAToucherStillAnswers(t *testing.T) {
	register := New()
	register.Store(NewSnapshot([]string{"gone"}))
	if !register.Observe("gone") {
		t.Fatal("a register with no toucher stopped answering the revocation question")
	}
	if register.Observe("live") {
		t.Fatal("a register with no toucher revoked a live credential")
	}
}
