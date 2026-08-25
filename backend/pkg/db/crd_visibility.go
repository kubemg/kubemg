package db

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

/*
 * Which of a cluster's custom resources are worth putting in front of an
 * operator.
 *
 * The Explore sidebar builds its custom-resource sections from the cluster's own
 * CRD list, which is the only way to browse a cluster nobody here has heard of.
 * The cost of that is that a cluster running three operators declares a hundred
 * kinds, and most of them are one operator talking to itself — an internal
 * revision, a lock, a generated certificate request. They are reachable, and
 * nobody browses them.
 *
 * So an administrator curates the list, per cluster, and this table is what they
 * curated. Two properties follow from the shape:
 *
 *   - **Rows are the hidden set.** A CRD nobody has said anything about is
 *     shown, which is what every existing install already does; installing a new
 *     operator adds its kinds to the sidebar rather than hiding them until
 *     somebody notices. Turning a CRD back on is deleting a row, so the table
 *     never accumulates a record of everything a cluster ever served.
 *   - **It is curation, not access control.** Hiding a kind removes it from the
 *     navigation; it does not remove it from the cluster, and the object routes
 *     still address it exactly as `kubectl` would. What may be read is the
 *     cluster's own RBAC to decide, and this table must never be read as an
 *     answer to that question.
 *
 * A resource is keyed as `plural.group` — `virtualservices.networking.istio.io` —
 * which is how `kubectl` names one unambiguously and how the front end already
 * keys the CRDs it draws a first-class table for.
 */

// ClusterCRDVisibility is one custom resource hidden from one cluster's sidebar.
type ClusterCRDVisibility struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	ClusterID uint   `gorm:"uniqueIndex:idx_crd_visibility_cluster_resource;not null" json:"cluster_id"`
	Resource  string `gorm:"size:253;uniqueIndex:idx_crd_visibility_cluster_resource;not null" json:"resource"`

	// HiddenBy is the administrator who last wrote this row. The audit trail
	// carries the act; this carries it where the row is read.
	HiddenBy  uint      `json:"hidden_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the table name.
func (ClusterCRDVisibility) TableName() string { return "cluster_crd_visibility" }

// HiddenCRDs returns the custom resources hidden from one cluster's sidebar.
func (s *Store) HiddenCRDs(ctx context.Context, clusterID uint) ([]string, error) {
	rows := []ClusterCRDVisibility{}
	err := s.gdb.WithContext(ctx).
		Where("cluster_id = ?", clusterID).
		Order("resource asc").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("hidden crds: %w", err)
	}

	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Resource)
	}
	return out, nil
}

// SetHiddenCRDs replaces a cluster's hidden set with exactly what was submitted.
//
// It is a replacement rather than a pair of add/remove calls because that is the
// shape of the decision: an administrator looks at the whole list of what a
// cluster serves and says which of it is worth showing. Doing it in one
// transaction is what keeps a half-applied change from leaving a sidebar nobody
// asked for.
func (s *Store) SetHiddenCRDs(
	ctx context.Context, clusterID uint, resources []string, by uint,
) error {
	return s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("cluster_id = ?", clusterID).Delete(&ClusterCRDVisibility{}).Error
		if err != nil {
			return fmt.Errorf("clear hidden crds: %w", err)
		}
		if len(resources) == 0 {
			return nil
		}

		now := time.Now().UTC()
		rows := make([]ClusterCRDVisibility, 0, len(resources))
		for _, resource := range resources {
			rows = append(rows, ClusterCRDVisibility{
				ClusterID: clusterID,
				Resource:  resource,
				HiddenBy:  by,
				CreatedAt: now,
			})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return fmt.Errorf("set hidden crds: %w", err)
		}
		return nil
	})
}

// deleteClusterCRDVisibility drops a cluster's curation with the cluster. What
// its sidebar showed is a property of the cluster and outlives nothing.
func deleteClusterCRDVisibility(tx *gorm.DB, clusterID uint) error {
	if err := tx.Where("cluster_id = ?", clusterID).Delete(&ClusterCRDVisibility{}).Error; err != nil {
		return fmt.Errorf("delete cluster crd visibility: %w", err)
	}
	return nil
}
