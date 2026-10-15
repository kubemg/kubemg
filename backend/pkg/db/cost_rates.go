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
 * What a cluster's capacity costs, as its operator prices it.
 *
 * The waste report states what can be given back in cores and bytes. Turning
 * that into money needs a price, and **the price is an operator's, never a
 * vendor's list**: a list price differs by region, commitment and discount, goes
 * stale without notice, and is unreachable from an air-gapped install. So what
 * is stored is a rate somebody typed, and the page says "at your rates".
 *
 * One row per cluster, not a fleet-wide rate: a cluster in one cloud region and
 * one on rented hardware do not cost the same, and a single rate would be
 * silently wrong for half the fleet.
 *
 * CPU and memory are priced together — a compute price is both, and a figure
 * that priced one and not the other would read as the whole. Storage and load
 * balancers are optional: absent means "not priced", and zero means "costs
 * nothing", which is a real answer for an on-premises load balancer.
 */

// ClusterCostRates is one cluster's unit prices. The columns are pinned: GORM
// would name MemoryGiBHour `memory_gi_b_hour`, and the upsert names them.
type ClusterCostRates struct {
	ID        uint `gorm:"primaryKey" json:"-"`
	ClusterID uint `gorm:"uniqueIndex;not null" json:"-"`

	// Currency is an ISO 4217 code; KubeMG converts nothing.
	Currency string `gorm:"size:3;not null" json:"currency"`

	// CPUCoreHour is the price of one core reserved for an hour.
	CPUCoreHour float64 `gorm:"column:cpu_core_hour;not null" json:"cpu_core_hour"`
	// MemoryGiBHour is the price of one GiB reserved for an hour.
	MemoryGiBHour float64 `gorm:"column:memory_gib_hour;not null" json:"memory_gib_hour"`
	// StorageGiBMonth is the price of one GiB of provisioned volume for a month.
	StorageGiBMonth *float64 `gorm:"column:storage_gib_month" json:"storage_gib_month,omitempty"`
	// LoadBalancerHour is the price of one load balancer for an hour.
	LoadBalancerHour *float64 `gorm:"column:load_balancer_hour" json:"load_balancer_hour,omitempty"`

	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the table name.
func (ClusterCostRates) TableName() string { return "cluster_cost_rates" }

// ClusterCostRates returns a cluster's rates, ErrNotFound when none are set.
func (s *Store) ClusterCostRates(ctx context.Context, clusterID uint) (*ClusterCostRates, error) {
	var rates ClusterCostRates
	err := s.gdb.WithContext(ctx).Where("cluster_id = ?", clusterID).First(&rates).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cluster cost rates: %w", err)
	}
	return &rates, nil
}

// PutClusterCostRates sets or replaces a cluster's rates.
func (s *Store) PutClusterCostRates(ctx context.Context, rates *ClusterCostRates) error {
	now := time.Now().UTC()
	rates.UpdatedAt = now
	if rates.CreatedAt.IsZero() {
		rates.CreatedAt = now
	}

	err := s.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "cluster_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"currency", "cpu_core_hour", "memory_gib_hour",
			"storage_gib_month", "load_balancer_hour", "updated_at",
		}),
	}).Create(rates).Error
	if err != nil {
		return fmt.Errorf("put cluster cost rates: %w", err)
	}
	return nil
}

// DeleteClusterCostRates removes a cluster's rates.
func (s *Store) DeleteClusterCostRates(ctx context.Context, clusterID uint) error {
	res := s.gdb.WithContext(ctx).Where("cluster_id = ?", clusterID).Delete(&ClusterCostRates{})
	if res.Error != nil {
		return fmt.Errorf("delete cluster cost rates: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// deleteClusterCostRates removes a cluster's rates with it.
func deleteClusterCostRates(tx *gorm.DB, clusterID uint) error {
	if err := tx.Where("cluster_id = ?", clusterID).Delete(&ClusterCostRates{}).Error; err != nil {
		return fmt.Errorf("delete cluster cost rates: %w", err)
	}
	return nil
}
