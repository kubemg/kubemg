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
		&TerminalSession{},
		&Setting{},
		&ObservabilitySource{},
		&ClusterConsole{},
		&SSOProviderConfig{},
		&SSOGroupMapping{},
		&AlarmChannel{},
		&AlarmRule{},
		&GuardrailPolicy{},
		&JitRequest{},
	); err != nil {
		return fmt.Errorf("automigrate: %w", err)
	}

	if err := widenUserAccessUniqueness(gdb); err != nil {
		return err
	}

	// Every account predating federation is a local one, and every grant and
	// membership predating it was written by an administrator. Saying so
	// explicitly matters more here than elsewhere: the federation sync deletes
	// the rows it considers its own, and a blank provenance it read as "mine"
	// would revoke access nobody asked it to touch.
	if err := gdb.Model(&User{}).
		Where("auth_source IS NULL OR auth_source = ''").
		Update("auth_source", AuthSourceLocal).Error; err != nil {
		return fmt.Errorf("backfill auth_source: %w", err)
	}
	if err := gdb.Model(&UserClusterAccess{}).
		Where("source IS NULL OR source = ''").
		Update("source", GrantSourceLocal).Error; err != nil {
		return fmt.Errorf("backfill user access source: %w", err)
	}
	if err := gdb.Model(&UserGroup{}).
		Where("source IS NULL OR source = ''").
		Update("source", GrantSourceLocal).Error; err != nil {
		return fmt.Errorf("backfill membership source: %w", err)
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

	if err := backfillRecordingAccess(gdb); err != nil {
		return err
	}
	// The preset guardrails, stored disabled. See SeedGuardrailPolicies: a rule
	// that refuses what RBAC permits must never arrive armed by way of an upgrade.
	if err := SeedGuardrailPolicies(gdb); err != nil {
		return err
	}
	return nil
}

// legacyUserAccessIndex is the unique index that used to hold one grant per
// (user, cluster). AutoMigrate creates the wider one beside it but never removes
// this, and while it exists a time-bound elevation cannot be inserted next to the
// standing grant it elevates — the insert collides and the approval fails.
const legacyUserAccessIndex = "idx_user_cluster"

// widenUserAccessUniqueness drops that index once the wider one exists.
//
// It runs after AutoMigrate on purpose: the window in which neither index exists
// has to be zero, because the constraint is what stops two rows of the same
// provenance from being two answers to one question. Dropping an index that is
// already gone is not an error, so this is safe on every boot after the first.
func widenUserAccessUniqueness(gdb *gorm.DB) error {
	migrator := gdb.Migrator()
	if !migrator.HasIndex(&UserClusterAccess{}, legacyUserAccessIndex) {
		return nil
	}
	if !migrator.HasIndex(&UserClusterAccess{}, "idx_user_cluster_source") {
		// The replacement is missing, so dropping this would leave the table with
		// no uniqueness at all. Refusing is right: it means AutoMigrate did not do
		// what this function assumes, and guessing would be worse than stopping.
		return fmt.Errorf("cannot drop %s: the replacement index is missing", legacyUserAccessIndex)
	}
	if err := migrator.DropIndex(&UserClusterAccess{}, legacyUserAccessIndex); err != nil {
		return fmt.Errorf("drop legacy user access index: %w", err)
	}
	return nil
}

// recordingAccessBackfilled marks the one-time grant below as done. It has to be
// recorded, and the settings table is where this schema already keeps facts about
// itself: without a marker the backfill would run on every boot and re-grant a
// capability an administrator had deliberately revoked, which would make the
// whole control decorative.
const recordingAccessBackfilled = "recording_access_backfilled"

// backfillRecordingAccess grandfathers existing administrators into the
// recording-viewer capability.
//
// The capability is new and defaults to off, so introducing it would otherwise
// silently take the recordings page away from every admin of a running install —
// an upgrade must not quietly remove access somebody had yesterday. New accounts
// start without it, so the default for anything created from here on is the
// restrictive one.
func backfillRecordingAccess(gdb *gorm.DB) error {
	var marked int64
	if err := gdb.Model(&Setting{}).
		Where("key = ?", recordingAccessBackfilled).
		Count(&marked).Error; err != nil {
		return fmt.Errorf("read recording access marker: %w", err)
	}
	if marked > 0 {
		return nil
	}

	if err := gdb.Model(&User{}).
		Where("system_role IN ?", []string{SystemRoleAdmin, SystemRoleSuperAdmin}).
		Update("can_view_recordings", true).Error; err != nil {
		return fmt.Errorf("backfill recording access: %w", err)
	}
	if err := gdb.Save(&Setting{
		Key:   recordingAccessBackfilled,
		Value: "1",
	}).Error; err != nil {
		return fmt.Errorf("mark recording access backfill: %w", err)
	}
	return nil
}
