package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/kubemg/kubemg/backend/pkg/db"
)

// newPruneServer builds the handler struct directly. The pruner is background
// work rather than a route, so there is no request to drive it with.
func newPruneServer(store *fakeStore, defaultDays int) *server {
	return &server{store: store, auditRetentionDays: defaultDays}
}

func TestAuditPrunerKeepsOnlyTheRetentionWindow(t *testing.T) {
	store := newFakeStore()
	now := time.Now().UTC()
	store.audit = []db.AuditEvent{
		{ID: 1, At: now.AddDate(0, 0, -40)}, // outside a 30 day window
		{ID: 2, At: now.AddDate(0, 0, -31)},
		{ID: 3, At: now.AddDate(0, 0, -29)}, // inside it
		{ID: 4, At: now},
	}

	newPruneServer(store, 30).pruneAudit(context.Background())

	if len(store.audit) != 2 {
		t.Fatalf("expected the two records inside the window to survive, got %d", len(store.audit))
	}
	for _, event := range store.audit {
		if event.ID != 3 && event.ID != 4 {
			t.Fatalf("record %d is older than the window and should have been pruned", event.ID)
		}
	}
}

// The window has to be read per pass, not captured at start: shortening
// retention from the Settings page is meant to take effect without a restart.
func TestAuditPrunerFollowsTheStoredOverride(t *testing.T) {
	store := newFakeStore()
	now := time.Now().UTC()
	store.audit = []db.AuditEvent{
		{ID: 1, At: now.AddDate(0, 0, -10)},
		{ID: 2, At: now},
	}
	store.settings[db.SettingAuditRetentionDays] = "7"

	newPruneServer(store, 30).pruneAudit(context.Background())

	if len(store.audit) != 1 || store.audit[0].ID != 2 {
		t.Fatalf("the 7 day override should have dropped the 10 day old record, got %v", store.audit)
	}
}

// A stored value that is not a usable retention window must not be honoured:
// reading "0" or a typo as the window would empty the whole trail.
func TestAuditPrunerIgnoresAnUnusableOverride(t *testing.T) {
	for _, stored := range []string{"0", "-5", "not-a-number", "999999"} {
		t.Run(stored, func(t *testing.T) {
			store := newFakeStore()
			store.settings[db.SettingAuditRetentionDays] = stored
			store.audit = []db.AuditEvent{{ID: 1, At: time.Now().UTC()}}

			newPruneServer(store, 30).pruneAudit(context.Background())

			if len(store.audit) != 1 {
				t.Fatal("a fresh record must survive an unusable retention override")
			}
			if len(store.pruned) != 1 {
				t.Fatalf("expected exactly one pass, got %d", len(store.pruned))
			}
			// The cutoff has to be the default window, not "now".
			wanted := time.Now().UTC().AddDate(0, 0, -30)
			if store.pruned[0].After(wanted.Add(time.Minute)) {
				t.Fatalf("cutoff %s is more recent than the 30 day default", store.pruned[0])
			}
		})
	}
}

// A database that is briefly unhappy must not kill the goroutine; the next pass
// tries again.
func TestAuditPrunerSurvivesAStoreFailure(t *testing.T) {
	store := newFakeStore()
	store.pruneErr = errors.New("connection refused")

	newPruneServer(store, 30).pruneAudit(context.Background())
}

// startAuditPruner runs a pass immediately rather than waiting out its first
// interval, so a server that was down through a retention window catches up on
// boot.
func TestStartAuditPrunerRunsBeforeItsFirstTick(t *testing.T) {
	store := newFakeStore()
	store.audit = []db.AuditEvent{{ID: 1, At: time.Now().UTC().AddDate(0, 0, -90)}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		newPruneServer(store, 30).startAuditPruner(ctx)
	}()

	deadline := time.After(2 * time.Second)
	for len(store.audit) > 0 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("the pruner did not run a pass before its first tick")
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the pruner did not stop when its context was cancelled")
	}
}

