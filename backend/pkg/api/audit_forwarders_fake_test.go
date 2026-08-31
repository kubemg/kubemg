package api

import (
	"context"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The forwarder half of fakeStore, in its own file for the same reason the
// alarm half is: a self-contained table, and router_test.go is long enough.

func (f *fakeStore) forwarderTable() map[uint]*db.AuditForwarder {
	if f.forwarders == nil {
		f.forwarders = map[uint]*db.AuditForwarder{}
	}
	return f.forwarders
}

// addAuditForwarder seeds a destination.
func (f *fakeStore) addAuditForwarder(forwarder db.AuditForwarder) *db.AuditForwarder {
	table := f.forwarderTable()
	forwarder.ID = f.nextID
	f.nextID++
	if forwarder.CreatedAt.IsZero() {
		forwarder.CreatedAt = time.Now()
	}
	table[forwarder.ID] = &forwarder
	return &forwarder
}

func (f *fakeStore) ListAuditForwarders(_ context.Context) ([]db.AuditForwarder, error) {
	if f.forwarderErr != nil {
		return nil, f.forwarderErr
	}
	table := f.forwarderTable()
	out := []db.AuditForwarder{}
	for id := uint(1); id <= f.nextID; id++ {
		if forwarder, ok := table[id]; ok {
			out = append(out, *forwarder)
		}
	}
	return out, nil
}

func (f *fakeStore) AuditForwarderByID(_ context.Context, id uint) (*db.AuditForwarder, error) {
	forwarder, ok := f.forwarderTable()[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	copied := *forwarder
	return &copied, nil
}

func (f *fakeStore) CreateAuditForwarder(_ context.Context, forwarder *db.AuditForwarder) error {
	for _, existing := range f.forwarderTable() {
		if existing.Name == forwarder.Name {
			return db.ErrConflict
		}
	}
	stored := f.addAuditForwarder(*forwarder)
	*forwarder = *stored
	return nil
}

func (f *fakeStore) UpdateAuditForwarder(_ context.Context, forwarder *db.AuditForwarder) error {
	table := f.forwarderTable()
	existing, ok := table[forwarder.ID]
	if !ok {
		return db.ErrNotFound
	}
	for id, other := range table {
		if id != forwarder.ID && other.Name == forwarder.Name {
			return db.ErrConflict
		}
	}
	// Delivery health is not in the store's update map either; keeping it here
	// is what lets a test assert that an edit does not erase it.
	updated := *forwarder
	updated.LastStatus = existing.LastStatus
	updated.LastMessage = existing.LastMessage
	updated.LastAttemptAt = existing.LastAttemptAt
	updated.CreatedAt = existing.CreatedAt
	table[forwarder.ID] = &updated
	*forwarder = updated
	return nil
}

func (f *fakeStore) DeleteAuditForwarder(_ context.Context, id uint) error {
	table := f.forwarderTable()
	if _, ok := table[id]; !ok {
		return db.ErrNotFound
	}
	delete(table, id)
	return nil
}

func (f *fakeStore) RecordAuditForwarderAttempt(_ context.Context, id uint, status, message string) error {
	forwarder, ok := f.forwarderTable()[id]
	if !ok {
		// The store's own behaviour: a row deleted mid-flush is not an error.
		return nil
	}
	now := time.Now()
	forwarder.LastStatus = status
	forwarder.LastMessage = message
	forwarder.LastAttemptAt = &now
	return nil
}
