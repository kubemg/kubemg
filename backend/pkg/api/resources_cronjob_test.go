package api

import (
	"strings"
	"testing"
	"time"
)

// The schedule arithmetic itself is pinned in pkg/cronsched. What is pinned here
// is the shape the list reports it in, which is where a wrong answer would be
// read as a fact about the cluster: a suspended CronJob has no next run, an
// unreadable schedule costs one row rather than the list, and a schedule with no
// firing left is silence rather than an error.
func TestCronJobNext(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 15, 30, 0, time.UTC)
	lastRun := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

	t.Run("derives the next firing", func(t *testing.T) {
		next, reason := cronJobNext("*/30 * * * *", "", false, &lastRun, now)
		if reason != "" {
			t.Fatalf("reason = %q, want none", reason)
		}
		if next == nil || !next.Equal(time.Date(2026, 8, 23, 10, 30, 0, 0, time.UTC)) {
			t.Fatalf("next = %v, want 10:30Z", next)
		}
	})

	t.Run("resolves the schedule's own time zone", func(t *testing.T) {
		// Midnight in Istanbul is 21:00Z the day before, and a reader in any
		// other zone would count down to the wrong minute without this.
		next, reason := cronJobNext("0 0 * * *", "Europe/Istanbul", false, nil, now)
		if reason != "" {
			t.Fatalf("reason = %q, want none", reason)
		}
		if next == nil || !next.Equal(time.Date(2026, 8, 23, 21, 0, 0, 0, time.UTC)) {
			t.Fatalf("next = %v, want 21:00Z", next)
		}
	})

	t.Run("suspended has no next run", func(t *testing.T) {
		next, reason := cronJobNext("*/5 * * * *", "", true, &lastRun, now)
		if next != nil || reason != "" {
			t.Fatalf("next = %v, reason = %q, want neither", next, reason)
		}
	})

	t.Run("an unreadable schedule is a reason, not a failure", func(t *testing.T) {
		next, reason := cronJobNext("every tuesday", "", false, nil, now)
		if next != nil {
			t.Fatalf("next = %v, want none", next)
		}
		if reason == "" {
			t.Fatal("expected a reason to show on the row")
		}
		// The reason is read by an operator on a row, so it must not arrive
		// wrapped in the package's own error prefix.
		if strings.HasPrefix(reason, "unsupported schedule") {
			t.Fatalf("reason = %q, want the cause without the error prefix", reason)
		}
	})

	t.Run("an unknown time zone is a reason", func(t *testing.T) {
		next, reason := cronJobNext("0 0 * * *", "Mars/Olympus_Mons", false, nil, now)
		if next != nil || reason == "" {
			t.Fatalf("next = %v, reason = %q", next, reason)
		}
	})

	t.Run("a schedule that never fires again is silence", func(t *testing.T) {
		next, reason := cronJobNext("0 0 31 2 *", "", false, nil, now)
		if next != nil || reason != "" {
			t.Fatalf("next = %v, reason = %q, want neither", next, reason)
		}
	})
}
