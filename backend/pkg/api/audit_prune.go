package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/terminal"
)

// auditPruneInterval is how often the retention policy is applied. Twice a day
// is plenty: retention is measured in days, so a window that is a few hours
// stale is indistinguishable from one that is exact, and a tighter loop would
// only add DELETE pressure to a table the proxy is writing to constantly.
const auditPruneInterval = 12 * time.Hour

// defaultAuditRetentionDays is the fallback when neither the environment nor an
// operator has said otherwise.
const defaultAuditRetentionDays = 30

// auditPolicyRefreshInterval is how often the audit settings are re-read into the
// in-memory policy the gateway consults. A save republishes immediately, so this
// tick exists for the other case: a second replica, whose own memory knows
// nothing about a change saved through its sibling.
const auditPolicyRefreshInterval = 30 * time.Second

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

// startAuditPolicyRefresher keeps the in-memory audit policy in step with the
// stored settings. It publishes once immediately so a restart honours the
// selection from its first proxied call rather than recording everything until
// the first tick.
func (s *server) startAuditPolicyRefresher(ctx context.Context) {
	ticker := time.NewTicker(auditPolicyRefreshInterval)
	defer ticker.Stop()

	for {
		s.publishAuditPolicy(ctx)

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
	resolved := s.settings(ctx)
	days := resolved.AuditRetentionDays
	if days < minAuditRetentionDays {
		days = defaultAuditRetentionDays
	}

	now := time.Now().UTC()

	// Recordings have a window of their own, defaulting to the audit window and
	// never exceeding it. A shorter one is the useful direction: a recording is
	// two orders of magnitude larger than the audit row describing it, so a fleet
	// that wants a year of trail rarely wants a year of shell transcripts.
	recordingDays := clampRecordingRetention(resolved.SessionRecordingRetentionDays, days)
	if recordingDays < minAuditRetentionDays {
		recordingDays = days
	}
	s.pruneRecordings(ctx, now.AddDate(0, 0, -recordingDays))

	before := now.AddDate(0, 0, -days)

	// Decided access requests share the audit window rather than having one of
	// their own: a request is the record of who was given production and why, which
	// is precisely the class of thing that setting is about. Anything still live is
	// left alone whatever its age — a window nobody has closed is not history — and
	// a failure here must not stop the audit pass below.
	s.pruneJitRequests(ctx, before)

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

// pruneJitRequests drops decided access requests past the audit window. The audit
// records of the decisions outlive them by exactly as long as the trail does; what
// goes is the workflow row, which nothing reads back once it is closed.
func (s *server) pruneJitRequests(ctx context.Context, before time.Time) {
	if s.jit == nil {
		return
	}
	removed, err := s.store.PruneJitRequests(ctx, before)
	if err != nil {
		if ctx.Err() == nil {
			s.log().Warn("access request retention pass failed", slog.String("error", err.Error()))
		}
		return
	}
	if removed > 0 {
		s.log().Info("pruned decided access requests past their retention window",
			slog.Int64("removed", removed),
			slog.Time("before", before))
	}
}

// pruneRecordings drops session recordings past their window, files included.
// The window defaults to the audit one and is capped by it, because a recording
// *is* audit evidence: keeping the replay of a shell after the trail saying it
// was opened has been deleted would be the wrong way round.
//
// Rows are deleted with their files in one pass, and only a row whose file is
// gone or unreachable is left behind — the alternative is a directory of
// recordings nothing references, which no later pass can find.
func (s *server) pruneRecordings(ctx context.Context, before time.Time) {
	stale, err := s.store.PruneTerminalSessions(ctx, before)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.log().Warn("session recording retention pass failed",
			slog.String("error", err.Error()))
		return
	}
	if len(stale) == 0 {
		return
	}

	failed := 0
	if s.recordings != "" {
		for _, session := range stale {
			if err := terminal.Remove(s.recordings, session.StoragePath); err != nil {
				failed++
			}
		}
	}
	s.log().Info("pruned session recordings past their retention window",
		slog.Int("removed", len(stale)),
		slog.Int("files_left_behind", failed),
		slog.Time("before", before))
}

// log returns the server's logger, falling back to the default so nothing has
// to nil-check it.
func (s *server) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}
