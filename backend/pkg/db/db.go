package db

import (
	"errors"
	"fmt"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/kubemg/kubemg/backend/pkg/config"
)

var (
	// ErrNotFound is returned when a requested record does not exist.
	ErrNotFound = errors.New("record not found")
	// ErrConflict is returned when a record violates a unique constraint.
	ErrConflict = errors.New("record already exists")
)

// Open dials PostgreSQL using the supplied settings.
func Open(cfg config.DB) (*gorm.DB, error) {
	gdb, err := gorm.Open(postgres.Open(cfg.DSN()), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Warn),
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	return gdb, nil
}

// Migrate applies the KubeMG schema.
func Migrate(gdb *gorm.DB) error {
	if err := gdb.AutoMigrate(
		&User{},
		&Cluster{},
		&UserClusterAccess{},
		&Group{},
		&UserGroup{},
		&GroupClusterAccess{},
		&AuditEvent{},
		&Setting{},
	); err != nil {
		return fmt.Errorf("automigrate: %w", err)
	}

	// Accounts predating the IAM schema have an empty system_role. Derive it
	// from the legacy role column so they stay usable.
	if err := gdb.Model(&User{}).
		Where("system_role IS NULL OR system_role = ''").
		Update("system_role", gorm.Expr("role")).Error; err != nil {
		return fmt.Errorf("backfill system_role: %w", err)
	}

	// Clusters registered before Phase 2 predate the connection mode column and
	// are all direct-mode by definition: they were registered with a stored API
	// URL and service account token.
	if err := gdb.Model(&Cluster{}).
		Where("connection_mode IS NULL OR connection_mode = ''").
		Update("connection_mode", ModeDirect).Error; err != nil {
		return fmt.Errorf("backfill connection_mode: %w", err)
	}

	// AutoMigrate fills the new column with its default instead of leaving it
	// empty, so a pre-IAM admin lands here as role=admin/system_role=user and
	// Normalize then demotes it on read. Every row written through Normalize
	// derives role *from* system_role, so that pairing can only come from the
	// backfill — repair it rather than silently stripping the account's access.
	if err := gdb.Model(&User{}).
		Where("role = ? AND system_role = ?", RoleAdmin, SystemRoleUser).
		Update("system_role", SystemRoleAdmin).Error; err != nil {
		return fmt.Errorf("repair backfilled system_role: %w", err)
	}
	return nil
}
