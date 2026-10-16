package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrDeviceCodeTaken is a user code that collided with a live one. The caller
// draws another.
var ErrDeviceCodeTaken = errors.New("device user code already in use")

// CreateCLIDeviceCode files a sign-in request, clearing expired ones on the way
// in (PutWSTicket's shape).
func (s *Store) CreateCLIDeviceCode(ctx context.Context, code *CLIDeviceCode) error {
	gdb := s.gdb.WithContext(ctx)
	if err := gdb.Where("expires_at < ?", time.Now().UTC()).Delete(&CLIDeviceCode{}).Error; err != nil {
		return fmt.Errorf("prune device codes: %w", err)
	}
	code.ExpiresAt = code.ExpiresAt.UTC()
	res := gdb.Clauses(clause.OnConflict{DoNothing: true}).Create(code)
	if res.Error != nil {
		return fmt.Errorf("store device code: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrDeviceCodeTaken
	}
	return nil
}

// PendingCLIDeviceCodesFrom counts the live, unanswered requests one address
// has open — the bound that keeps an unauthenticated route from filling the
// table.
func (s *Store) PendingCLIDeviceCodesFrom(ctx context.Context, sourceIP string, now time.Time) (int64, error) {
	var n int64
	err := s.gdb.WithContext(ctx).Model(&CLIDeviceCode{}).
		Where("source_ip = ? AND status = ? AND expires_at > ?", sourceIP, CLIDevicePending, now).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("count device codes: %w", err)
	}
	return n, nil
}

// CLIDeviceCodeByUserCode reads a live request by the code a person typed.
func (s *Store) CLIDeviceCodeByUserCode(ctx context.Context, userCode string, now time.Time) (*CLIDeviceCode, error) {
	var code CLIDeviceCode
	err := s.gdb.WithContext(ctx).Where("user_code = ? AND expires_at > ?", userCode, now).First(&code).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load device code: %w", err)
	}
	return &code, nil
}

// CLIDeviceCodeByHash reads a request by the device code the CLI polls with,
// expired or not — an expired one is answered as such rather than as unknown.
func (s *Store) CLIDeviceCodeByHash(ctx context.Context, hash string) (*CLIDeviceCode, error) {
	var code CLIDeviceCode
	err := s.gdb.WithContext(ctx).Where("device_code_hash = ?", hash).First(&code).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load device code: %w", err)
	}
	return &code, nil
}

// DecideCLIDeviceCode settles a pending request. One conditional update: a
// request answered twice — two tabs, two people — is settled by the first.
func (s *Store) DecideCLIDeviceCode(
	ctx context.Context, userCode, status string, userID uint, now time.Time,
) (bool, error) {
	res := s.gdb.WithContext(ctx).Model(&CLIDeviceCode{}).
		Where("user_code = ? AND status = ? AND expires_at > ?", userCode, CLIDevicePending, now).
		Updates(map[string]any{"status": status, "user_id": userID})
	if res.Error != nil {
		return false, fmt.Errorf("decide device code: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

// PollCLIDeviceCode stamps a poll and reports the previous one, so the caller
// can answer slow_down to a CLI that polls faster than it was told to.
func (s *Store) PollCLIDeviceCode(ctx context.Context, hash string, now time.Time) error {
	if err := s.gdb.WithContext(ctx).Model(&CLIDeviceCode{}).
		Where("device_code_hash = ?", hash).Update("last_polled_at", now).Error; err != nil {
		return fmt.Errorf("stamp device poll: %w", err)
	}
	return nil
}

// TakeCLIDeviceCode spends a settled request: one DELETE … RETURNING, so of
// two polls that both saw it approved exactly one receives a session.
func (s *Store) TakeCLIDeviceCode(ctx context.Context, hash string) (*CLIDeviceCode, error) {
	var rows []CLIDeviceCode
	res := s.gdb.WithContext(ctx).
		Clauses(clause.Returning{}).
		Where("device_code_hash = ? AND status <> ?", hash, CLIDevicePending).
		Delete(&rows)
	if res.Error != nil {
		return nil, fmt.Errorf("redeem device code: %w", res.Error)
	}
	if res.RowsAffected == 0 || len(rows) == 0 {
		return nil, ErrNotFound
	}
	return &rows[0], nil
}

// CreateCLISession files a signed-in CLI.
func (s *Store) CreateCLISession(ctx context.Context, session *CLISession) error {
	if err := s.gdb.WithContext(ctx).Create(session).Error; err != nil {
		return fmt.Errorf("create cli session: %w", err)
	}
	return nil
}

// CLISessionByRefreshHash reads the session a refresh token names, live or not.
func (s *Store) CLISessionByRefreshHash(ctx context.Context, hash string) (*CLISession, error) {
	var session CLISession
	err := s.gdb.WithContext(ctx).Where("refresh_hash = ?", hash).First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load cli session: %w", err)
	}
	return &session, nil
}

// CLISessionByID reads one session.
func (s *Store) CLISessionByID(ctx context.Context, id string) (*CLISession, error) {
	var session CLISession
	err := s.gdb.WithContext(ctx).Where("id = ?", id).First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load cli session: %w", err)
	}
	return &session, nil
}

// ListCLISessions returns a page of sessions newest first, with the total.
func (s *Store) ListCLISessions(ctx context.Context, filter CLISessionFilter) ([]CLISession, int64, error) {
	query := s.gdb.WithContext(ctx).Model(&CLISession{})
	if filter.UserID != 0 {
		query = query.Where("user_id = ?", filter.UserID)
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
		return nil, 0, fmt.Errorf("count cli sessions: %w", err)
	}
	limit := filter.Limit
	if limit <= 0 || limit > kubeconfigPageSize {
		limit = kubeconfigPageSize
	}
	offset := max(filter.Offset, 0)
	rows := []CLISession{}
	if err := query.Order("created_at desc").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("list cli sessions: %w", err)
	}
	return rows, total, nil
}

