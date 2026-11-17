package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

/*
 * Whether a cluster's vulnerability alerts are turned on, and from which scanner.
 *
 * KubeMG does not scan. A scanner already running in the cluster — Trivy
 * Operator in v1 — writes its findings as custom resources, and KubeMG reads
 * those. So what is stored here is not a datasource: there is no address to
 * dial and no credential to keep, only an administrator's decision that this
 * cluster's reports should be read, which scanner wrote them, and what the last
 * look at them found.
 *
 * It is its own row rather than an observability_sources kind for that reason:
 * every column there describes a Service KubeMG talks to, and none of them
 * applies to a CRD read through the agent as KubeMG.
 */

// ScannerTrivyOperator is Aqua Security's in-cluster scanner, the one adapter
// KubeMG ships.
const ScannerTrivyOperator = "trivy-operator"

// ValidScanner reports whether a scanner is one KubeMG can read.
func ValidScanner(provider string) bool { return provider == ScannerTrivyOperator }

// ClusterVulnScanning is one cluster's vulnerability-alerts switch.
type ClusterVulnScanning struct {
	ID        uint `gorm:"primaryKey" json:"-"`
	ClusterID uint `gorm:"uniqueIndex;not null" json:"cluster_id"`

	Provider string `gorm:"size:40;not null" json:"provider"`
	// Enabled lets an administrator stop the alerts without forgetting which
	// scanner the cluster runs — the same switch a datasource has.
	Enabled bool `gorm:"not null" json:"enabled"`

	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
	LastStatus    string     `gorm:"size:20;not null;default:pending" json:"last_status"`
	LastMessage   string     `gorm:"type:text" json:"last_message,omitempty"`
	// DetectedVersion is the operator's version as its Deployment declared it at
	// the last check.
	DetectedVersion string `gorm:"size:80" json:"detected_version,omitempty"`

	// SummarizedAt is when the reports were last read and summarised — the
	// watcher's pass, or an administrator's check. A failed pass leaves it, and
	// the summaries, where the last good one put them.
	SummarizedAt *time.Time `json:"summarized_at,omitempty"`
	// SummaryMessage is the last pass's outcome in words, failed or not.
	SummaryMessage string `gorm:"type:text" json:"summary_message,omitempty"`
	// SummaryTruncated is set when the last pass stopped at the report ceiling.
	SummaryTruncated bool `gorm:"not null;default:false" json:"summary_truncated"`
	// KEVWatermark is set once a pass has run with a KEV catalogue in hand.
	// Only a pass after one fires vulnerability_kev alarms: the first pass
	// that can see KEV at all is where things stand, not a burst of news.
	KEVWatermark bool `gorm:"not null;default:false" json:"-"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the table name.
func (ClusterVulnScanning) TableName() string { return "cluster_vuln_scanning" }

// ClusterVulnScanning returns a cluster's scanning row, ErrNotFound when the
// cluster has never been connected.
func (s *Store) ClusterVulnScanning(ctx context.Context, clusterID uint) (*ClusterVulnScanning, error) {
	var row ClusterVulnScanning
	err := s.gdb.WithContext(ctx).Where("cluster_id = ?", clusterID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cluster vulnerability scanning: %w", err)
	}
	return &row, nil
}

// PutClusterVulnScanning connects a cluster, or rewrites its connection.
func (s *Store) PutClusterVulnScanning(ctx context.Context, row *ClusterVulnScanning) error {
	now := time.Now().UTC()
	row.UpdatedAt = now
	if row.CreatedAt.IsZero() {
		row.CreatedAt = now
	}
	if row.LastStatus == "" {
		row.LastStatus = StatusPending
	}
	err := s.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "cluster_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"provider", "enabled", "last_checked_at", "last_status",
			"last_message", "detected_version", "updated_at",
		}),
	}).Create(row).Error
	if err != nil {
		return fmt.Errorf("put cluster vulnerability scanning: %w", err)
	}
	return nil
}

// UpdateClusterVulnScanningHealth records the outcome of a check.
func (s *Store) UpdateClusterVulnScanningHealth(ctx context.Context, clusterID uint, health SourceHealth) error {
	res := s.gdb.WithContext(ctx).Model(&ClusterVulnScanning{}).
		Where("cluster_id = ?", clusterID).
		Updates(map[string]any{
			"last_status":      health.Status,
			"last_message":     health.Message,
			"detected_version": health.DetectedVersion,
			"last_checked_at":  health.CheckedAt,
			"updated_at":       health.CheckedAt,
		})
	if res.Error != nil {
		return fmt.Errorf("update cluster vulnerability scanning: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteClusterVulnScanning disconnects a cluster, and forgets what its
// reports said: a disconnected cluster's findings would otherwise go on being
// shown from a summary nothing refreshes.
func (s *Store) DeleteClusterVulnScanning(ctx context.Context, clusterID uint) error {
	var affected int64
	err := s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("cluster_id = ?", clusterID).Delete(&ClusterVulnScanning{})
		if res.Error != nil {
			return res.Error
		}
		affected = res.RowsAffected
		return tx.Where("cluster_id = ?", clusterID).Delete(&VulnWorkloadSummary{}).Error
	})
	if err != nil {
		return fmt.Errorf("delete cluster vulnerability scanning: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// deleteClusterVulnScanning removes a cluster's row, and its summaries, with it.
func deleteClusterVulnScanning(tx *gorm.DB, clusterID uint) error {
	if err := tx.Where("cluster_id = ?", clusterID).Delete(&ClusterVulnScanning{}).Error; err != nil {
		return fmt.Errorf("delete cluster vulnerability scanning: %w", err)
	}
	if err := tx.Where("cluster_id = ?", clusterID).Delete(&VulnWorkloadSummary{}).Error; err != nil {
		return fmt.Errorf("delete vulnerability summaries: %w", err)
	}
	// Acceptances outlive a disconnect (they are decisions, not readings) but
	// not the cluster itself.
	if err := tx.Where("cluster_id = ?", clusterID).Delete(&VulnAcceptance{}).Error; err != nil {
		return fmt.Errorf("delete vulnerability acceptances: %w", err)
	}
	return nil
}
