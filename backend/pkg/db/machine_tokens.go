package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// CreateMachineToken stores an issued credential. The caller holds the only
// copy of the secret; what lands here is its hash.
func (s *Store) CreateMachineToken(ctx context.Context, token *MachineToken) error {
	if err := s.gdb.WithContext(ctx).Create(token).Error; err != nil {
		return fmt.Errorf("create service token: %w", err)
	}
	return nil
}

// MachineTokenByHash finds a token by the hash of its secret. It is the read on
// the authentication path, which is why it is a single indexed lookup and why
// nothing about the presented secret is logged on the way past.
func (s *Store) MachineTokenByHash(ctx context.Context, hash string) (*MachineToken, error) {
	token := &MachineToken{}
	err := s.gdb.WithContext(ctx).Where("token_hash = ?", hash).First(token).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("service token by hash: %w", err)
	}
	return token, nil
}

// ListMachineTokens returns an account's tokens, newest first. A zero userID
// lists every token on the install, which is what an administrator auditing
// outstanding credentials asks for.
func (s *Store) ListMachineTokens(ctx context.Context, userID uint) ([]MachineToken, error) {
	tokens := []MachineToken{}
	query := s.gdb.WithContext(ctx).Order("created_at desc")
	if userID != 0 {
		query = query.Where("user_id = ?", userID)
	}
	if err := query.Find(&tokens).Error; err != nil {
		return nil, fmt.Errorf("list service tokens: %w", err)
	}
	return tokens, nil
}

// MachineTokenByID reads one token row.
func (s *Store) MachineTokenByID(ctx context.Context, id uint) (*MachineToken, error) {
	token := &MachineToken{}
	err := s.gdb.WithContext(ctx).First(token, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("service token by id: %w", err)
	}
	return token, nil
}

// RevokeMachineToken closes a token out. The row survives: a credential that
// once existed is a fact, and deleting it would leave the audit records it
// produced pointing at nothing.
//
// Revoking an already-revoked token is answered rather than written, so a second
// click does not rewrite when it stopped working.
func (s *Store) RevokeMachineToken(ctx context.Context, id uint, at time.Time) (*MachineToken, error) {
	token, err := s.MachineTokenByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if token.Revoked() {
		return token, nil
	}
	if err := s.gdb.WithContext(ctx).Model(&MachineToken{}).
		Where("id = ?", id).Update("revoked_at", at).Error; err != nil {
		return nil, fmt.Errorf("revoke service token: %w", err)
	}
	token.RevokedAt = &at
	return token, nil
}

// TouchMachineToken records that a token was used. It is deliberately a bare
// column write with no read behind it: the caller decides how often this is
// worth doing, and a failure here must never fail the call it was observing.
func (s *Store) TouchMachineToken(ctx context.Context, id uint, at time.Time) error {
	err := s.gdb.WithContext(ctx).Model(&MachineToken{}).
		Where("id = ?", id).Update("last_used_at", at).Error
	if err != nil {
		return fmt.Errorf("touch service token: %w", err)
	}
	return nil
}
