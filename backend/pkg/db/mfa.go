package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// pendingFactorTTL is how long an enrolment may sit between the QR code being
// shown and the first code being typed.
const pendingFactorTTL = 15 * time.Minute

// MFAState reads an account's confirmed factors and how many recovery codes it
// has left.
func (s *Store) MFAState(ctx context.Context, userID uint) (MFAState, error) {
	gdb := s.gdb.WithContext(ctx)
	state := MFAState{Factors: []MFAFactor{}}
	if err := gdb.Where("user_id = ? AND confirmed_at IS NOT NULL", userID).
		Order("id").Find(&state.Factors).Error; err != nil {
		return MFAState{}, fmt.Errorf("list mfa factors: %w", err)
	}
	if err := gdb.Model(&MFARecoveryCode{}).
		Where("user_id = ? AND used_at IS NULL", userID).
		Count(&state.RecoveryCodesRemaining).Error; err != nil {
		return MFAState{}, fmt.Errorf("count recovery codes: %w", err)
	}
	return state, nil
}

// MFAEnrolledUserIDs lists every account holding a confirmed factor.
func (s *Store) MFAEnrolledUserIDs(ctx context.Context) ([]uint, error) {
	var ids []uint
	if err := s.gdb.WithContext(ctx).Model(&MFAFactor{}).
		Where("confirmed_at IS NOT NULL").
		Distinct("user_id").Pluck("user_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("list mfa enrolled users: %w", err)
	}
	return ids, nil
}

// MFAFactorByID reads one of an account's factors, confirmed or not. Another
// account's factor is not found.
func (s *Store) MFAFactorByID(ctx context.Context, userID, id uint) (*MFAFactor, error) {
	var factor MFAFactor
	err := s.gdb.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&factor).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load mfa factor: %w", err)
	}
	return &factor, nil
}

// CreatePendingMFAFactor files an unconfirmed factor. The account's previous
// unconfirmed one goes, and so does anybody's that has been waiting longer than
// pendingFactorTTL — enrolment is rare enough that the delete in front of it
// costs nothing a sweeper would save.
func (s *Store) CreatePendingMFAFactor(ctx context.Context, factor *MFAFactor) error {
	return s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cutoff := time.Now().UTC().Add(-pendingFactorTTL)
		if err := tx.Where("confirmed_at IS NULL AND (user_id = ? OR created_at < ?)", factor.UserID, cutoff).
			Delete(&MFAFactor{}).Error; err != nil {
			return fmt.Errorf("prune pending mfa factors: %w", err)
		}
		factor.ConfirmedAt = nil
		if err := tx.Create(factor).Error; err != nil {
			return fmt.Errorf("create mfa factor: %w", err)
		}
		return nil
	})
}

// ConfirmMFAFactor marks a pending factor confirmed at the step its first code
// matched. recoveryHashes, when non-nil, replaces the account's recovery codes
// in the same transaction — the first factor comes with a set, later ones do
// not touch it. A factor that is not pending, or not the account's, or older
// than pendingFactorTTL is not found.
func (s *Store) ConfirmMFAFactor(
	ctx context.Context, userID, id uint, step int64, at time.Time, recoveryHashes []string,
) error {
	return s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&MFAFactor{}).
			Where("id = ? AND user_id = ? AND confirmed_at IS NULL AND created_at >= ?",
				id, userID, at.Add(-pendingFactorTTL)).
			Updates(map[string]any{"confirmed_at": at, "last_used_at": at, "last_step": step})
		if res.Error != nil {
			return fmt.Errorf("confirm mfa factor: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		if recoveryHashes != nil {
			return replaceRecoveryCodes(tx, userID, recoveryHashes)
		}
		return nil
	})
}