func TestAuditRetentionIsConfigurableThroughSettings(t *testing.T) {
	env := newTestEnv(t)
	admin := env.store.addUser("admin", "secret123", "admin")
	token := env.tokenFor(t, admin)

	rec := env.do(t, http.MethodGet, "/api/v1/settings", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := decode[settingsResponse](t, rec)
	if body.Defaults.AuditRetentionDays != defaultAuditRetentionDays {
		t.Fatalf("expected a %d day default, got %d",
			defaultAuditRetentionDays, body.Defaults.AuditRetentionDays)
	}
	if body.Overrides.AuditRetentionDays != 0 {
		t.Fatalf("nothing is stored yet, so the override must read 0, got %d",
			body.Overrides.AuditRetentionDays)
	}

	rec = env.do(t, http.MethodPut, "/api/v1/settings", token,
		map[string]any{"audit_retention_days": 7})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	body = decode[settingsResponse](t, rec)
	if body.Effective.AuditRetentionDays != 7 || body.Overrides.AuditRetentionDays != 7 {
		t.Fatalf("expected the 7 day override to take effect, got %+v", body)
	}

	// Zero clears it back to the default rather than meaning "keep nothing".
	rec = env.do(t, http.MethodPut, "/api/v1/settings", token,
		map[string]any{"audit_retention_days": 0})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	body = decode[settingsResponse](t, rec)
	if body.Effective.AuditRetentionDays != defaultAuditRetentionDays {
		t.Fatalf("clearing the override should restore the default, got %d",
			body.Effective.AuditRetentionDays)
	}

	for _, days := range []int{-1, maxAuditRetentionDays + 1} {
		rec = env.do(t, http.MethodPut, "/api/v1/settings", token,
			map[string]any{"audit_retention_days": days})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected %d days to be refused, got %d (%s)", days, rec.Code, rec.Body.String())
		}
	}
}

// A recording is two orders of magnitude larger than the audit row describing
// it, so the useful direction for its window is shorter than the trail's.
func TestRecordingPrunerHonoursItsOwnShorterWindow(t *testing.T) {
	store := newFakeStore()
	now := time.Now().UTC()
	store.settings[db.SettingAuditRetentionDays] = "90"
	store.settings[db.SettingSessionRecordingRetentionDays] = "7"

	store.audit = []db.AuditEvent{{ID: 1, At: now.AddDate(0, 0, -30)}}
	store.addTerminalSession(db.TerminalSession{StartedAt: now.AddDate(0, 0, -30)})
	store.addTerminalSession(db.TerminalSession{StartedAt: now.AddDate(0, 0, -1)})

	newPruneServer(store, 30).pruneAudit(context.Background())

	// The trail keeps its 30-day-old row; the recording of the same age goes.
	if len(store.audit) != 1 {
		t.Fatalf("the 90 day audit window should have kept the record, got %d", len(store.audit))
	}
	if len(store.recordings) != 1 {
		t.Fatalf("the 7 day recording window should have kept one, got %d", len(store.recordings))
	}
}

// A recording must never outlive the trail saying the shell was opened, so a
// window longer than the audit one is clamped rather than honoured.
func TestRecordingPrunerIsCappedByTheAuditWindow(t *testing.T) {
	store := newFakeStore()
	now := time.Now().UTC()
	store.settings[db.SettingAuditRetentionDays] = "7"
	store.settings[db.SettingSessionRecordingRetentionDays] = "365"

	store.addTerminalSession(db.TerminalSession{StartedAt: now.AddDate(0, 0, -30)})
	store.addTerminalSession(db.TerminalSession{StartedAt: now})

	newPruneServer(store, 30).pruneAudit(context.Background())

	if len(store.recordings) != 1 {
		t.Fatalf("the recording window should have been clamped to 7 days, got %d",
			len(store.recordings))
	}
}
