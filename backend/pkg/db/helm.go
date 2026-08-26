package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// helmRepositoriesSeeded marks that the starter catalogue has been written.
//
// The marker, rather than "insert if the table is empty", is what makes a
// deliberate deletion stick: an operator who removes `bitnami` because their
// site does not reach the internet must not find it back after the next restart,
// and an empty table is exactly what an operator who deleted all six leaves
// behind. Same reasoning, same mechanism, as the guardrail preset seed.
const helmRepositoriesSeeded = "helm_repositories_seeded"

// SeedHelmRepositories writes the starter catalogue once.
//
// The rows arrive with `status = pending` and no charts: seeding declares where
// charts may come from, it does not reach the network. The first sync pass does
// that, which means a first boot with no egress produces six repositories that
// each report their own reason rather than a boot that hangs on a DNS lookup.
func SeedHelmRepositories(gdb *gorm.DB) error {
	var marked int64
	if err := gdb.Model(&Setting{}).
		Where("key = ?", helmRepositoriesSeeded).
		Count(&marked).Error; err != nil {
		return fmt.Errorf("read helm repository seed marker: %w", err)
	}
	if marked > 0 {
		return nil
	}

	for _, template := range SeededHelmRepositories {
		repository := template
		repository.Seeded = true
		repository.Status = HelmRepoPending
		// A name an operator has already taken is left exactly as it is: this
		// is a seed, and overwriting a row somebody configured — credential
		// included — would be the seed editing an operator's work.
		if err := gdb.Clauses(clause.OnConflict{DoNothing: true}).
			Create(&repository).Error; err != nil {
			return fmt.Errorf("seed helm repository %q: %w", template.Name, err)
		}
	}

	if err := gdb.Save(&Setting{Key: helmRepositoriesSeeded, Value: "1"}).Error; err != nil {
		return fmt.Errorf("mark helm repository seed: %w", err)
	}
	return nil
}

/* ---------------------------------------------------------- repositories --- */

// HelmRepositories lists every declared repository, by name.
func (s *Store) HelmRepositories(ctx context.Context) ([]HelmRepository, error) {
	var repositories []HelmRepository
	if err := s.gdb.WithContext(ctx).Order("name asc").Find(&repositories).Error; err != nil {
		return nil, err
	}
	return repositories, nil
}

// HelmRepository reads one by name. The name is the address here rather than the
// id, because it is what an install names and what a release records.
func (s *Store) HelmRepository(ctx context.Context, name string) (*HelmRepository, error) {
	var repository HelmRepository
	err := s.gdb.WithContext(ctx).Where("name = ?", strings.TrimSpace(name)).First(&repository).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &repository, nil
}

// PutHelmRepository writes a repository, creating or replacing by name.
//
// The credential is in the update set, which means the caller has already
// decided whether it is a new one or the stored one carried forward. That
// decision belongs at the API layer — it is about what the *request* omitted —
// and putting it here would make the store guess at the difference between "no
// credential" and "credential unchanged".
func (s *Store) PutHelmRepository(ctx context.Context, repository *HelmRepository) error {
	return s.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"url", "username", "credential", "description",
			"status", "status_message", "updated_at",
		}),
	}).Create(repository).Error
}

// DeleteHelmRepository removes a repository and everything it contributed.
//
// The charts go with it in one transaction: a catalogue row whose repository is
// gone has no URL to fetch from and would be an install that fails at the last
// step, which is the worst place for it to fail.
func (s *Store) DeleteHelmRepository(ctx context.Context, name string) error {
	return s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var repository HelmRepository
		err := tx.Where("name = ?", strings.TrimSpace(name)).First(&repository).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := tx.Where("repository_id = ?", repository.ID).Delete(&HelmChart{}).Error; err != nil {
			return err
		}
		return tx.Delete(&repository).Error
	})
}

// UpdateHelmRepositoryHealth records what the last sync found. It writes only
// the health columns, so it can never undo an edit made while a sync was in
// flight.
func (s *Store) UpdateHelmRepositoryHealth(ctx context.Context, id uint,
	status, message string, charts int, syncedAt *time.Time,
) error {
	return s.gdb.WithContext(ctx).Model(&HelmRepository{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":         status,
			"status_message": message,
			"chart_count":    charts,
			"synced_at":      syncedAt,
		}).Error
}

/* ---------------------------------------------------------------- charts --- */

// ReplaceHelmCharts swaps a repository's catalogue for a freshly fetched one.
//
// Replace rather than merge, in one transaction, because the catalogue *is* the
// index: a chart the repository stopped publishing has to disappear, and a merge
// would leave it there for ever. The transaction is what keeps the window where
// a reader sees no charts from existing — without it, a sync would empty the
// catalogue for as long as the insert takes, and a form opened in that second
// would report the repository as holding nothing.
//
// It is deliberately never called with an empty set by a failed fetch: a sync
// that could not reach the repository does not reach here at all.
func (s *Store) ReplaceHelmCharts(ctx context.Context, repositoryID uint, charts []HelmChart) error {
	return s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("repository_id = ?", repositoryID).Delete(&HelmChart{}).Error; err != nil {
			return err
		}
		if len(charts) == 0 {
			return nil
		}
		// Batched because a large repository contributes thousands of rows and
		// one INSERT with thousands of value tuples exceeds what the driver
		// will bind in a single statement.
		return tx.CreateInBatches(&charts, 200).Error
	})
}

// HelmCharts lists a repository's catalogue, narrowed by a search term.
//
// The search is a prefix-or-contains match on the name and the description,
// bounded by `limit`, because a catalogue is browsed through a search box rather
// than scrolled: bitnami alone publishes over a hundred charts and no form shows
// them all.
func (s *Store) HelmCharts(ctx context.Context, repositoryID uint, search string, limit int) ([]HelmChart, error) {
	query := s.gdb.WithContext(ctx).Where("repository_id = ?", repositoryID)

	if term := strings.TrimSpace(search); term != "" {
		like := "%" + strings.ToLower(term) + "%"
		query = query.Where("LOWER(name) LIKE ? OR LOWER(description) LIKE ?", like, like)
	}
	if limit > 0 {
		query = query.Limit(limit)
	}

	var charts []HelmChart
	if err := query.Order("name asc").Find(&charts).Error; err != nil {
		return nil, err
	}
	return charts, nil
}

// HelmChart reads one chart of one repository.
func (s *Store) HelmChart(ctx context.Context, repositoryID uint, name string) (*HelmChart, error) {
	var chart HelmChart
	err := s.gdb.WithContext(ctx).
		Where("repository_id = ? AND name = ?", repositoryID, strings.TrimSpace(name)).
		First(&chart).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &chart, nil
}
