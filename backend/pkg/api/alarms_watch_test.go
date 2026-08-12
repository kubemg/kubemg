package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

/*
 * The lease around cluster-event polling.
 *
 * These are the assertions worth having a test for, because the failure they
 * guard against is invisible from inside one process: a second replica polling
 * the same fleet costs nothing here and doubles the read load on somebody's
 * production API server. Nothing in a single-process run would ever show it.
 *
 * The tick is asserted rather than the ticker: `startAlarmWatcher` is a loop
 * around a clock, and a test that waited for a minute-long interval would be a
 * test nobody runs.
 */

// AcquireLease takes the lease unless another holder already has it. The fake
// keeps no expiry — a test that wants the lease held elsewhere says so directly,
// which is the state that matters, and time is not what is being tested.
func (f *fakeStore) AcquireLease(
	_ context.Context, name, holder string, _ time.Duration,
) (bool, error) {
	f.leaseCalls++
	if f.leaseErr != nil {
		return false, f.leaseErr
	}
	if current, taken := f.leases[name]; taken && current != holder {
		return false, nil
	}
	f.leases[name] = holder
	return true, nil
}

// watchServer builds the handler struct directly: the poller is background work
// rather than a route, so there is no request to drive it with.
//
// `proxy` is left nil on purpose. pollClusterEvents returns immediately without
// one, which is exactly the signal these tests need — "did the pass get as far as
// wanting to read a cluster" is answered by whether the lease was consulted and
// what it said, and a nil proxy means no test here can accidentally reach out.
func watchServer(store *fakeStore) *server {
	return &server{store: store, instanceID: "replica-under-test"}
}

func TestAlarmTickTakesTheLeaseBeforePolling(t *testing.T) {
	store := newFakeStore()
	s := watchServer(store)

	s.alarmTick(context.Background(), &watermarkTable{seen: map[uint]time.Time{}})

	if store.leaseCalls != 1 {
		t.Fatalf("expected the pass to consult the lease once, got %d calls", store.leaseCalls)
	}
	if store.leases[db.LeaseAlarmWatcher] != "replica-under-test" {
		t.Fatalf("expected this replica to hold the lease, got %q",
			store.leases[db.LeaseAlarmWatcher])
	}
}

// The whole point: a replica that does not hold the lease does not read the
// fleet, however many of them are running.
func TestAlarmTickDoesNotPollWithoutTheLease(t *testing.T) {
	store := newFakeStore()
	store.leases[db.LeaseAlarmWatcher] = "some-other-replica"
	s := watchServer(store)

	s.alarmTick(context.Background(), &watermarkTable{seen: map[uint]time.Time{}})

	if store.leases[db.LeaseAlarmWatcher] != "some-other-replica" {
		t.Fatal("a replica without the lease stole it instead of standing down")
	}
}

// A renewal is the same call, so the holder keeps the job across ticks rather
// than handing it back and forth with whichever replica asks next.
func TestAlarmTickRenewsItsOwnLease(t *testing.T) {
	store := newFakeStore()
	s := watchServer(store)
	watermarks := &watermarkTable{seen: map[uint]time.Time{}}

	s.alarmTick(context.Background(), watermarks)
	s.alarmTick(context.Background(), watermarks)

	if store.leaseCalls != 2 {
		t.Fatalf("expected each pass to renew, got %d calls", store.leaseCalls)
	}
	if store.leases[db.LeaseAlarmWatcher] != "replica-under-test" {
		t.Fatalf("expected the holder to keep the lease, got %q",
			store.leases[db.LeaseAlarmWatcher])
	}
}

// Failing closed. Every replica sees the same database error, so treating it as
// permission to poll would put all of them on the cluster at once — arriving
// exactly when something is already wrong.
func TestAlarmTickDoesNotPollWhenTheLeaseCannotBeRead(t *testing.T) {
	store := newFakeStore()
	store.leaseErr = errors.New("database is unreachable")
	s := watchServer(store)

	s.alarmTick(context.Background(), &watermarkTable{seen: map[uint]time.Time{}})

	if _, taken := store.leases[db.LeaseAlarmWatcher]; taken {
		t.Fatal("a failed lease read must not be recorded as a held lease")
	}
}

/*
 * Which events a pass delivers.
 *
 * This is the whole behaviour of the feature, and both halves of it fail
 * silently rather than loudly: an event that should have paged somebody and did
 * not looks exactly like a quiet cluster, and one delivered twice looks like the
 * cluster being noisy. Neither shows up in a log.
 */

