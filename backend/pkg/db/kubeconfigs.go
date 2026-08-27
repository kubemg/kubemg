package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// KubeconfigFilter narrows a read of the register. Zero-valued fields are not
// applied, so the empty filter is "every credential this install ever issued,
// newest first".
type KubeconfigFilter struct {
	// UserID narrows to one holder. It is what the handler pins for a non-admin
	// caller, the audit trail's rule: the query parameter can narrow this
	// further, never widen it.
	UserID    uint
	ClusterID uint
	// ActiveOnly keeps the credentials that still work — neither revoked nor
	// past their expiry. It is the default reading of "who holds access right
	// now", which is the question the register exists to answer.
	ActiveOnly bool
	Now        time.Time

	Limit  int
	Offset int
}

// kubeconfigPageSize bounds a page so a wide-open read of a long-lived install
// cannot pull every credential it ever issued into memory.
const kubeconfigPageSize = 100

// CreateKubeconfigIssuance records a credential this console handed out.
func (s *Store) CreateKubeconfigIssuance(ctx context.Context, issuance *KubeconfigIssuance) error {
	if err := s.gdb.WithContext(ctx).Create(issuance).Error; err != nil {
		return fmt.Errorf("record kubeconfig issuance: %w", err)
	}
	return nil
}

// ListKubeconfigIssuances returns a page of the register newest first, with the
// total matching the filter so the console can page through it.
func (s *Store) ListKubeconfigIssuances(
	ctx context.Context, filter KubeconfigFilter,
) ([]KubeconfigIssuance, int64, error) {
	query := s.gdb.WithContext(ctx).Model(&KubeconfigIssuance{})

	if filter.UserID != 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.ClusterID != 0 {
		query = query.Where("cluster_id = ?", filter.ClusterID)
	}
	if filter.ActiveOnly {
		now := filter.Now
		if now.IsZero() {
			now = time.Now().UTC()
		}
		query = query.Where("revoked_at IS NULL AND expires_at > ?", now)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count kubeconfig issuances: %w", err)
	}

	limit := filter.Limit
	if limit <= 0 || limit > kubeconfigPageSize {
		limit = kubeconfigPageSize
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	rows := []KubeconfigIssuance{}
	if err := query.Order("created_at desc").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list kubeconfig issuances: %w", err)
	}
	return rows, total, nil
}

// KubeconfigIssuanceByID reads one row of the register.
func (s *Store) KubeconfigIssuanceByID(ctx context.Context, id uint) (*KubeconfigIssuance, error) {
	issuance := &KubeconfigIssuance{}
	err := s.gdb.WithContext(ctx).First(issuance, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("kubeconfig issuance by id: %w", err)
	}
	return issuance, nil
}

// RevokeKubeconfigIssuance closes one credential out. The row survives, and
// revoking an already-revoked credential is answered rather than written, so a
// second click does not rewrite when it stopped working.
func (s *Store) RevokeKubeconfigIssuance(
	ctx context.Context, id uint, at time.Time, by uint, byName string,
) (*KubeconfigIssuance, error) {
	issuance, err := s.KubeconfigIssuanceByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if issuance.Revoked() {
		return issuance, nil
	}
	updates := map[string]any{"revoked_at": at, "revoked_by": by, "revoked_by_name": byName}
	if err := s.gdb.WithContext(ctx).Model(&KubeconfigIssuance{}).
		Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("revoke kubeconfig issuance: %w", err)
	}
	issuance.RevokedAt = &at
	issuance.RevokedBy = by
	issuance.RevokedByName = byName
	return issuance, nil
}

// RevokeKubeconfigsForUser withdraws everything one person currently holds, and
// returns the rows as they were *before* the write.
//
// It is one action rather than N row writes because it is what an incident
// calls for: a laptop is gone, and nobody wants to work down a list. The rows
// come back so the caller can say what the action actually reached — an
// agent-mode credential stops on its next call, a direct-mode one does not stop
// at all, and only the caller has the vocabulary to explain that difference.
// Already-revoked and already-expired rows are skipped rather than restamped.
func (s *Store) RevokeKubeconfigsForUser(
	ctx context.Context, userID uint, at time.Time, by uint, byName string,
) ([]KubeconfigIssuance, error) {
	live := []KubeconfigIssuance{}
	err := s.gdb.WithContext(ctx).
		Where("user_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, at).
		Order("created_at desc").
		Find(&live).Error
	if err != nil {
		return nil, fmt.Errorf("list live kubeconfigs: %w", err)
	}
	if len(live) == 0 {
		return live, nil
	}

	ids := make([]uint, 0, len(live))
	for _, row := range live {
		ids = append(ids, row.ID)
	}
	updates := map[string]any{"revoked_at": at, "revoked_by": by, "revoked_by_name": byName}
	if err := s.gdb.WithContext(ctx).Model(&KubeconfigIssuance{}).
		Where("id IN ?", ids).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("revoke kubeconfigs for user: %w", err)
	}
	return live, nil
}

// RevokedKubeconfigTokenIDs is what the published snapshot is built from: the
// token ids the gateway must refuse.
//
// Only credentials that are revoked *and* have not yet expired are returned.
// An expired token is refused by its own signature check long before this set is
// consulted, so carrying it here would grow the snapshot forever to answer a
// question nothing asks. Direct-mode rows are excluded for the opposite reason:
// their token ids appear in no credential, so listing them would be a set of
// ids that can never match.
func (s *Store) RevokedKubeconfigTokenIDs(ctx context.Context, now time.Time) ([]string, error) {
	ids := []string{}
	err := s.gdb.WithContext(ctx).Model(&KubeconfigIssuance{}).
		Where("revoked_at IS NOT NULL AND expires_at > ? AND connection_mode = ?", now, ModeAgent).
		Pluck("token_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("list revoked kubeconfig token ids: %w", err)
	}
	return ids, nil
}

// TouchKubeconfigIssuance records that a credential was used. It is a bare
// column write with no read behind it, for the machine token's reason: the
// caller decides how often this is worth doing, and a failure here must never
// fail the call it was observing.
func (s *Store) TouchKubeconfigIssuance(ctx context.Context, tokenID string, at time.Time) error {
	err := s.gdb.WithContext(ctx).Model(&KubeconfigIssuance{}).
		Where("token_id = ?", tokenID).Update("last_used_at", at).Error
	if err != nil {
		return fmt.Errorf("touch kubeconfig issuance: %w", err)
	}
	return nil
}

// PruneKubeconfigIssuances drops register rows past the audit window. Retention
// follows the trail's, because that is what a register row is — the record of a
// credential having existed — and a register that outlived the audit records of
// the calls the credential made would be half an answer.
func (s *Store) PruneKubeconfigIssuances(ctx context.Context, before time.Time) (int64, error) {
	result := s.gdb.WithContext(ctx).
		Where("created_at < ?", before).
		Delete(&KubeconfigIssuance{})
	if result.Error != nil {
		return 0, fmt.Errorf("prune kubeconfig issuances: %w", result.Error)
	}
	return result.RowsAffected, nil
}
