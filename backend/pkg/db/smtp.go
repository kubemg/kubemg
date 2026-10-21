package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// smtpConfigID is the one row this table holds. A server sends mail through
// one relay; a table rather than a settings key because the password must be
// sealed, and only a tagged column is.
const smtpConfigID = 1

// SMTPConfig is the relay KubeMG mails through.
type SMTPConfig struct {
	ID       uint   `gorm:"primaryKey" json:"-"`
	Host     string `gorm:"size:255;not null" json:"host"`
	Port     int    `gorm:"not null" json:"port"`
	Security string `gorm:"size:16;not null;default:starttls" json:"security"`
	Username string `gorm:"size:255" json:"username"`
	// Password is write-only: it is read back as has_password.
	Password  string    `gorm:"type:text;serializer:secret" json:"-"`
	From      string    `gorm:"column:from_address;size:320;not null" json:"from"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy uint      `json:"updated_by"`
}

// TableName pins the table.
func (SMTPConfig) TableName() string { return "smtp_configs" }

// SMTPConfig returns the relay, or ErrNotFound when none is configured.
func (s *Store) SMTPConfig(ctx context.Context) (*SMTPConfig, error) {
	var row SMTPConfig
	err := s.gdb.WithContext(ctx).First(&row, smtpConfigID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load smtp config: %w", err)
	}
	return &row, nil
}

// PutSMTPConfig stores the relay, replacing whatever was there.
func (s *Store) PutSMTPConfig(ctx context.Context, config *SMTPConfig) error {
	config.ID = smtpConfigID
	config.UpdatedAt = time.Now().UTC()
	err := s.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"host", "port", "security", "username", "password", "from_address", "updated_at", "updated_by",
		}),
	}).Create(config).Error
	if err != nil {
		return fmt.Errorf("put smtp config: %w", err)
	}
	return nil
}

// DeleteSMTPConfig removes the relay; mail stops.
func (s *Store) DeleteSMTPConfig(ctx context.Context) error {
	if err := s.gdb.WithContext(ctx).Delete(&SMTPConfig{}, smtpConfigID).Error; err != nil {
		return fmt.Errorf("delete smtp config: %w", err)
	}
	return nil
}
