package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

/*
 * What a cluster's scanner found, one row per workload.
 *
 * A Trivy report is one container's image, and carries every finding: hundreds
 * of them, kilobytes each. Reading them on every list draw would make the
 * largest objects in the cluster the most-read ones, so a watcher reads them
 * behind a lease and writes this summary — counts by severity, how many have a
 * fix, whether the workload is reachable from outside the cluster, and the level
 * that follows. The drawer reads one workload's reports live, by the names kept
 * here, when somebody opens it.
 *
 * A pass replaces a cluster's rows whole. A failed pass writes nothing, so what
 * is shown is always one complete reading, never half of two.
 */

// Vulnerability levels, from the red-fatigue rule in the API package.
const (
	VulnLevelDanger = "danger"
	VulnLevelWarn   = "warn"
	VulnLevelNone   = "none"
)

// VulnWorkloadSummary is one workload's findings, as of the last pass.
type VulnWorkloadSummary struct {
	ID        uint   `gorm:"primaryKey" json:"-"`
	ClusterID uint   `gorm:"uniqueIndex:idx_vuln_summary_key;not null" json:"-"`
	Namespace string `gorm:"size:190;uniqueIndex:idx_vuln_summary_key" json:"namespace"`
	Kind      string `gorm:"size:40;uniqueIndex:idx_vuln_summary_key;not null" json:"kind"`
	Name      string `gorm:"size:253;uniqueIndex:idx_vuln_summary_key;not null" json:"name"`

	Critical int `gorm:"not null;default:0" json:"critical"`
	High     int `gorm:"not null;default:0" json:"high"`
	Medium   int `gorm:"not null;default:0" json:"medium"`
	Low      int `gorm:"not null;default:0" json:"low"`
	Unknown  int `gorm:"not null;default:0" json:"unknown"`
	// FixableCritical and FixableHigh count findings with a fixed version — the
	// ones an upgrade closes, which is what the level is decided on.
	FixableCritical int `gorm:"not null;default:0" json:"fixable_critical"`
	FixableHigh     int `gorm:"not null;default:0" json:"fixable_high"`
	// Exposed is whether a LoadBalancer or NodePort Service, or a Service an
	// Ingress or HTTPRoute sends to, selects this workload's pods.
	Exposed bool   `gorm:"not null;default:false" json:"exposed"`
	Level   string `gorm:"size:10;not null;default:none" json:"level"`

	Images []string `gorm:"serializer:json;type:text" json:"images"`
	// Reports are the report objects summarised here, so the drawer can read
	// exactly those and nothing else.
	Reports []string `gorm:"serializer:json;type:text" json:"-"`
	// ScannedAt is the newest report's own update time.
	ScannedAt *time.Time `json:"scanned_at,omitempty"`
}

// TableName pins the table name.
func (VulnWorkloadSummary) TableName() string { return "vuln_workload_summaries" }

// VulnSummaryPass is one completed reading of a cluster's reports.
type VulnSummaryPass struct {
	Rows      []VulnWorkloadSummary
	At        time.Time
	Message   string
	Truncated bool
}

// ReplaceVulnSummaries writes a pass: the cluster's rows replaced whole, and the
// connection row told when and how it went, in one transaction. A cluster that
// is no longer connected is not written to — a disconnect racing a pass must
// not leave findings behind it.
func (s *Store) ReplaceVulnSummaries(ctx context.Context, clusterID uint, pass VulnSummaryPass) error {
	err := s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&ClusterVulnScanning{}).Where("cluster_id = ?", clusterID).Updates(map[string]any{
			"summarized_at":     pass.At,
			"summary_message":   pass.Message,
			"summary_truncated": pass.Truncated,
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		if err := tx.Where("cluster_id = ?", clusterID).Delete(&VulnWorkloadSummary{}).Error; err != nil {
			return err
		}
		if len(pass.Rows) == 0 {
			return nil
		}
		rows := make([]VulnWorkloadSummary, len(pass.Rows))
		copy(rows, pass.Rows)
		for i := range rows {
			rows[i].ID = 0
			rows[i].ClusterID = clusterID
		}
		return tx.CreateInBatches(rows, 200).Error
	})
	if errors.Is(err, ErrNotFound) {
		return err
	}
	if err != nil {
		return fmt.Errorf("replace vulnerability summaries: %w", err)
	}
	return nil
}

// RecordVulnSummaryFailure says a pass failed without touching what the last
// good one wrote.
func (s *Store) RecordVulnSummaryFailure(ctx context.Context, clusterID uint, message string) error {
	err := s.gdb.WithContext(ctx).Model(&ClusterVulnScanning{}).
		Where("cluster_id = ?", clusterID).
		Update("summary_message", message).Error
	if err != nil {
		return fmt.Errorf("record vulnerability summary failure: %w", err)
	}
	return nil
}

// VulnSummaries returns a cluster's rows, most urgent first.
func (s *Store) VulnSummaries(ctx context.Context, clusterID uint) ([]VulnWorkloadSummary, error) {
	var rows []VulnWorkloadSummary
	err := s.gdb.WithContext(ctx).Where("cluster_id = ?", clusterID).
		Order("CASE level WHEN 'danger' THEN 0 WHEN 'warn' THEN 1 ELSE 2 END").
		Order("fixable_critical DESC, critical DESC, high DESC, namespace, kind, name").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("vulnerability summaries: %w", err)
	}
	return rows, nil
}

// VulnWorkloadSummaryFor returns one workload's row, ErrNotFound when the last
// pass had nothing for it.
func (s *Store) VulnWorkloadSummaryFor(
	ctx context.Context, clusterID uint, namespace, kind, name string,
) (*VulnWorkloadSummary, error) {
	var rows []VulnWorkloadSummary
	err := s.gdb.WithContext(ctx).
		Where("cluster_id = ? AND namespace = ? AND kind = ? AND name = ?", clusterID, namespace, kind, name).
		Limit(1).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("vulnerability summary: %w", err)
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	return &rows[0], nil
}

// VulnClusterCounts is one cluster's workloads per level, for the sidebar.
type VulnClusterCounts struct {
	ClusterID uint `json:"cluster_id"`
	Danger    int  `json:"danger"`
	Warn      int  `json:"warn"`
}

// VulnCountsByCluster counts levels across every cluster whose alerts are on.
// A disabled cluster is absent, not zero: nothing is being said about it.
func (s *Store) VulnCountsByCluster(ctx context.Context) ([]VulnClusterCounts, error) {
	var out []VulnClusterCounts
	err := s.gdb.WithContext(ctx).Table("cluster_vuln_scanning AS s").
		Select(`s.cluster_id AS cluster_id,
			COALESCE(SUM(CASE WHEN v.level = 'danger' THEN 1 ELSE 0 END), 0) AS danger,
			COALESCE(SUM(CASE WHEN v.level = 'warn' THEN 1 ELSE 0 END), 0) AS warn`).
		Joins("LEFT JOIN vuln_workload_summaries AS v ON v.cluster_id = s.cluster_id").
		Where("s.enabled = ?", true).
		Group("s.cluster_id").
		Order("s.cluster_id").
		Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("vulnerability counts: %w", err)
	}
	return out, nil
}

// EnabledVulnScanning lists the clusters whose alerts are on.
func (s *Store) EnabledVulnScanning(ctx context.Context) ([]ClusterVulnScanning, error) {
	var rows []ClusterVulnScanning
	if err := s.gdb.WithContext(ctx).Where("enabled = ?", true).Order("cluster_id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("enabled vulnerability scanning: %w", err)
	}
	return rows, nil
}
