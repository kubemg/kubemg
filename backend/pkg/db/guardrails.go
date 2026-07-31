package db

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ListGuardrailPolicies returns every rule, global ones first and then by id, so
// the list reads the way the engine evaluates it and does not reorder itself as
// rules are edited.
func (s *Store) ListGuardrailPolicies(ctx context.Context) ([]GuardrailPolicy, error) {
	policies := []GuardrailPolicy{}
	if err := s.gdb.WithContext(ctx).
		Order("cluster_id asc, id asc").
		Find(&policies).Error; err != nil {
		return nil, fmt.Errorf("list guardrail policies: %w", err)
	}
	return policies, nil
}

// GuardrailPolicyByID loads one rule.
func (s *Store) GuardrailPolicyByID(ctx context.Context, id uint) (*GuardrailPolicy, error) {
	var policy GuardrailPolicy
	err := s.gdb.WithContext(ctx).First(&policy, id).Error
	switch {
	case err == nil:
		return &policy, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, ErrNotFound
	default:
		return nil, fmt.Errorf("load guardrail policy: %w", err)
	}
}

// CreateGuardrailPolicy stores a new rule.
func (s *Store) CreateGuardrailPolicy(ctx context.Context, policy *GuardrailPolicy) error {
	if err := s.gdb.WithContext(ctx).Create(policy).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ErrConflict
		}
		return fmt.Errorf("create guardrail policy: %w", err)
	}
	return nil
}

// UpdateGuardrailPolicy replaces a rule's editable fields.
//
// The columns are named rather than saved wholesale because Enabled is a false
// that has to be written: a struct-valued Save would treat "disable this rule"
// as a zero value and leave it armed, which is the one update that must not
// silently do nothing.
func (s *Store) UpdateGuardrailPolicy(ctx context.Context, policy *GuardrailPolicy) error {
	updates := map[string]any{
		"name":        policy.Name,
		"description": policy.Description,
		"cluster_id":  policy.ClusterID,
		"pattern":     policy.Pattern,
		"target":      policy.Target,
		"action":      policy.Action,
		"enabled":     policy.Enabled,
	}
	result := s.gdb.WithContext(ctx).Model(&GuardrailPolicy{}).
		Where("id = ?", policy.ID).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("update guardrail policy: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return s.gdb.WithContext(ctx).First(policy, policy.ID).Error
}

// DeleteGuardrailPolicy removes a rule.
func (s *Store) DeleteGuardrailPolicy(ctx context.Context, id uint) error {
	result := s.gdb.WithContext(ctx).Delete(&GuardrailPolicy{}, id)
	if result.Error != nil {
		return fmt.Errorf("delete guardrail policy: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// guardrailDefaultsSeeded marks the one-time seed below as done, the same way
// the recording-access backfill marks itself. Without a marker the presets would
// come back on every boot, and a rule an administrator deliberately deleted
// reappearing is worse than never having seeded it: the second time, nobody
// trusts the list.
const guardrailDefaultsSeeded = "guardrail_defaults_seeded"

// SeedGuardrailPolicies installs the preset catalogue on a fresh database.
//
// Every seeded rule is stored **disabled**. That is the whole design of this
// function: guardrails refuse calls that RBAC permits, so an upgrade that armed
// them would start refusing, without warning, work an operator did yesterday and
// still has the privilege to do. Seeding them disabled makes the feature
// discoverable — the list is populated, each rule explains itself, and arming
// one is a toggle — without any install changing behaviour by being upgraded.
func SeedGuardrailPolicies(gdb *gorm.DB) error {
	var marked int64
	if err := gdb.Model(&Setting{}).
		Where("key = ?", guardrailDefaultsSeeded).
		Count(&marked).Error; err != nil {
		return fmt.Errorf("read guardrail seed marker: %w", err)
	}
	if marked > 0 {
		return nil
	}

	for _, template := range GuardrailTemplates {
		policy := GuardrailPolicy{
			Name:        template.Name,
			Description: template.Description,
			Pattern:     template.Pattern,
			Target:      template.Target,
			Action:      template.Action,
			Enabled:     false,
		}
		// The columns are named explicitly so `enabled` is in the INSERT whatever
		// default the column happens to carry — including one left behind in an
		// existing database by an earlier schema. Relying on the struct alone is
		// what let a false become a true here once already.
		if err := gdb.Select(
			"name", "description", "cluster_id", "pattern", "target", "action",
			"enabled", "created_at", "updated_at",
		).Create(&policy).Error; err != nil {
			return fmt.Errorf("seed guardrail policy %q: %w", template.Key, err)
		}
	}

	if err := gdb.Save(&Setting{Key: guardrailDefaultsSeeded, Value: "1"}).Error; err != nil {
		return fmt.Errorf("mark guardrail seed: %w", err)
	}
	return nil
}
