package db

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm/clause"
)

/*
 * Accepted risk: one CVE, on one workload, that somebody reviewed and stood
 * behind rather than fixed — posture acknowledgement's shape, keyed on a CVE
 * instead of a rule.
 *
 * It does not hide the finding. The drawer still lists it, marked accepted
 * with who and why; it only stops the finding counting towards the workload's
 * colour, its KEV count and its alarms. An acceptance may carry an expiry, after
 * which the finding counts again without anybody having to remember it.
 */

// VulnAcceptance is one accepted finding.
type VulnAcceptance struct {
	ID uint `gorm:"primaryKey" json:"id"`

	ClusterID uint   `gorm:"uniqueIndex:idx_vuln_accept_key;not null" json:"cluster_id"`
	Namespace string `gorm:"size:190;uniqueIndex:idx_vuln_accept_key;not null" json:"namespace"`
	Kind      string `gorm:"size:40;uniqueIndex:idx_vuln_accept_key;not null" json:"kind"`
	Name      string `gorm:"size:190;uniqueIndex:idx_vuln_accept_key;not null" json:"name"`
	CVE       string `gorm:"size:60;uniqueIndex:idx_vuln_accept_key;not null" json:"cve"`

	Reason     string     `gorm:"type:text;not null" json:"reason"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	AcceptedBy string     `gorm:"size:120" json:"accepted_by"`
	AcceptedID uint       `gorm:"index" json:"accepted_by_id"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the table name.
func (VulnAcceptance) TableName() string { return "vuln_acceptances" }

// Live reports whether the acceptance still holds at now.
func (a VulnAcceptance) Live(now time.Time) bool { return a.ExpiresAt == nil || a.ExpiresAt.After(now) }

// PutVulnAcceptance records or replaces an acceptance.
func (s *Store) PutVulnAcceptance(ctx context.Context, row *VulnAcceptance) error {
	err := s.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "cluster_id"}, {Name: "namespace"}, {Name: "kind"}, {Name: "name"}, {Name: "cve"}},
		DoUpdates: clause.AssignmentColumns([]string{"reason", "expires_at", "accepted_by", "accepted_id", "updated_at"}),
	}).Create(row).Error
	if err != nil {
		return fmt.Errorf("put vulnerability acceptance: %w", err)
	}
	return nil
}

// DeleteVulnAcceptance withdraws one acceptance.
func (s *Store) DeleteVulnAcceptance(ctx context.Context, clusterID uint, namespace, kind, name, cve string) error {
	res := s.gdb.WithContext(ctx).
		Where("cluster_id = ? AND namespace = ? AND kind = ? AND name = ? AND cve = ?", clusterID, namespace, kind, name, cve).
		Delete(&VulnAcceptance{})
	if res.Error != nil {
		return fmt.Errorf("delete vulnerability acceptance: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// VulnAcceptances returns a cluster's acceptances, expired ones included —
// the caller decides what still holds.
func (s *Store) VulnAcceptances(ctx context.Context, clusterID uint) ([]VulnAcceptance, error) {
	var rows []VulnAcceptance
	if err := s.gdb.WithContext(ctx).Where("cluster_id = ?", clusterID).Order("namespace, kind, name, cve").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("vulnerability acceptances: %w", err)
	}
	return rows, nil
}
