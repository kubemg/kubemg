package db

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm/clause"
)

// Setting keys. Each one shadows the environment variable of the same meaning:
// the environment supplies the boot-time default, a stored value overrides it
// at runtime so an operator can fix the address without a redeploy.
const (
	SettingPublicURL      = "public_url"
	SettingAgentImage     = "agent_image"
	SettingAgentNamespace = "agent_namespace"
	// SettingAuditRetentionDays is how long a proxied call stays in the audit
	// table. It is stored as a decimal string like every other setting so the
	// key/value table needs no second column.
	SettingAuditRetentionDays = "audit_retention_days"
)

// SettingKeys enumerates the runtime-configurable settings.
var SettingKeys = []string{
	SettingPublicURL,
	SettingAgentImage,
	SettingAgentNamespace,
	SettingAuditRetentionDays,
}

// Setting is one operator-configurable value. An empty Value means "unset" and
// reads as the configured default rather than as an empty string — that is how
// a setting is cleared without deleting the row.
type Setting struct {
	Key       string    `gorm:"primaryKey;size:64" json:"key"`
	Value     string    `gorm:"type:text" json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy uint      `json:"updated_by"`
}

// Settings returns every stored override keyed by setting name.
func (s *Store) Settings(ctx context.Context) (map[string]string, error) {
	var rows []Setting
	if err := s.gdb.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Key] = row.Value
	}
	return out, nil
}

// PutSettings upserts the supplied overrides. Keys absent from the map are left
// alone, so a caller can write one setting without reading the others first.
func (s *Store) PutSettings(ctx context.Context, values map[string]string, updatedBy uint) error {
	if len(values) == 0 {
		return nil
	}
	now := time.Now().UTC()
	rows := make([]Setting, 0, len(values))
	for key, value := range values {
		rows = append(rows, Setting{Key: key, Value: value, UpdatedAt: now, UpdatedBy: updatedBy})
	}

	err := s.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at", "updated_by"}),
	}).Create(&rows).Error
	if err != nil {
		return fmt.Errorf("put settings: %w", err)
	}
	return nil
}
