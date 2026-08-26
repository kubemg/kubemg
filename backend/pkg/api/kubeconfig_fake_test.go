package api

import (
	"context"
	"sort"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The kubeconfig_issuances table, as the fake store holds it. A slice rather
// than a map, because "newest first" is part of what the register's read
// promises and a map would not preserve it.

func (f *fakeStore) CreateKubeconfigIssuance(_ context.Context, issuance *db.KubeconfigIssuance) error {
	if f.issuanceErr != nil {
		return f.issuanceErr
	}
	issuance.ID = f.nextID
	f.nextID++
	if issuance.CreatedAt.IsZero() {
		// Each row is stamped a millisecond after the last so ordering is
		// deterministic: two issuances inside one test would otherwise share a
		// timestamp and sort arbitrarily.
		issuance.CreatedAt = time.Now().UTC().Add(time.Duration(len(f.issuances)) * time.Millisecond)
	}
	stored := *issuance
	f.issuances = append(f.issuances, &stored)
	return nil
}

func (f *fakeStore) ListKubeconfigIssuances(
	_ context.Context, filter db.KubeconfigFilter,
) ([]db.KubeconfigIssuance, int64, error) {
	if f.issuanceErr != nil {
		return nil, 0, f.issuanceErr
	}
	now := filter.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	out := []db.KubeconfigIssuance{}
	for _, row := range f.issuances {
		if filter.UserID != 0 && row.UserID != filter.UserID {
			continue
		}
		if filter.ClusterID != 0 && row.ClusterID != filter.ClusterID {
			continue
		}
		if filter.ActiveOnly && (row.Revoked() || row.Expired(now)) {
			continue
		}
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, int64(len(out)), nil
}

func (f *fakeStore) KubeconfigIssuanceByID(_ context.Context, id uint) (*db.KubeconfigIssuance, error) {
	for _, row := range f.issuances {
		if row.ID == id {
			out := *row
			return &out, nil
		}
	}
	return nil, db.ErrNotFound
}

func (f *fakeStore) RevokeKubeconfigIssuance(
	_ context.Context, id uint, at time.Time, by uint, byName string,
) (*db.KubeconfigIssuance, error) {
	for _, row := range f.issuances {
		if row.ID != id {
			continue
		}
		if row.RevokedAt == nil {
			stamp := at
			row.RevokedAt = &stamp
			row.RevokedBy = by
			row.RevokedByName = byName
		}
		out := *row
		return &out, nil
	}
	return nil, db.ErrNotFound
}

func (f *fakeStore) RevokeKubeconfigsForUser(
	_ context.Context, userID uint, at time.Time, by uint, byName string,
) ([]db.KubeconfigIssuance, error) {
	if f.issuanceErr != nil {
		return nil, f.issuanceErr
	}
	live := []db.KubeconfigIssuance{}
	for _, row := range f.issuances {
		if row.UserID != userID || row.Revoked() || row.Expired(at) {
			continue
		}
		live = append(live, *row)
		stamp := at
		row.RevokedAt = &stamp
		row.RevokedBy = by
		row.RevokedByName = byName
	}
	sort.Slice(live, func(i, j int) bool { return live[i].CreatedAt.After(live[j].CreatedAt) })
	return live, nil
}

func (f *fakeStore) RevokedKubeconfigTokenIDs(_ context.Context, now time.Time) ([]string, error) {
	if f.revokedIDsErr != nil {
		return nil, f.revokedIDsErr
	}
	ids := []string{}
	for _, row := range f.issuances {
		if row.Revoked() && !row.Expired(now) && row.ConnectionMode == db.ModeAgent {
			ids = append(ids, row.TokenID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (f *fakeStore) TouchKubeconfigIssuance(_ context.Context, tokenID string, at time.Time) error {
	for _, row := range f.issuances {
		if row.TokenID == tokenID {
			stamp := at
			row.LastUsedAt = &stamp
			return nil
		}
	}
	return nil
}

func (f *fakeStore) PruneKubeconfigIssuances(_ context.Context, before time.Time) (int64, error) {
	if f.pruneErr != nil {
		return 0, f.pruneErr
	}
	kept := make([]*db.KubeconfigIssuance, 0, len(f.issuances))
	var removed int64
	for _, row := range f.issuances {
		if row.CreatedAt.Before(before) {
			removed++
			continue
		}
		kept = append(kept, row)
	}
	f.issuances = kept
	return removed, nil
}