// alarmEvent builds one Event as a cluster would report it.
func alarmEvent(name string, at time.Time) eventObject {
	item := eventObject{Type: "Warning", Reason: "BackOff", Message: "back-off", Count: 1}
	item.Metadata.Name = name
	item.Metadata.Namespace = "shop"
	item.InvolvedObject.Kind = "Pod"
	item.InvolvedObject.Name = name
	item.InvolvedObject.Namespace = "shop"
	moment := at
	item.LastTimestamp = &moment
	item.FirstTimestamp = &moment
	return item
}

func candidateNames(entries []alarmCandidate) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.item.InvolvedObject.Name)
	}
	return out
}

/*
 * The bug this ordering fixes, pinned so it cannot come back.
 *
 * `watermarks.advance` both tests an event and raises the mark to it, so
 * whichever event is offered first sets the floor for the rest of the pass. The
 * API server returns an event list in **key order**, which has nothing to do
 * with time — so a newer event reaching the loop first used to raise the mark
 * past an older one that was still new since the last pass, and that second
 * event was dropped and never mentioned again.
 */
func TestSelectAlarmEventsDeliversOutOfOrderArrivals(t *testing.T) {
	now := time.Now().UTC()
	watermarks := &watermarkTable{seen: map[uint]time.Time{1: now.Add(-5 * time.Minute)}}

	// Deliberately newest-first, which is what a key-ordered list can hand over.
	items := []eventObject{
		alarmEvent("late", now.Add(-1*time.Minute)),
		alarmEvent("early", now.Add(-3*time.Minute)),
	}

	got := candidateNames(selectAlarmEvents(1, items, watermarks, now))
	if len(got) != 2 {
		t.Fatalf("delivered %v, want both events — the older one must not be eaten by the newer", got)
	}
	// And oldest first, so the sequence a pager shows matches the sequence that
	// happened.
	if got[0] != "early" || got[1] != "late" {
		t.Fatalf("delivered %v, want oldest first", got)
	}
}

// Anything at or before the mark has already been delivered by a previous pass.
func TestSelectAlarmEventsSkipsWhatTheMarkCovers(t *testing.T) {
	now := time.Now().UTC()
	mark := now.Add(-2 * time.Minute)
	watermarks := &watermarkTable{seen: map[uint]time.Time{1: mark}}

	items := []eventObject{
		alarmEvent("old", mark.Add(-time.Minute)),
		alarmEvent("at-the-mark", mark),
		alarmEvent("new", now.Add(-time.Second)),
	}

	got := candidateNames(selectAlarmEvents(1, items, watermarks, now))
	if len(got) != 1 || got[0] != "new" {
		t.Fatalf("delivered %v, want only the event past the mark", got)
	}
}

// The window is a second guard beside the mark: a cluster whose clock is behind,
// or a list that arrives out of order, must not resurrect yesterday.
func TestSelectAlarmEventsDropsAnythingOlderThanTheWindow(t *testing.T) {
	now := time.Now().UTC()
	watermarks := &watermarkTable{seen: map[uint]time.Time{1: now.Add(-24 * time.Hour)}}

	items := []eventObject{
		alarmEvent("yesterday", now.Add(-20*time.Hour)),
		alarmEvent("recent", now.Add(-time.Minute)),
	}

	got := candidateNames(selectAlarmEvents(1, items, watermarks, now))
	if len(got) != 1 || got[0] != "recent" {
		t.Fatalf("delivered %v, want only what is inside the window", got)
	}
}

// An event with no usable timestamp cannot be placed against the mark at all, so
// delivering it would mean paging on something that might be an hour old.
func TestSelectAlarmEventsSkipsUndatedEvents(t *testing.T) {
	now := time.Now().UTC()
	watermarks := &watermarkTable{seen: map[uint]time.Time{1: now.Add(-time.Hour)}}

	undated := eventObject{Type: "Warning", Reason: "BackOff"}
	undated.InvolvedObject.Name = "nowhen"

	if got := selectAlarmEvents(1, []eventObject{undated}, watermarks, now); len(got) != 0 {
		t.Fatalf("delivered %v, want nothing for an event with no time", candidateNames(got))
	}
}

// A second pass over the same events delivers nothing: the mark moved.
func TestSelectAlarmEventsIsIdempotentAcrossPasses(t *testing.T) {
	now := time.Now().UTC()
	watermarks := &watermarkTable{seen: map[uint]time.Time{1: now.Add(-10 * time.Minute)}}
	items := []eventObject{
		alarmEvent("a", now.Add(-3*time.Minute)),
		alarmEvent("b", now.Add(-2*time.Minute)),
	}

	if got := selectAlarmEvents(1, items, watermarks, now); len(got) != 2 {
		t.Fatalf("first pass delivered %v, want both", candidateNames(got))
	}
	if got := selectAlarmEvents(1, items, watermarks, now); len(got) != 0 {
		t.Fatalf("second pass delivered %v, want nothing — the mark has moved", candidateNames(got))
	}
}