// RevokeCLISession ends one session. Revoking one already revoked is answered,
// not rewritten.
func (s *Store) RevokeCLISession(ctx context.Context, id string, at time.Time, by uint, byName string) (*CLISession, error) {
	session, err := s.CLISessionByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if session.RevokedAt != nil {
		return session, nil
	}
	if err := s.gdb.WithContext(ctx).Model(&CLISession{}).Where("id = ? AND revoked_at IS NULL", id).
		Updates(map[string]any{"revoked_at": at, "revoked_by": by, "revoked_by_name": byName}).Error; err != nil {
		return nil, fmt.Errorf("revoke cli session: %w", err)
	}
	session.RevokedAt = &at
	session.RevokedBy = by
	session.RevokedByName = byName
	return session, nil
}

// RevokeCLISessionsForUser ends every live session one person holds and returns
// them as they were before the write.
func (s *Store) RevokeCLISessionsForUser(
	ctx context.Context, userID uint, at time.Time, by uint, byName string,
) ([]CLISession, error) {
	live := []CLISession{}
	if err := s.gdb.WithContext(ctx).
		Where("user_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, at).
		Order("created_at desc").Find(&live).Error; err != nil {
		return nil, fmt.Errorf("list live cli sessions: %w", err)
	}
	if len(live) == 0 {
		return live, nil
	}
	ids := make([]string, len(live))
	for i, row := range live {
		ids[i] = row.ID
	}
	if err := s.gdb.WithContext(ctx).Model(&CLISession{}).Where("id IN ?", ids).
		Updates(map[string]any{"revoked_at": at, "revoked_by": by, "revoked_by_name": byName}).Error; err != nil {
		return nil, fmt.Errorf("revoke cli sessions for user: %w", err)
	}
	return live, nil
}

// RevokedCLISessionIDs is the sessions' half of the published snapshot: ended
// by revocation and not yet past their own expiry. Nothing a session minted can
// outlive it — every access token's lifetime is capped at what the session has
// left — so an expired session needs no entry.
func (s *Store) RevokedCLISessionIDs(ctx context.Context, now time.Time) ([]string, error) {
	ids := []string{}
	err := s.gdb.WithContext(ctx).Model(&CLISession{}).
		Where("revoked_at IS NOT NULL AND expires_at > ?", now).
		Pluck("id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("list revoked cli sessions: %w", err)
	}
	return ids, nil
}

// TouchCLISession records a session's use. A bare column write, the register's
// rule; an id that names no session writes nothing.
func (s *Store) TouchCLISession(ctx context.Context, id string, at time.Time) error {
	if err := s.gdb.WithContext(ctx).Model(&CLISession{}).
		Where("id = ?", id).Update("last_used_at", at).Error; err != nil {
		return fmt.Errorf("touch cli session: %w", err)
	}
	return nil
}

// PruneCLISessions drops sessions that ended before the audit window, the
// register's retention rule.
func (s *Store) PruneCLISessions(ctx context.Context, before time.Time) (int64, error) {
	res := s.gdb.WithContext(ctx).Where("expires_at < ?", before).Delete(&CLISession{})
	if res.Error != nil {
		return 0, fmt.Errorf("prune cli sessions: %w", res.Error)
	}
	return res.RowsAffected, nil
}
