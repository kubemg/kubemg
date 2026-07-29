package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// terminalSessionPageSize bounds a page, like the audit trail's own.
const terminalSessionPageSize = 100

// TerminalSessionFilter narrows a recording query. Zero-valued fields are not
// applied, so the empty filter is "every recording, newest first".
type TerminalSessionFilter struct {
	UserID    uint
	ClusterID uint
	Namespace string
	Pod       string
	// SessionID finds the recording of one session. It is how a line in the
	// audit trail reaches its replay: the trail knows the correlation id, not the
	// recording's own row id.
	SessionID string
	// OpenOnly keeps sessions that have not ended yet.
	OpenOnly bool
	Since    *time.Time
	Until    *time.Time
	// Search matches the pod, the container, the username or the shell.
	Search string

	Limit  int
	Offset int
}

// TerminalSessionResult is what is known about a session once it has ended.
type TerminalSessionResult struct {
	EndedAt   time.Time
	Duration  time.Duration
	ByteCount int64
	Truncated bool
	Error     string
}

// CreateTerminalSession records a session that has just opened. It is written at
// open rather than at close for the same reason a streaming audit record is: a
// shell that runs for an hour must be visible while it is still running.
func (s *Store) CreateTerminalSession(ctx context.Context, session *TerminalSession) error {
	if err := s.gdb.WithContext(ctx).Create(session).Error; err != nil {
		return fmt.Errorf("create terminal session: %w", err)
	}
	return nil
}

// FinishTerminalSession closes out a session's row. It is addressed by the
// correlation id rather than the primary key so the recorder never has to hold
// onto a database identity.
func (s *Store) FinishTerminalSession(
	ctx context.Context, sessionID string, result TerminalSessionResult,
) error {
	endedAt := result.EndedAt
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	}
	updates := map[string]any{
		"ended_at":         endedAt,
		"duration_seconds": int64(result.Duration.Seconds()),
		"byte_count":       result.ByteCount,
		"truncated":        result.Truncated,
		"error":            result.Error,
	}
	res := s.gdb.WithContext(ctx).Model(&TerminalSession{}).
		Where("session_id = ?", sessionID).
		Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("finish terminal session: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListTerminalSessions returns a page of recordings newest first, with how many
// match the filter in total so the UI can page.
func (s *Store) ListTerminalSessions(
	ctx context.Context, filter TerminalSessionFilter,
) ([]TerminalSession, int64, error) {
	query := s.gdb.WithContext(ctx).Model(&TerminalSession{})

	if filter.UserID != 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.ClusterID != 0 {
		query = query.Where("cluster_id = ?", filter.ClusterID)
	}
	if filter.Namespace != "" {
		query = query.Where("namespace = ?", filter.Namespace)
	}
	if filter.Pod != "" {
		query = query.Where("pod_name = ?", filter.Pod)
	}
	if filter.SessionID != "" {
		query = query.Where("session_id = ?", filter.SessionID)
	}
	if filter.OpenOnly {
		query = query.Where("ended_at IS NULL")
	}
	if filter.Since != nil {
		query = query.Where("started_at >= ?", *filter.Since)
	}
	if filter.Until != nil {
		query = query.Where("started_at <= ?", *filter.Until)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		like := "%" + strings.ToLower(search) + "%"
		query = query.Where(
			"LOWER(pod_name) LIKE ? OR LOWER(container_name) LIKE ? "+
				"OR LOWER(username) LIKE ? OR LOWER(shell) LIKE ? OR LOWER(namespace) LIKE ?",
			like, like, like, like, like,
		)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count terminal sessions: %w", err)
	}

	limit := filter.Limit
	if limit <= 0 || limit > terminalSessionPageSize {
		limit = terminalSessionPageSize
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	sessions := []TerminalSession{}
	err := query.Order("started_at desc").Order("id desc").
		Limit(limit).Offset(offset).
		Find(&sessions).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list terminal sessions: %w", err)
	}
	return sessions, total, nil
}

// TerminalSessionByID loads one recording.
func (s *Store) TerminalSessionByID(ctx context.Context, id uint) (*TerminalSession, error) {
	var session TerminalSession
	err := s.gdb.WithContext(ctx).First(&session, id).Error
	switch {
	case err == nil:
		return &session, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, ErrNotFound
	default:
		return nil, fmt.Errorf("load terminal session: %w", err)
	}
}

// DeleteTerminalSession drops a recording's row. Removing the file it names is
// the caller's job, because only the HTTP layer knows the directory recordings
// are confined to.
func (s *Store) DeleteTerminalSession(ctx context.Context, id uint) error {
	res := s.gdb.WithContext(ctx).Delete(&TerminalSession{}, id)
	if res.Error != nil {
		return fmt.Errorf("delete terminal session: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// PruneTerminalSessions drops recordings that started before the cutoff and
// returns the rows it removed, so their files can be deleted too. Returning
// them is the point: a row deleted without its file leaves a recording on disk
// that nothing references and no retention policy will ever reach again.
func (s *Store) PruneTerminalSessions(
	ctx context.Context, before time.Time,
) ([]TerminalSession, error) {
	var stale []TerminalSession
	err := s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("started_at < ?", before).Find(&stale).Error; err != nil {
			return err
		}
		if len(stale) == 0 {
			return nil
		}
		ids := make([]uint, 0, len(stale))
		for _, session := range stale {
			ids = append(ids, session.ID)
		}
		return tx.Where("id IN ?", ids).Delete(&TerminalSession{}).Error
	})
	if err != nil {
		return nil, fmt.Errorf("prune terminal sessions: %w", err)
	}
	return stale, nil
}
