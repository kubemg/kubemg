package db

import (
	"context"
	"fmt"
	"time"
)

// PasswordAccount is what the password age policy needs to know about one
// account: its clock, an administrator's forced expiry, where a reminder goes
// and which reminder it last got.
type PasswordAccount struct {
	ID                 uint
	Username           string
	Email              string
	CreatedAt          time.Time
	PasswordChangedAt  *time.Time
	PasswordExpiredAt  *time.Time
	PasswordNoticeDue  *time.Time
	PasswordNoticeDays int
}

// PasswordAccounts lists every account whose password is held here: active,
// local and a person. A federated account's password is its provider's (an
// LDAP bind included), and a machine account has none.
func (s *Store) PasswordAccounts(ctx context.Context) ([]PasswordAccount, error) {
	var out []PasswordAccount
	err := s.gdb.WithContext(ctx).Model(&User{}).
		Select("id, username, email, created_at, password_changed_at, password_expired_at, "+
			"password_notice_due, password_notice_days").
		Where("is_active = ? AND (auth_source = ? OR auth_source = '') AND account_type <> ?",
			true, AuthSourceLocal, AccountTypeMachine).
		Order("id asc").
		Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list password accounts: %w", err)
	}
	return out, nil
}

// ExpirePassword forces an account to change its password at its next
// request, and reports whether this call did it. An account already forced is
// answered, not written: the original moment stays the one on record.
func (s *Store) ExpirePassword(ctx context.Context, userID uint, at time.Time) (bool, error) {
	res := s.gdb.WithContext(ctx).Model(&User{}).
		Where("id = ? AND password_expired_at IS NULL", userID).
		UpdateColumn("password_expired_at", at)
	if res.Error != nil {
		return false, fmt.Errorf("expire password: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// RecordPasswordNotice notes that the reminder for due at stage days was
// mailed, so the next pass does not mail it again.
func (s *Store) RecordPasswordNotice(ctx context.Context, userID uint, due time.Time, days int) error {
	err := s.gdb.WithContext(ctx).Model(&User{}).Where("id = ?", userID).
		UpdateColumns(map[string]any{"password_notice_due": due, "password_notice_days": days}).Error
	if err != nil {
		return fmt.Errorf("record password notice: %w", err)
	}
	return nil
}
