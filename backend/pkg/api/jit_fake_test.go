package api

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// The JIT half of fakeStore. Like the alarm and guardrail halves it lives in its
// own file, and like them it stands in for one table — plus the grant rows the
// workflow writes, which are the other half of what the tests assert on: an
// approval that does not produce a grant is the bug worth catching.

// clock is the fake's notion of now, defaulting to the wall clock.
func (f *fakeStore) clock() time.Time {
	if f.now != nil {
		return f.now()
	}
	return time.Now().UTC()
}

func (f *fakeStore) jitTable() map[string]*db.JitRequest {
	if f.jit == nil {
		f.jit = map[string]*db.JitRequest{}
	}
	return f.jit
}

// addJitRequest seeds a request.
func (f *fakeStore) addJitRequest(request db.JitRequest) *db.JitRequest {
	table := f.jitTable()
	if request.ID == "" {
		request.ID = "seeded-" + itoa(f.nextID)
		f.nextID++
	}
	if request.Status == "" {
		request.Status = db.JitStatusPending
	}
	if request.CreatedAt.IsZero() {
		request.CreatedAt = time.Now().UTC()
	}
	request.UpdatedAt = request.CreatedAt
	table[request.ID] = &request
	return &request
}

func (f *fakeStore) CreateJitRequest(_ context.Context, request *db.JitRequest) error {
	if f.createErr != nil {
		return f.createErr
	}
	stored := *request
	if stored.CreatedAt.IsZero() {
		stored.CreatedAt = time.Now().UTC()
		stored.UpdatedAt = stored.CreatedAt
	}
	f.jitTable()[stored.ID] = &stored
	*request = stored
	return nil
}

func (f *fakeStore) JitRequestByID(_ context.Context, id string) (*db.JitRequest, error) {
	request, ok := f.jitTable()[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	out := *request
	return &out, nil
}

func (f *fakeStore) ListJitRequests(
	_ context.Context, filter db.JitRequestFilter,
) ([]db.JitRequest, error) {
	out := []db.JitRequest{}
	for _, request := range f.jitTable() {
		if len(filter.Statuses) > 0 && !slices.Contains(filter.Statuses, request.Status) {
			continue
		}
		if filter.RequesterID != 0 && request.RequesterID != filter.RequesterID {
			continue
		}
		if filter.ClusterID != 0 && request.ClusterID != filter.ClusterID {
			continue
		}
		out = append(out, *request)
	}
	// Newest first, like the real store — and by id as a tiebreak, because seeded
	// rows share a timestamp and a test asserting on the first row must not depend
	// on map iteration order.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (f *fakeStore) PendingJitRequestFor(
	_ context.Context, userID, clusterID uint,
) (*db.JitRequest, error) {
	for _, request := range f.jitTable() {
		if request.RequesterID == userID &&
			request.ClusterID == clusterID &&
			request.Status == db.JitStatusPending {
			out := *request
			return &out, nil
		}
	}
	return nil, db.ErrNotFound
}

func (f *fakeStore) ActivateJitRequest(
	_ context.Context, id string, decision db.JitDecision, grant db.UserClusterAccess,
) (*db.JitRequest, error) {
	request, ok := f.jitTable()[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	// The real store moves the status with a conditional update, so a second
	// approver loses; the fake has to refuse the same way or the handler test for it
	// would pass against a store that allowed it.
	if request.Status != db.JitStatusPending {
		return nil, db.ErrConflict
	}

	request.Status = db.JitStatusActive
	request.ApproverID = decision.ApproverID
	request.ApproverUsername = decision.ApproverUsername
	request.ApproverComment = decision.Comment
	request.ApprovedAt = &decision.At
	request.ExpiresAt = decision.ExpiresAt
	request.UpdatedAt = decision.At

	grant.Source = db.GrantSourceJIT
	f.putGrant(grant)

	out := *request
	return &out, nil
}

func (f *fakeStore) FinishJitRequest(
	_ context.Context, id string, from []string, status string, decision db.JitDecision,
) (*db.JitRequest, error) {
	request, ok := f.jitTable()[id]
	if !ok {
		return nil, db.ErrNotFound
	}
	if !slices.Contains(from, request.Status) {
		return nil, db.ErrConflict
	}

	wasLive := slices.Contains(db.JitLiveStatuses, request.Status)
	if request.Status == db.JitStatusPending {
		request.ApproverID = decision.ApproverID
		request.ApproverUsername = decision.ApproverUsername
	}
	if decision.Comment != "" {
		request.ApproverComment = decision.Comment
	}
	request.Status = status
	request.UpdatedAt = decision.At
	if wasLive {
		f.dropJitGrant(request.RequesterID, request.ClusterID)
	}

	out := *request
	return &out, nil
}

func (f *fakeStore) ExpireJitRequests(_ context.Context, now time.Time) ([]db.JitRequest, error) {
	expired := []db.JitRequest{}
	for _, request := range f.jitTable() {
		if !slices.Contains(db.JitLiveStatuses, request.Status) {
			continue
		}
		if request.ExpiresAt == nil || request.ExpiresAt.After(now) {
			continue
		}
		request.Status = db.JitStatusExpired
		request.UpdatedAt = now
		f.dropJitGrant(request.RequesterID, request.ClusterID)
		expired = append(expired, *request)
	}
	return expired, nil
}

func (f *fakeStore) OrphanedJitRequests(_ context.Context) ([]db.JitRequest, error) {
	out := []db.JitRequest{}
	for _, request := range f.jitTable() {
		if !slices.Contains(db.JitLiveStatuses, request.Status) {
			continue
		}
		if _, held := f.grantOf(request.RequesterID, request.ClusterID, db.GrantSourceJIT); !held {
			out = append(out, *request)
		}
	}
	return out, nil
}

func (f *fakeStore) PruneJitRequests(_ context.Context, before time.Time) (int64, error) {
	var removed int64
	for id, request := range f.jitTable() {
		if slices.Contains(db.JitLiveStatuses, request.Status) {
			continue
		}
		if request.UpdatedAt.Before(before) {
			delete(f.jitTable(), id)
			removed++
		}
	}
	return removed, nil
}

/* ------------------------------------------------------------------ grants --- */

// The fake keeps grants keyed the way the table is — by (user, cluster, source) —
// so an elevation sits *beside* a standing grant rather than replacing it. That is
// the property the whole feature rests on, and a fake that flattened it would make
// the expiry tests meaningless.

func (f *fakeStore) putGrant(grant db.UserClusterAccess) {
	if f.jitGrants == nil {
		f.jitGrants = map[uint]map[uint]db.UserClusterAccess{}
	}
	if f.jitGrants[grant.UserID] == nil {
		f.jitGrants[grant.UserID] = map[uint]db.UserClusterAccess{}
	}
	f.jitGrants[grant.UserID][grant.ClusterID] = grant
}

func (f *fakeStore) grantOf(
	userID, clusterID uint, source string,
) (db.UserClusterAccess, bool) {
	if source != db.GrantSourceJIT {
		grant, ok := f.access[userID][clusterID]
		return grant, ok
	}
	grant, ok := f.jitGrants[userID][clusterID]
	return grant, ok
}

func (f *fakeStore) dropJitGrant(userID, clusterID uint) {
	if f.jitGrants[userID] == nil {
		return
	}
	delete(f.jitGrants[userID], clusterID)
}
