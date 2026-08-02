package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

/*
 * The JIT request table, and the two writes that are not just a row.
 *
 * Activating an approval and ending an elevation both touch two tables — the
 * request and the grant — and neither is safe half-applied. An approval recorded
 * without its grant is access somebody believes they have; a grant written
 * without its approval is access with no record of who allowed it. So both go
 * through one transaction, and the status is moved with the same statement that
 * decides whether the transition was legal, so two approvers clicking at once
 * cannot both win.
 */

// defaultJitPageSize bounds a listing that did not ask for a size. The approvals
// inbox is a working queue rather than an archive — a fleet with two hundred
// pending requests has a process problem, not a pagination problem.
const defaultJitPageSize = 200

// CreateJitRequest stores a new request.
func (s *Store) CreateJitRequest(ctx context.Context, request *JitRequest) error {
	if err := s.gdb.WithContext(ctx).Create(request).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ErrConflict
		}
		return fmt.Errorf("create jit request: %w", err)
	}
	return nil
}

// JitRequestByID loads one request.
func (s *Store) JitRequestByID(ctx context.Context, id string) (*JitRequest, error) {
	var request JitRequest
	err := s.gdb.WithContext(ctx).Where("id = ?", id).First(&request).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("jit request by id: %w", err)
	}
	return &request, nil
}

// ListJitRequests returns requests newest first.
//
// Newest first because this is read as an inbox and as a history, and both are
// asked "what has just happened" — the opposite of how the audit trail's own
// pruner walks the table.
func (s *Store) ListJitRequests(ctx context.Context, filter JitRequestFilter) ([]JitRequest, error) {
	q := s.gdb.WithContext(ctx).Model(&JitRequest{}).Order("created_at desc")
	if len(filter.Statuses) > 0 {
		q = q.Where("status IN ?", filter.Statuses)
	}
	if filter.RequesterID != 0 {
		q = q.Where("requester_id = ?", filter.RequesterID)
	}
	if filter.ClusterID != 0 {
		q = q.Where("cluster_id = ?", filter.ClusterID)
	}
	limit := filter.Limit
	if limit <= 0 || limit > defaultJitPageSize {
		limit = defaultJitPageSize
	}

	requests := []JitRequest{}
	if err := q.Limit(limit).Find(&requests).Error; err != nil {
		return nil, fmt.Errorf("list jit requests: %w", err)
	}
	return requests, nil
}

// CountPendingJitRequests is how many requests are waiting for a decision. The
// console badges it, and a count is a far cheaper question than the list.
func (s *Store) CountPendingJitRequests(ctx context.Context) (int64, error) {
	var n int64
	err := s.gdb.WithContext(ctx).Model(&JitRequest{}).
		Where("status = ?", JitStatusPending).Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("count pending jit requests: %w", err)
	}
	return n, nil
}

// PendingJitRequestFor finds an undecided request from this user for this
// cluster, which is what stops a queue filling with the same ask.
func (s *Store) PendingJitRequestFor(
	ctx context.Context, userID, clusterID uint,
) (*JitRequest, error) {
	var request JitRequest
	err := s.gdb.WithContext(ctx).
		Where("requester_id = ? AND cluster_id = ? AND status = ?", userID, clusterID, JitStatusPending).
		First(&request).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pending jit request: %w", err)
	}
	return &request, nil
}

// JitDecision is what an approver did, ready to stamp onto a request.
type JitDecision struct {
	ApproverID       uint
	ApproverUsername string
	Comment          string
	At               time.Time
	// ExpiresAt is set on an approval and nil on everything else.
	ExpiresAt *time.Time
}

