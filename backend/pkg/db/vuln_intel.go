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
 * Exploit intelligence: which CVEs are being exploited, and how likely the rest
 * are to be.
 *
 * Two public lists, both optional and both off until an administrator turns
 * them on — an air-gapped installation reaches nothing it was not told to:
 *
 *   - CISA's Known Exploited Vulnerabilities catalogue (KEV): a CVE on it is
 *     being exploited in the wild. A KEV-listed finding is what earns the red
 *     on its own, whatever its severity.
 *   - FIRST's Exploit Prediction Scoring System (EPSS): the probability a CVE
 *     is exploited in the next 30 days. Used to order findings; only scores of
 *     at least EpssKeepThreshold are kept — the long tail below it orders
 *     nothing and would be most of the table.
 *
 * Each list is replaced whole by a fetch or an upload that succeeded, in one
 * transaction. A failed sync records why and keeps what was there: the Helm
 * index's rule.
 *
 * This is not a CVE database. Nothing here says what is vulnerable — the
 * scanner in the cluster does that. These say which of its findings matter
 * first.
 */

// Intelligence source kinds.
const (
	IntelKEV  = "kev"
	IntelEPSS = "epss"
)

// Default feed addresses. An administrator may point either at a mirror.
//
// KEV defaults to CISA's own GitHub publication of the catalogue rather than
// cisa.gov: the cisa.gov feed sits behind a bot filter that answered 403 to a
// server's egress in testing while a desktop got the file. The content is the
// same document, published by CISA.
const (
	DefaultKEVURL  = "https://raw.githubusercontent.com/cisagov/kev-data/main/known_exploited_vulnerabilities.json"
	DefaultEPSSURL = "https://epss.empiricalsecurity.com/epss_scores-current.csv.gz"
)

// EpssKeepThreshold is the lowest EPSS score kept.
const EpssKeepThreshold = 0.01

// ValidIntelKind reports whether kind is a source KubeMG reads.
func ValidIntelKind(kind string) bool { return kind == IntelKEV || kind == IntelEPSS }

// DefaultIntelURL is the public feed for a kind.
func DefaultIntelURL(kind string) string {
	if kind == IntelEPSS {
		return DefaultEPSSURL
	}
	return DefaultKEVURL
}

// VulnIntelSource is one list's switch and the outcome of its last refresh.
type VulnIntelSource struct {
	ID   uint   `gorm:"primaryKey" json:"-"`
	Kind string `gorm:"size:10;uniqueIndex;not null" json:"kind"`
	// Enabled turns the scheduled fetch on. An uploaded list is used whether
	// or not it is — the switch is about reaching the internet, not about
	// trusting the list.
	Enabled bool   `gorm:"not null" json:"enabled"`
	URL     string `gorm:"type:text;not null" json:"url"`

	Status   string     `gorm:"size:20;not null;default:pending" json:"status"`
	Message  string     `gorm:"type:text" json:"message,omitempty"`
	Entries  int        `gorm:"not null;default:0" json:"entries"`
	SyncedAt *time.Time `json:"synced_at,omitempty"`
	// Origin is how the current list arrived: "sync" or "upload".
	Origin string `gorm:"size:10" json:"origin,omitempty"`
	// Version is the list's own version or date, when it states one.
	Version string `gorm:"size:80" json:"version,omitempty"`

	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the table name.
func (VulnIntelSource) TableName() string { return "vuln_intel_sources" }

// VulnKEVEntry is one CVE on the KEV catalogue.
type VulnKEVEntry struct {
	CVE        string     `gorm:"primaryKey;size:40" json:"cve"`
	Vendor     string     `gorm:"size:200" json:"vendor,omitempty"`
	Product    string     `gorm:"size:200" json:"product,omitempty"`
	Name       string     `gorm:"type:text" json:"name,omitempty"`
	DateAdded  *time.Time `json:"date_added,omitempty"`
	DueDate    *time.Time `json:"due_date,omitempty"`
	Ransomware bool       `gorm:"not null;default:false" json:"ransomware"`
}

// TableName pins the table name.
func (VulnKEVEntry) TableName() string { return "vuln_kev_entries" }

// VulnEPSSEntry is one CVE's exploit-prediction score.
type VulnEPSSEntry struct {
	CVE        string  `gorm:"primaryKey;size:40" json:"cve"`
	Score      float64 `gorm:"not null" json:"score"`
	Percentile float64 `gorm:"not null" json:"percentile"`
}

// TableName pins the table name.
func (VulnEPSSEntry) TableName() string { return "vuln_epss_entries" }

// VulnIntelSources returns both sources, filling in a default for one never
// configured so the console always has two rows to draw.
func (s *Store) VulnIntelSources(ctx context.Context) ([]VulnIntelSource, error) {
	var rows []VulnIntelSource
	if err := s.gdb.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("vulnerability intelligence sources: %w", err)
	}
	out := make([]VulnIntelSource, 0, 2)
	for _, kind := range []string{IntelKEV, IntelEPSS} {
		found := false
		for _, row := range rows {
			if row.Kind == kind {
				out = append(out, row)
				found = true
			}
		}
		if !found {
			out = append(out, VulnIntelSource{Kind: kind, URL: DefaultIntelURL(kind), Status: StatusPending})
		}
	}
	return out, nil
}

