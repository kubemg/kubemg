package db

import (
	"context"
	"fmt"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

/*
 * What an hour of capacity costs, according to whoever runs this installation.
 *
 * KubeMG holds no cloud credential and calls no billing API — it has spent seven
 * phases arguing that a bastion needs no standing access to anything, and a
 * Cost Explorer key would be the largest standing credential in the product. So
 * the rate card is **typed in**, not discovered, and everything downstream of it
 * is an estimate that says so.
 *
 * That is a real limitation and it is the honest one. A billing integration
 * would report what was *invoiced*, which is a different number from what these
 * workloads cost: it arrives days late, it is netted against discounts and
 * commitments KubeMG cannot attribute to a Deployment, and it covers a great
 * deal that is not this cluster. What an operator can act on today is "these
 * requests, at these rates, come to this" — and that needs the rates and nothing
 * else.
 *
 * A row per cluster, plus one for the installation default. The default exists
 * because a fleet is usually one company's rates; the override exists because a
 * fleet is very often *not* one cloud, and pricing an on-prem cluster at EC2's
 * list price is worse than pricing it at nothing.
 */

// RateCardScopeDefault is the ClusterID of the installation-wide rate card.
//
// Zero is a sentinel rather than a nullable column on purpose: the lookup is
// "this cluster's card, else the default", and a unique index over a nullable
// column does not constrain the rows where it is null — two installation
// defaults would be storable, and which one applied would be whichever the
// query happened to order first.
const RateCardScopeDefault uint = 0

// Rate card providers. The provider is a **label on the numbers**, not a
// behaviour: nothing in KubeMG calls AWS differently from Azure, and the field
// exists so an operator reading a rate card months later can see which price
// list it was copied from.
const (
	RateProviderAWS    = "aws"
	RateProviderGCP    = "gcp"
	RateProviderAzure  = "azure"
	RateProviderCustom = "custom"
)

// RateProviders enumerates the labels a rate card may carry.
var RateProviders = []string{RateProviderAWS, RateProviderGCP, RateProviderAzure, RateProviderCustom}

// ValidRateProvider reports whether a provider label is one KubeMG stores.
func ValidRateProvider(provider string) bool { return slices.Contains(RateProviders, provider) }

// RateCard is one set of unit prices.
//
// The units are chosen to be the ones a cloud's own price list quotes, so
// transcribing a published figure needs no arithmetic in the operator's head —
// a step that is where a rate card acquires the factor of a thousand nobody
// notices until the report is absurd.
type RateCard struct {
	ID uint `gorm:"primaryKey" json:"id"`

	// ClusterID is the cluster these rates price, or RateCardScopeDefault for
	// the installation-wide default.
	ClusterID uint `gorm:"uniqueIndex:idx_rate_card_scope;not null" json:"cluster_id"`

	// Provider names the price list these figures were copied from. It is
	// descriptive; see the constants above.
	Provider string `gorm:"size:20;not null" json:"provider"`

	// Currency is an ISO 4217 code, stored and echoed and never converted.
	// KubeMG has no exchange rate and will not invent one: a fleet priced in two
	// currencies reports two currencies rather than a total that is neither.
	Currency string `gorm:"size:3;not null" json:"currency"`

	// CPUCoreHour is the price of one vCPU for one hour.
	CPUCoreHour float64 `gorm:"not null" json:"cpu_core_hour"`
	// MemoryGiBHour is the price of one GiB of memory for one hour.
	MemoryGiBHour float64 `gorm:"not null" json:"memory_gib_hour"`
	// StorageGiBMonth is the price of one provisioned GiB for one month. It
	// prices what a PersistentVolumeClaim asked for, which is what is billed —
	// a half-empty volume costs its whole size.
	StorageGiBMonth float64 `gorm:"not null" json:"storage_gib_month"`
	// LoadBalancerMonth is the standing monthly charge for one load balancer,
	// before any traffic. Traffic is deliberately not modelled: KubeMG cannot
	// see a byte of it, and a bandwidth estimate assembled out of nothing is
	// the kind of number that discredits the ones beside it.
	LoadBalancerMonth float64 `gorm:"not null" json:"load_balancer_month"`

	// Note is where the operator records what these rates actually are — the
	// instance family, the region, the discount they already reflect. It is
	// shown wherever the figures are, because a cost estimate whose provenance
	// is not on screen is one nobody can argue with.
	Note string `gorm:"size:500" json:"note,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the table name.
func (RateCard) TableName() string { return "rate_cards" }

// Priced reports whether this card prices anything at all. A card of zeroes is
// storable — an operator may know the compute rates and not the storage one —
// but a card where *everything* is zero prices nothing, and the reports treat
// that as "no rates configured" rather than as "this fleet is free".
func (r RateCard) Priced() bool {
	return r.CPUCoreHour > 0 || r.MemoryGiBHour > 0 ||
		r.StorageGiBMonth > 0 || r.LoadBalancerMonth > 0
}

// RateCardFor resolves the rates that apply to one cluster: its own card where
// it has one, the installation default otherwise, and nothing at all where
// neither exists.
//
// The absent case is a first-class answer rather than a zeroed card, because
// the difference matters everywhere downstream: an unpriced fleet must be told
// it is unpriced, not shown a bill for nothing.
func (s *Store) RateCardFor(ctx context.Context, clusterID uint) (*RateCard, error) {
	cards := []RateCard{}
	err := s.gdb.WithContext(ctx).
		Where("cluster_id IN ?", []uint{clusterID, RateCardScopeDefault}).
		Find(&cards).Error
	if err != nil {
		return nil, fmt.Errorf("rate card: %w", err)
	}

	var fallback *RateCard
	for i := range cards {
		if cards[i].ClusterID == clusterID {
			return &cards[i], nil
		}
		if cards[i].ClusterID == RateCardScopeDefault {
			fallback = &cards[i]
		}
	}
	return fallback, nil
}

// RateCard returns the card stored at exactly this scope, without falling back.
// The settings screens need to distinguish "this cluster is inheriting" from
// "this cluster has a card that happens to match the default".
func (s *Store) RateCard(ctx context.Context, clusterID uint) (*RateCard, error) {
	var card RateCard
	err := s.gdb.WithContext(ctx).Where("cluster_id = ?", clusterID).First(&card).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("rate card: %w", err)
	}
	return &card, nil
}

// PutRateCard stores or replaces the card at one scope.
func (s *Store) PutRateCard(ctx context.Context, card *RateCard) error {
	now := time.Now().UTC()
	card.UpdatedAt = now
	if card.CreatedAt.IsZero() {
		card.CreatedAt = now
	}

	err := s.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "cluster_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"provider", "currency", "cpu_core_hour", "memory_gib_hour",
			"storage_gib_month", "load_balancer_month", "note", "updated_at",
		}),
	}).Create(card).Error
	if err != nil {
		return fmt.Errorf("put rate card: %w", err)
	}
	return nil
}

// DeleteRateCard removes one scope's card. On a cluster that means it goes back
// to inheriting the installation default; on the default it means the fleet has
// no rates at all, which the reports say rather than work around.
func (s *Store) DeleteRateCard(ctx context.Context, clusterID uint) error {
	res := s.gdb.WithContext(ctx).Where("cluster_id = ?", clusterID).Delete(&RateCard{})
	if res.Error != nil {
		return fmt.Errorf("delete rate card: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// deleteClusterRateCard drops a cluster's override with the cluster. Rates are
// a property of the thing being priced, so they do not outlive it — and leaving
// the row behind would silently re-apply to whatever cluster next took that id.
func deleteClusterRateCard(tx *gorm.DB, clusterID uint) error {
	if err := tx.Where("cluster_id = ?", clusterID).Delete(&RateCard{}).Error; err != nil {
		return fmt.Errorf("delete cluster rate card: %w", err)
	}
	return nil
}
