package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// ListAlarmChannels returns every configured destination, oldest first so the
// list does not reorder itself as delivery health changes.
func (s *Store) ListAlarmChannels(ctx context.Context) ([]AlarmChannel, error) {
	channels := []AlarmChannel{}
	if err := s.gdb.WithContext(ctx).Order("id asc").Find(&channels).Error; err != nil {
		return nil, fmt.Errorf("list alarm channels: %w", err)
	}
	return channels, nil
}

// AlarmChannelByID loads one destination, credential included — this is the read
// the dispatcher uses, and it is why nothing above the store may hand a channel
// straight back to a client.
func (s *Store) AlarmChannelByID(ctx context.Context, id uint) (*AlarmChannel, error) {
	var channel AlarmChannel
	err := s.gdb.WithContext(ctx).First(&channel, id).Error
	switch {
	case err == nil:
		return &channel, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, ErrNotFound
	default:
		return nil, fmt.Errorf("load alarm channel: %w", err)
	}
}

// CreateAlarmChannel stores a new destination.
func (s *Store) CreateAlarmChannel(ctx context.Context, channel *AlarmChannel) error {
	if err := s.gdb.WithContext(ctx).Create(channel).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return ErrConflict
		}
		return fmt.Errorf("create alarm channel: %w", err)
	}
	return nil
}

// UpdateAlarmChannel replaces a destination's editable fields.
//
// The secret is written only when a new one was supplied, which is what lets an
// operator change a port or a header without re-typing a routing key they cannot
// read. It follows the rule the cluster token and the datasource credential
// already follow, and for the same reason: a field that has to be re-entered to
// save an unrelated change is a field that gets pasted from somewhere insecure.
func (s *Store) UpdateAlarmChannel(ctx context.Context, channel *AlarmChannel) error {
	updates := map[string]any{
		"name":       channel.Name,
		"kind":       channel.Kind,
		"url":        channel.URL,
		"auth_mode":  channel.AuthMode,
		"username":   channel.Username,
		"headers":    channel.Headers,
		"enabled":    channel.Enabled,
		"updated_at": time.Now().UTC(),
	}
	if channel.Secret != "" {
		updates["secret"] = channel.Secret
	}
	// Clearing authentication has to clear the credential with it, or a channel
	// switched to "none" keeps a live token nothing displays.
	if channel.AuthMode == AuthNone {
		updates["secret"] = ""
		updates["username"] = ""
	}

	res := s.gdb.WithContext(ctx).Model(&AlarmChannel{}).
		Where("id = ?", channel.ID).Updates(updates)
	if res.Error != nil {
		if errors.Is(res.Error, gorm.ErrDuplicatedKey) {
			return ErrConflict
		}
		return fmt.Errorf("update alarm channel: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAlarmChannel removes a destination and, with it, the rules that pointed
// at it.
//
// Cascading is the right answer rather than refusing the delete: a rule whose
// channel is gone is not a rule, it is a condition that matches and then goes
// nowhere — which looks exactly like working alarms right up to the incident
// nobody was paged for. Deleting them in the same transaction means the rule list
// never contains one.
func (s *Store) DeleteAlarmChannel(ctx context.Context, id uint) error {
	return s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Delete(&AlarmChannel{}, id)
		if res.Error != nil {
			return fmt.Errorf("delete alarm channel: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		if err := tx.Where("channel_id = ?", id).Delete(&AlarmRule{}).Error; err != nil {
			return fmt.Errorf("delete alarm rules for channel: %w", err)
		}
		return nil
	})
}

// RecordAlarmDelivery stores the verdict of one delivery attempt.
func (s *Store) RecordAlarmDelivery(ctx context.Context, id uint, status, message string) error {
	now := time.Now().UTC()
	err := s.gdb.WithContext(ctx).Model(&AlarmChannel{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"last_status":     status,
			"last_message":    message,
			"last_attempt_at": now,
		}).Error
	if err != nil {
		return fmt.Errorf("record alarm delivery: %w", err)
	}
	return nil
}

// ListAlarmRules returns every rule.
func (s *Store) ListAlarmRules(ctx context.Context) ([]AlarmRule, error) {
	rules := []AlarmRule{}
	if err := s.gdb.WithContext(ctx).Order("id asc").Find(&rules).Error; err != nil {
		return nil, fmt.Errorf("list alarm rules: %w", err)
	}
	return rules, nil
}

// AlarmRuleByID loads one rule.
func (s *Store) AlarmRuleByID(ctx context.Context, id uint) (*AlarmRule, error) {
	var rule AlarmRule
	err := s.gdb.WithContext(ctx).First(&rule, id).Error
	switch {
	case err == nil:
		return &rule, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, ErrNotFound
	default:
		return nil, fmt.Errorf("load alarm rule: %w", err)
	}
}

// CreateAlarmRule stores a new rule.
func (s *Store) CreateAlarmRule(ctx context.Context, rule *AlarmRule) error {
	if err := s.gdb.WithContext(ctx).Create(rule).Error; err != nil {
		return fmt.Errorf("create alarm rule: %w", err)
	}
	return nil
}

// UpdateAlarmRule replaces a rule's editable fields. The fire counters are left
// alone: they are the dispatcher's record of what this rule has done, not part of
// what an operator is editing.
func (s *Store) UpdateAlarmRule(ctx context.Context, rule *AlarmRule) error {
	res := s.gdb.WithContext(ctx).Model(&AlarmRule{}).
		Where("id = ?", rule.ID).
		Updates(map[string]any{
			"name":            rule.Name,
			"description":     rule.Description,
			"channel_id":      rule.ChannelID,
			"enabled":         rule.Enabled,
			"trigger":         rule.Trigger,
			"cluster_id":      rule.ClusterID,
			"namespaces":      rule.Namespaces,
			"event_reasons":   rule.EventReasons,
			"event_type":      rule.EventType,
			"verbs":           rule.Verbs,
			"denied_only":     rule.DeniedOnly,
			"min_status":      rule.MinStatus,
			"severity":        rule.Severity,
			"cooloff_seconds": rule.CooloffSeconds,
			"updated_at":      time.Now().UTC(),
		})
	if res.Error != nil {
		return fmt.Errorf("update alarm rule: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAlarmRule removes a rule.
func (s *Store) DeleteAlarmRule(ctx context.Context, id uint) error {
	res := s.gdb.WithContext(ctx).Delete(&AlarmRule{}, id)
	if res.Error != nil {
		return fmt.Errorf("delete alarm rule: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordAlarmFired stamps a rule that has just delivered. It is a bare UPDATE
// rather than a read-modify-write because two dispatches racing on the same rule
// should both be counted, and neither cares which one wrote the timestamp.
func (s *Store) RecordAlarmFired(ctx context.Context, id uint, at time.Time) error {
	err := s.gdb.WithContext(ctx).Model(&AlarmRule{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"last_fired_at": at,
			"fire_count":    gorm.Expr("fire_count + 1"),
		}).Error
	if err != nil {
		return fmt.Errorf("record alarm fired: %w", err)
	}
	return nil
}