// ActivateJitRequest records an approval and writes the grant it authorises, in
// one transaction.
//
// The status is moved with a conditional update from `pending`, and a zero row
// count is reported as ErrConflict rather than retried: two approvers pressing
// Approve on the same request must produce one grant and one audit record, and
// the loser has to be told the decision was already made rather than silently
// extending somebody else's window.
func (s *Store) ActivateJitRequest(
	ctx context.Context, id string, decision JitDecision, grant UserClusterAccess,
) (*JitRequest, error) {
	var out *JitRequest
	err := s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&JitRequest{}).
			Where("id = ? AND status = ?", id, JitStatusPending).
			Updates(map[string]any{
				"status":            JitStatusActive,
				"approver_id":       decision.ApproverID,
				"approver_username": decision.ApproverUsername,
				"approver_comment":  decision.Comment,
				"approved_at":       decision.At,
				"expires_at":        decision.ExpiresAt,
				"updated_at":        decision.At,
			})
		if res.Error != nil {
			return fmt.Errorf("approve jit request: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			// Either it is gone or somebody else decided it. The caller
			// distinguishes them by reading the row; both are a conflict.
			return ErrConflict
		}

		// The elevation is its own row, keyed by provenance, so the requester's
		// standing grant is untouched and comes back into force by itself when
		// this one expires.
		if err := putJitGrant(tx, grant); err != nil {
			return err
		}

		var stored JitRequest
		if err := tx.Where("id = ?", id).First(&stored).Error; err != nil {
			return fmt.Errorf("reload jit request: %w", err)
		}
		out = &stored
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FinishJitRequest moves a request to a terminal status and, where that status
// means the elevation is over, deletes the grant with it.
//
// `from` is the set of statuses the transition is legal from, applied in the same
// statement, for the same reason ActivateJitRequest does it: a rejection and an
// approval racing must not both succeed.
func (s *Store) FinishJitRequest(
	ctx context.Context, id string, from []string, status string, decision JitDecision,
) (*JitRequest, error) {
	var out *JitRequest
	err := s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stored JitRequest
		if err := tx.Where("id = ?", id).First(&stored).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return fmt.Errorf("load jit request: %w", err)
		}

		fields := map[string]any{
			"status":     status,
			"updated_at": decision.At,
		}
		// A revocation is a decision by somebody, and it is worth recording who —
		// but it must not overwrite the approver of the original grant, which is
		// the more important half of the record. So the comment carries it.
		if decision.Comment != "" {
			fields["approver_comment"] = decision.Comment
		}
		if stored.Status == JitStatusPending {
			fields["approver_id"] = decision.ApproverID
			fields["approver_username"] = decision.ApproverUsername
		}

		res := tx.Model(&JitRequest{}).
			Where("id = ? AND status IN ?", id, from).
			Updates(fields)
		if res.Error != nil {
			return fmt.Errorf("finish jit request: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrConflict
		}

		// Only a request that *was* live loses a grant. Deleting unconditionally
		// would look harmless — a rejected request never had one — but the row is
		// keyed by (user, cluster, source), so rejecting a second request for a
		// cluster would delete the elevation the first one is still carrying.
		if slices.Contains(JitLiveStatuses, stored.Status) {
			if err := deleteJitGrant(tx, stored.RequesterID, stored.ClusterID); err != nil {
				return err
			}
		}

		if err := tx.Where("id = ?", id).First(&stored).Error; err != nil {
			return fmt.Errorf("reload jit request: %w", err)
		}
		out = &stored
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ExpireJitRequests closes out every elevation whose window has passed and
// returns what it closed, so the caller can write the audit records.
//
// The grant is deleted here as housekeeping, not as enforcement: AccessForUser
// has already stopped honouring an expired row, so a sweeper that has not run for
// an hour has left rows behind rather than access behind.
func (s *Store) ExpireJitRequests(ctx context.Context, now time.Time) ([]JitRequest, error) {
	expired := []JitRequest{}
	err := s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("status IN ? AND expires_at IS NOT NULL AND expires_at <= ?",
			JitLiveStatuses, now).Find(&expired).Error
		if err != nil {
			return fmt.Errorf("find expired jit requests: %w", err)
		}
		if len(expired) == 0 {
			return nil
		}

		ids := make([]string, 0, len(expired))
		for _, request := range expired {
			ids = append(ids, request.ID)
			if err := deleteJitGrant(tx, request.RequesterID, request.ClusterID); err != nil {
				return err
			}
		}
		err = tx.Model(&JitRequest{}).Where("id IN ?", ids).Updates(map[string]any{
			"status":     JitStatusExpired,
			"updated_at": now,
		}).Error
		if err != nil {
			return fmt.Errorf("expire jit requests: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return expired, nil
}

// OrphanedJitRequests are requests that claim to be live while no grant backs
// them. That happens when an administrator revokes a user's access to a cluster
// outright: the grant rows go, and a request still saying "active" would show a
// countdown for privilege nobody holds.
func (s *Store) OrphanedJitRequests(ctx context.Context) ([]JitRequest, error) {
	requests := []JitRequest{}
	live := s.gdb.Model(&UserClusterAccess{}).
		Select("1").
		Where("user_cluster_access.user_id = jit_requests.requester_id").
		Where("user_cluster_access.cluster_id = jit_requests.cluster_id").
		Where("user_cluster_access.source = ?", GrantSourceJIT)

	err := s.gdb.WithContext(ctx).Model(&JitRequest{}).
		Where("status IN ?", JitLiveStatuses).
		Where("NOT EXISTS (?)", live).
		Find(&requests).Error
	if err != nil {
		return nil, fmt.Errorf("find orphaned jit requests: %w", err)
	}
	return requests, nil
}

// RevokeJitGrant removes a user's temporary elevation on a cluster, leaving
// every standing grant they hold alone.
func (s *Store) RevokeJitGrant(ctx context.Context, userID, clusterID uint) error {
	return deleteJitGrant(s.gdb.WithContext(ctx), userID, clusterID)
}

// putJitGrant upserts the elevation row. It targets the (user, cluster, source)
// index, so re-approving after an earlier window on the same cluster replaces
// that row rather than colliding with it.
func putJitGrant(tx *gorm.DB, grant UserClusterAccess) error {
	grant.Source = GrantSourceJIT
	err := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "cluster_id"}, {Name: "source"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"k8s_role", "namespaces", "expires_at",
		}),
	}).Create(&grant).Error
	if err != nil {
		return fmt.Errorf("activate jit grant: %w", err)
	}
	return nil
}

func deleteJitGrant(tx *gorm.DB, userID, clusterID uint) error {
	err := tx.Where("user_id = ? AND cluster_id = ? AND source = ?",
		userID, clusterID, GrantSourceJIT).
		Delete(&UserClusterAccess{}).Error
	if err != nil {
		return fmt.Errorf("revoke jit grant: %w", err)
	}
	return nil
}

// PruneJitRequests deletes decided requests older than a cutoff, returning how
// many went.
//
// It shares the audit window rather than having its own: a request is a record of
// who was given production and why, which is exactly the class of thing the audit
// retention setting is about. Anything still live is left alone whatever its age —
// a window nobody has closed is not history.
func (s *Store) PruneJitRequests(ctx context.Context, before time.Time) (int64, error) {
	res := s.gdb.WithContext(ctx).
		Where("status NOT IN ? AND updated_at < ?", JitLiveStatuses, before).
		Delete(&JitRequest{})
	if res.Error != nil {
		return 0, fmt.Errorf("prune jit requests: %w", res.Error)
	}
	return res.RowsAffected, nil
}
