package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// ListAuditForwarders returns every configured destination, oldest first so the
// list does not reorder itself as delivery health changes.
func (s *Store) ListAuditForwarders(ctx context.Context) ([]AuditForwarder, error) {
	forwarders := []AuditForwarder{}
	if err := s.gdb.WithContext(ctx).Order("id asc").Find(&forwarders).Error; err != nil {
		return nil, fmt.Errorf("list audit forwarders: %w", err)
	}
	return forwarders, nil
}

// AuditForwarderByID loads one destination.
func (s *Store) AuditForwarderByID(ctx context.Context, id uint) (*AuditForwarder, error) {
	var forwarder AuditForwarder
	err := s.gdb.WithContext(ctx).First(&forwarder, id).Error
	switch {
	case err == nil:
		return &forwarder, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, ErrNotFound
	default:
		return nil, fmt.Errorf("load audit forwarder: %w", err)
	}
}

// CreateAuditForwarder stores a new destination.
func (s *Store) CreateAuditForwarder(ctx context.Context, forwarder *AuditForwarder) error {
	if err := s.gdb.WithContext(ctx).Create(forwarder).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ErrConflict
		}
		return fmt.Errorf("create audit forwarder: %w", err)
	}
	return nil
}

// UpdateAuditForwarder replaces a destination's editable fields.
//
// Delivery health is deliberately not in the map: it is written by the shipper
// on its own clock, and an edit saved while a flush is in flight must not roll
// back the answer to "is this destination working".
func (s *Store) UpdateAuditForwarder(ctx context.Context, forwarder *AuditForwarder) error {
	updates := map[string]any{
		"name":                     forwarder.Name,
		"kind":                     forwarder.Kind,
		"host":                     forwarder.Host,
		"port":                     forwarder.Port,
		"protocol":                 forwarder.Protocol,
		"facility":                 forwarder.Facility,
		"app_name":                 forwarder.AppName,
		"octet_counting":           forwarder.OctetCounting,
		"tls_ca_bundle":            forwarder.TLSCABundle,
		"tls_insecure_skip_verify": forwarder.TLSInsecureSkipVerify,
		"enabled":                  forwarder.Enabled,
	}

	result := s.gdb.WithContext(ctx).Model(&AuditForwarder{}).
		Where("id = ?", forwarder.ID).Updates(updates)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrDuplicatedKey) {
			return ErrConflict
		}
		return fmt.Errorf("update audit forwarder: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAuditForwarder removes a destination.
func (s *Store) DeleteAuditForwarder(ctx context.Context, id uint) error {
	result := s.gdb.WithContext(ctx).Delete(&AuditForwarder{}, id)
	if result.Error != nil {
		return fmt.Errorf("delete audit forwarder: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordAuditForwarderAttempt stamps the outcome of one delivery.
//
// A missing row is not an error here. The shipper holds its destinations in
// memory for the length of a flush, so a forwarder deleted mid-flush lands in
// exactly this call — and failing it would turn an ordinary deletion into a
// logged error every two seconds until the next reload.
func (s *Store) RecordAuditForwarderAttempt(ctx context.Context, id uint, status, message string) error {
	now := time.Now().UTC()
	if len(message) > 500 {
		message = message[:500]
	}
	err := s.gdb.WithContext(ctx).Model(&AuditForwarder{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"last_status":     status,
			"last_message":    message,
			"last_attempt_at": now,
		}).Error
	if err != nil {
		return fmt.Errorf("record audit forwarder attempt: %w", err)
	}
	return nil
}
