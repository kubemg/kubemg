package db

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// AuditFilter narrows an audit query. Zero-valued fields are not applied, so
// the empty filter is "everything, newest first".
type AuditFilter struct {
	UserID    uint
	ClusterID uint
	Verb      string
	// Verbs narrows to a set of verbs. It is separate from Verb rather than
	// replacing it because the two are asked differently: a single verb is one
	// equality test, and a set is what a badge multi-select produces. Both may be
	// supplied, and both are applied.
	Verbs     []string
	Namespace string
	// Status keeps one exact HTTP status. A 403 is the row an auditor looks for by
	// name, which FailedOnly's "anything that went wrong" does not isolate.
	Status int
	// Streaming, when set, keeps only the long-lived calls.
	Streaming bool
	// FailedOnly keeps refusals and errors — the rows an auditor looks at first.
	FailedOnly bool
	Since      *time.Time
	Until      *time.Time
	// Search matches the path, the username or the resource.
	Search string

	Limit  int
	Offset int
}

// auditPageSize bounds a page so a wide-open query cannot pull a million rows
// into memory.
const auditPageSize = 100

// AppendAuditEvents writes a batch of audit records.
func (s *Store) AppendAuditEvents(ctx context.Context, events []AuditEvent) error {
	if len(events) == 0 {
		return nil
	}
	if err := s.gdb.WithContext(ctx).CreateInBatches(events, 100).Error; err != nil {
		return fmt.Errorf("append audit events: %w", err)
	}
	return nil
}

// ListAuditEvents returns a page of audit records newest first, along with how
// many match the filter in total so the UI can page through them.
func (s *Store) ListAuditEvents(ctx context.Context, filter AuditFilter) ([]AuditEvent, int64, error) {
	query := s.gdb.WithContext(ctx).Model(&AuditEvent{})

	if filter.UserID != 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.ClusterID != 0 {
		query = query.Where("cluster_id = ?", filter.ClusterID)
	}
	if filter.Verb != "" {
		query = query.Where("verb = ?", filter.Verb)
	}
	if len(filter.Verbs) > 0 {
		query = query.Where("verb IN ?", filter.Verbs)
	}
	if filter.Status != 0 {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.Namespace != "" {
		query = query.Where("namespace = ?", filter.Namespace)
	}
	if filter.Streaming {
		query = query.Where("streaming = ?", true)
	}
	if filter.FailedOnly {
		// A refusal is either an explicit error or a non-2xx answer. A stream's
		// opening record carries 101, which is a success, not a failure.
		query = query.Where("(error <> '' AND error IS NOT NULL) OR status >= ?", 400)
	}
	if filter.Since != nil {
		query = query.Where("at >= ?", *filter.Since)
	}
	if filter.Until != nil {
		query = query.Where("at <= ?", *filter.Until)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		query = query.Where(
			"LOWER(path) LIKE ? OR LOWER(username) LIKE ? OR LOWER(resource) LIKE ? OR LOWER(namespace) LIKE ?",
			like, like, like, like,
		)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count audit events: %w", err)
	}

	limit := filter.Limit
	if limit <= 0 || limit > auditPageSize {
		limit = auditPageSize
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	events := []AuditEvent{}
	err := query.Order("at desc").Order("id desc").
		Limit(limit).Offset(offset).
		Find(&events).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list audit events: %w", err)
	}
	return events, total, nil
}

// AuditStats is the headline the audit page opens with.
type AuditStats struct {
	Total   int64 `json:"total"`
	Failed  int64 `json:"failed"`
	Streams int64 `json:"streams"`
}

// AuditSummary counts what happened in a window, so the page can lead with the
// shape of the traffic rather than with row one of a long table.
func (s *Store) AuditSummary(ctx context.Context, since time.Time) (AuditStats, error) {
	var stats AuditStats

	base := func() *gorm.DB {
		return s.gdb.WithContext(ctx).Model(&AuditEvent{}).Where("at >= ?", since)
	}

	if err := base().Count(&stats.Total).Error; err != nil {
		return stats, fmt.Errorf("count audit events: %w", err)
	}
	err := base().Where("(error <> '' AND error IS NOT NULL) OR status >= ?", 400).
		Count(&stats.Failed).Error
	if err != nil {
		return stats, fmt.Errorf("count failed audit events: %w", err)
	}
	// Count sessions, not records: a stream writes an open and a close.
	err = base().Where("streaming = ? AND phase = ?", true, "open").
		Count(&stats.Streams).Error
	if err != nil {
		return stats, fmt.Errorf("count audit streams: %w", err)
	}
	return stats, nil
}

// PruneAuditEvents drops records older than the cutoff, so an audit table on a
// busy fleet does not grow without bound.
func (s *Store) PruneAuditEvents(ctx context.Context, before time.Time) (int64, error) {
	res := s.gdb.WithContext(ctx).Where("at < ?", before).Delete(&AuditEvent{})
	if res.Error != nil {
		return 0, fmt.Errorf("prune audit events: %w", res.Error)
	}
	return res.RowsAffected, nil
}