// AdvanceMFAStep accepts a TOTP code's step for a confirmed factor. It is one
// conditional update, so of two replicas presented the same code at the same
// instant exactly one gets true.
func (s *Store) AdvanceMFAStep(ctx context.Context, factorID uint, step int64, at time.Time) (bool, error) {
	res := s.gdb.WithContext(ctx).Model(&MFAFactor{}).
		Where("id = ? AND confirmed_at IS NOT NULL AND last_step < ?", factorID, step).
		Updates(map[string]any{"last_step": step, "last_used_at": at})
	if res.Error != nil {
		return false, fmt.Errorf("advance mfa step: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

// DeleteMFAFactor removes one of an account's confirmed factors. Removing the
// last one takes the recovery codes with it: a recovery code stands in for a
// factor, and with none left it would be a second password nobody remembers
// having.
func (s *Store) DeleteMFAFactor(ctx context.Context, userID, id uint) error {
	return s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id = ? AND user_id = ? AND confirmed_at IS NOT NULL", id, userID).Delete(&MFAFactor{})
		if res.Error != nil {
			return fmt.Errorf("delete mfa factor: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		var left int64
		if err := tx.Model(&MFAFactor{}).
			Where("user_id = ? AND confirmed_at IS NOT NULL", userID).Count(&left).Error; err != nil {
			return fmt.Errorf("count mfa factors: %w", err)
		}
		if left == 0 {
			if err := tx.Where("user_id = ?", userID).Delete(&MFARecoveryCode{}).Error; err != nil {
				return fmt.Errorf("delete recovery codes: %w", err)
			}
		}
		return nil
	})
}

// ReplaceMFARecoveryCodes swaps the account's recovery codes for a new set.
func (s *Store) ReplaceMFARecoveryCodes(ctx context.Context, userID uint, hashes []string) error {
	return s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return replaceRecoveryCodes(tx, userID, hashes)
	})
}

func replaceRecoveryCodes(tx *gorm.DB, userID uint, hashes []string) error {
	if err := tx.Where("user_id = ?", userID).Delete(&MFARecoveryCode{}).Error; err != nil {
		return fmt.Errorf("delete recovery codes: %w", err)
	}
	if len(hashes) == 0 {
		return nil
	}
	rows := make([]MFARecoveryCode, len(hashes))
	for i, hash := range hashes {
		rows[i] = MFARecoveryCode{UserID: userID, CodeHash: hash}
	}
	if err := tx.Create(&rows).Error; err != nil {
		return fmt.Errorf("store recovery codes: %w", err)
	}
	return nil
}

// UseMFARecoveryCode spends a recovery code. One conditional update: a code
// presented twice at once is spent once.
func (s *Store) UseMFARecoveryCode(ctx context.Context, userID uint, hash string, at time.Time) (bool, error) {
	res := s.gdb.WithContext(ctx).Model(&MFARecoveryCode{}).
		Where("user_id = ? AND code_hash = ? AND used_at IS NULL", userID, hash).
		Update("used_at", at)
	if res.Error != nil {
		return false, fmt.Errorf("use recovery code: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// ResetMFA removes every factor and recovery code an account holds, and reports
// how many confirmed factors went.
func (s *Store) ResetMFA(ctx context.Context, userID uint) (int64, error) {
	var removed int64
	err := s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&MFAFactor{}).
			Where("user_id = ? AND confirmed_at IS NOT NULL", userID).Count(&removed).Error; err != nil {
			return fmt.Errorf("count mfa factors: %w", err)
		}
		return deleteMFA(tx, userID)
	})
	return removed, err
}

func deleteMFA(tx *gorm.DB, userID uint) error {
	if err := tx.Where("user_id = ?", userID).Delete(&MFAFactor{}).Error; err != nil {
		return fmt.Errorf("delete mfa factors: %w", err)
	}
	if err := tx.Where("user_id = ?", userID).Delete(&MFARecoveryCode{}).Error; err != nil {
		return fmt.Errorf("delete recovery codes: %w", err)
	}
	if err := tx.Where("user_id = ?", userID).Delete(&MFAChallenge{}).Error; err != nil {
		return fmt.Errorf("delete mfa challenges: %w", err)
	}
	return nil
}

// PutMFAChallenge files a challenge until expiresAt, clearing expired ones on
// the way in — PutWSTicket's shape.
func (s *Store) PutMFAChallenge(ctx context.Context, challenge *MFAChallenge) error {
	gdb := s.gdb.WithContext(ctx)
	if err := gdb.Where("expires_at < ?", time.Now().UTC()).Delete(&MFAChallenge{}).Error; err != nil {
		return fmt.Errorf("prune mfa challenges: %w", err)
	}
	challenge.ExpiresAt = challenge.ExpiresAt.UTC()
	if err := gdb.Create(challenge).Error; err != nil {
		return fmt.Errorf("store mfa challenge: %w", err)
	}
	return nil
}

// MFAChallengeByHash reads a live challenge without spending it. A wrong code
// must leave the challenge standing — the throttle, not the challenge, is what
// bounds guessing — so reading and spending are two calls.
func (s *Store) MFAChallengeByHash(ctx context.Context, hash string) (*MFAChallenge, error) {
	var challenge MFAChallenge
	err := s.gdb.WithContext(ctx).
		Where("token_hash = ? AND expires_at > ?", hash, time.Now().UTC()).First(&challenge).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load mfa challenge: %w", err)
	}
	return &challenge, nil
}

// TakeMFAChallenge spends a challenge: one DELETE … RETURNING, so of two
// requests that both proved the factor exactly one gets a session.
func (s *Store) TakeMFAChallenge(ctx context.Context, hash string) (bool, error) {
	var rows []MFAChallenge
	res := s.gdb.WithContext(ctx).
		Clauses(clause.Returning{}).
		Where("token_hash = ? AND expires_at > ?", hash, time.Now().UTC()).
		Delete(&rows)
	if res.Error != nil {
		return false, fmt.Errorf("redeem mfa challenge: %w", res.Error)
	}
	return res.RowsAffected > 0 && len(rows) > 0, nil
}
