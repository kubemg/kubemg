package api

import (
	"context"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The posture-acknowledgement half of fakeStore. It lives in its own file for
// the same reason the guardrail and alarm halves do.

func (f *fakeStore) postureAckTable() map[string]*db.PostureAcknowledgement {
	if f.postureAcks == nil {
		f.postureAcks = map[string]*db.PostureAcknowledgement{}
	}
	return f.postureAcks
}

func (f *fakeStore) ListPostureAcknowledgements(
	_ context.Context, clusterID uint,
) ([]db.PostureAcknowledgement, error) {
	out := []db.PostureAcknowledgement{}
	for _, ack := range f.postureAckTable() {
		if ack.ClusterID == clusterID {
			out = append(out, *ack)
		}
	}
	return out, nil
}

func (f *fakeStore) AcknowledgePostureFinding(_ context.Context, ack *db.PostureAcknowledgement) error {
	table := f.postureAckTable()
	key := postureAckKey(ack.Kind, ack.Namespace, ack.Name, ack.Rule)
	now := time.Now().UTC()
	if existing, ok := table[key]; ok {
		ack.ID = existing.ID
		ack.CreatedAt = existing.CreatedAt
	} else {
		ack.ID = f.nextID
		f.nextID++
		ack.CreatedAt = now
	}
	ack.UpdatedAt = now
	stored := *ack
	table[key] = &stored
	return nil
}

func (f *fakeStore) UnacknowledgePostureFinding(
	_ context.Context, clusterID uint, kind, namespace, name, rule string,
) error {
	table := f.postureAckTable()
	key := postureAckKey(kind, namespace, name, rule)
	existing, ok := table[key]
	if !ok || existing.ClusterID != clusterID {
		return db.ErrNotFound
	}
	delete(table, key)
	return nil
}