// PutVulnIntelSource saves a source's switch and address.
func (s *Store) PutVulnIntelSource(ctx context.Context, kind string, enabled bool, url string) error {
	row := VulnIntelSource{Kind: kind, Enabled: enabled, URL: url, Status: StatusPending, UpdatedAt: time.Now().UTC()}
	err := s.gdb.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "kind"}},
		DoUpdates: clause.AssignmentColumns([]string{"enabled", "url", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		return fmt.Errorf("put vulnerability intelligence source: %w", err)
	}
	return nil
}

// RecordVulnIntelFailure records a failed refresh and keeps the list.
func (s *Store) RecordVulnIntelFailure(ctx context.Context, kind, message string) error {
	if err := s.ensureIntelRow(s.gdb.WithContext(ctx), kind); err != nil {
		return err
	}
	err := s.gdb.WithContext(ctx).Model(&VulnIntelSource{}).Where("kind = ?", kind).
		Updates(map[string]any{"status": StatusUnhealthy, "message": message, "updated_at": time.Now().UTC()}).Error
	if err != nil {
		return fmt.Errorf("record vulnerability intelligence failure: %w", err)
	}
	return nil
}

func (s *Store) ensureIntelRow(tx *gorm.DB, kind string) error {
	row := VulnIntelSource{Kind: kind, URL: DefaultIntelURL(kind), Status: StatusPending, UpdatedAt: time.Now().UTC()}
	return tx.Clauses(clause.OnConflict{DoNothing: true, Columns: []clause.Column{{Name: "kind"}}}).Create(&row).Error
}

// IntelOutcome describes a list that replaced the stored one.
type IntelOutcome struct {
	Origin  string
	Version string
	Message string
	At      time.Time
}

// ReplaceKEV replaces the KEV catalogue whole.
func (s *Store) ReplaceKEV(ctx context.Context, entries []VulnKEVEntry, outcome IntelOutcome) error {
	return s.replaceIntel(ctx, IntelKEV, &VulnKEVEntry{}, entries, len(entries), outcome)
}

// ReplaceEPSS replaces the EPSS scores whole.
func (s *Store) ReplaceEPSS(ctx context.Context, entries []VulnEPSSEntry, outcome IntelOutcome) error {
	return s.replaceIntel(ctx, IntelEPSS, &VulnEPSSEntry{}, entries, len(entries), outcome)
}

func (s *Store) replaceIntel(ctx context.Context, kind string, model any, entries any, count int, outcome IntelOutcome) error {
	err := s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.ensureIntelRow(tx, kind); err != nil {
			return err
		}
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(model).Error; err != nil {
			return err
		}
		if count > 0 {
			if err := tx.CreateInBatches(entries, 1000).Error; err != nil {
				return err
			}
		}
		return tx.Model(&VulnIntelSource{}).Where("kind = ?", kind).Updates(map[string]any{
			"status": StatusHealthy, "message": outcome.Message, "entries": count,
			"synced_at": outcome.At, "origin": outcome.Origin, "version": outcome.Version,
			"updated_at": outcome.At,
		}).Error
	})
	if err != nil {
		return fmt.Errorf("replace %s: %w", kind, err)
	}
	return nil
}

// KEVCatalogue returns every KEV entry keyed by CVE. Empty when no list has
// arrived — which the summariser reads as "KEV not known", not "nothing is".
func (s *Store) KEVCatalogue(ctx context.Context) (map[string]VulnKEVEntry, error) {
	var rows []VulnKEVEntry
	if err := s.gdb.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("kev catalogue: %w", err)
	}
	out := make(map[string]VulnKEVEntry, len(rows))
	for _, row := range rows {
		out[row.CVE] = row
	}
	return out, nil
}

// EPSSFor returns the kept scores for the given CVEs.
func (s *Store) EPSSFor(ctx context.Context, cves []string) (map[string]VulnEPSSEntry, error) {
	out := map[string]VulnEPSSEntry{}
	if len(cves) == 0 {
		return out, nil
	}
	var rows []VulnEPSSEntry
	if err := s.gdb.WithContext(ctx).Where("cve IN ?", cves).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("epss scores: %w", err)
	}
	for _, row := range rows {
		out[row.CVE] = row
	}
	return out, nil
}

// EnabledVulnIntel returns the sources whose scheduled fetch is on.
func (s *Store) EnabledVulnIntel(ctx context.Context) ([]VulnIntelSource, error) {
	var rows []VulnIntelSource
	err := s.gdb.WithContext(ctx).Where("enabled = ?", true).Order("kind").Find(&rows).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("enabled vulnerability intelligence: %w", err)
	}
	return rows, nil
}
