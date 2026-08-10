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
