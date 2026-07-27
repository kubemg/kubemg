package api

import (
	"context"
	"log/slog"
	"time"
)

// auditPruneInterval is how often the retention policy is applied. Twice a day
// is plenty: retention is measured in days, so a window that is a few hours
// stale is indistinguishable from one that is exact, and a tighter loop would
// only add DELETE pressure to a table the proxy is writing to constantly.
const auditPruneInterval = 12 * time.Hour

// defaultAuditRetentionDays is the fallback when neither the environment nor an
// operator has said otherwise.
const defaultAuditRetentionDays = 30

// startAuditPruner applies the audit retention policy on a schedule until the
// context is cancelled. It runs once immediately, because a server that has
// been down for a week should not wait another twelve hours before honouring a
// retention policy it was already meant to be enforcing.
//
// The window is re-read from the settings every pass rather than captured at
// start: shortening retention from the Settings page has to take effect without
// a restart, which is the whole point of the setting being runtime-configurable.
func (s *server) startAuditPruner(ctx context.Context) {
	ticker := time.NewTicker(auditPruneInterval)
	defer ticker.Stop()

	for {
		s.pruneAudit(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// pruneAudit deletes everything older than the configured window. A failure is
// logged and left for the next pass: an audit table that is temporarily too big
// is a far smaller problem than a background goroutine that gives up on the
// first transient database error.
func (s *server) pruneAudit(ctx context.Context) {
	days := s.settings(ctx).AuditRetentionDays
	if days < minAuditRetentionDays {
		days = defaultAuditRetentionDays
	}

	before := time.Now().UTC().AddDate(0, 0, -days)
	removed, err := s.store.PruneAuditEvents(ctx, before)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.log().Warn("audit retention pass failed",
			slog.String("error", err.Error()),
			slog.Int("retention_days", days))
		return
	}
	if removed > 0 {
		s.log().Info("pruned audit events past their retention window",
			slog.Int64("removed", removed),
			slog.Int("retention_days", days),
			slog.Time("before", before))
	}
}

// log returns the server's logger, falling back to the default so nothing has
// to nil-check it.
func (s *server) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}
