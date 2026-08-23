package cronsched

import (
	"errors"
	"testing"
	"time"
)

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("bad fixture %q: %v", value, err)
	}
	return parsed
}

func TestNextIn(t *testing.T) {
	cases := []struct {
		name     string
		spec     string
		zone     string
		after    string
		last     string
		expected string
	}{
		{"every minute", "* * * * *", "", "2026-08-23T10:15:30Z", "", "2026-08-23T10:16:00Z"},
		{"hourly on the half", "30 * * * *", "", "2026-08-23T10:31:00Z", "", "2026-08-23T11:30:00Z"},
		{"exactly now rolls forward", "16 10 * * *", "", "2026-08-23T10:16:00Z", "", "2026-08-24T10:16:00Z"},
		{"step minutes", "*/15 * * * *", "", "2026-08-23T10:16:00Z", "", "2026-08-23T10:30:00Z"},
		{"open-ended step", "5/20 * * * *", "", "2026-08-23T10:06:00Z", "", "2026-08-23T10:25:00Z"},
		{"list of hours", "0 2,14 * * *", "", "2026-08-23T10:00:00Z", "", "2026-08-23T14:00:00Z"},
		{"range of hours", "0 9-17 * * *", "", "2026-08-23T18:00:00Z", "", "2026-08-24T09:00:00Z"},
		{"daily descriptor", "@daily", "", "2026-08-23T10:00:00Z", "", "2026-08-24T00:00:00Z"},
		{"weekly descriptor", "@weekly", "", "2026-08-23T10:00:00Z", "", "2026-08-30T00:00:00Z"},
		{"monthly descriptor", "@monthly", "", "2026-08-23T10:00:00Z", "", "2026-09-01T00:00:00Z"},
		{"named weekday", "0 3 * * mon", "", "2026-08-23T10:00:00Z", "", "2026-08-24T03:00:00Z"},
		{"sunday as seven", "0 3 * * 7", "", "2026-08-23T10:00:00Z", "", "2026-08-30T03:00:00Z"},
		{"named month", "0 0 1 jan *", "", "2026-08-23T10:00:00Z", "", "2027-01-01T00:00:00Z"},
		{"leap day", "0 0 29 2 *", "", "2026-08-23T10:00:00Z", "", "2028-02-29T00:00:00Z"},
		// A schedule written in Istanbul time (UTC+3) fires at 21:00 UTC, and
		// that is the whole reason the zone is resolved here rather than in a
		// browser sitting in some other zone.
		{"zoned schedule", "0 0 * * *", "Europe/Istanbul", "2026-08-23T10:00:00Z", "", "2026-08-23T21:00:00Z"},
		{"every interval from last run", "@every 30m", "", "2026-08-23T10:05:00Z", "2026-08-23T10:00:00Z", "2026-08-23T10:30:00Z"},
		// A CronJob whose controller was down for a day must not report a
		// firing in the past.
		{"every interval catches up", "@every 1h", "", "2026-08-23T10:05:00Z", "2026-08-22T09:00:00Z", "2026-08-23T11:00:00Z"},
		{"every interval with no last run", "@every 10m", "", "2026-08-23T10:05:00Z", "", "2026-08-23T10:15:00Z"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var last time.Time
			if tc.last != "" {
				last = mustTime(t, tc.last)
			}

			next, err := NextIn(tc.spec, tc.zone, mustTime(t, tc.after), last)
			if err != nil {
				t.Fatalf("NextIn(%q): %v", tc.spec, err)
			}
			if want := mustTime(t, tc.expected); !next.Equal(want) {
				t.Fatalf("NextIn(%q) = %s, want %s", tc.spec, next.Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}
}

// TestDayDisjunction pins cron's oldest wart: with both day fields restricted a
// day matching either one fires, which is what the CronJob controller does.
func TestDayDisjunction(t *testing.T) {
	// 2026-09-01 is a Tuesday; the next Monday is the 7th.
	next, err := NextIn("0 0 1 * mon", "", mustTime(t, "2026-08-23T10:00:00Z"), time.Time{})
	if err != nil {
		t.Fatalf("NextIn: %v", err)
	}
	if want := mustTime(t, "2026-08-24T00:00:00Z"); !next.Equal(want) {
		t.Fatalf("next = %s, want %s (the Monday, not the 1st)", next, want)
	}

	// With one field unrestricted the pair is ANDed instead: the 1st only when
	// it is a Monday. 2027-02-01 is the next such day.
	next, err = NextIn("0 0 1 * *", "", mustTime(t, "2026-08-23T10:00:00Z"), time.Time{})
	if err != nil {
		t.Fatalf("NextIn: %v", err)
	}
	if want := mustTime(t, "2026-09-01T00:00:00Z"); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

// TestUnsatisfiable covers a schedule that is valid and never fires. The caller
// shows no countdown rather than an error, so this must be a zero time and not
// a five-year walk that fails.
func TestUnsatisfiable(t *testing.T) {
	next, err := NextIn("0 0 31 2 *", "", mustTime(t, "2026-08-23T10:00:00Z"), time.Time{})
	if err != nil {
		t.Fatalf("NextIn: %v", err)
	}
	if !next.IsZero() {
		t.Fatalf("next = %s, want no firing", next)
	}
}

func TestRejects(t *testing.T) {
	cases := []string{
		"",
		"* * * *",
		"0 0 * * * *",
		"@every",
		"@every nonsense",
		"@every 0s",
		"@fortnightly",
		"60 * * * *",
		"0 24 * * *",
		"0 0 0 * *",
		"0 0 * 13 *",
		"0 0 * * 8",
		"*/0 * * * *",
		"17-5 * * * *",
		"a * * * *",
	}

	for _, spec := range cases {
		t.Run(spec, func(t *testing.T) {
			if _, err := NextIn(spec, "", time.Now(), time.Time{}); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("NextIn(%q) error = %v, want ErrUnsupported", spec, err)
			}
		})
	}
}

// TestUnknownZone is its own case because a bad zone is an operator-visible
// reason on a row rather than a parse failure, and the two arrive by the same
// error path.
func TestUnknownZone(t *testing.T) {
	if _, err := NextIn("0 0 * * *", "Mars/Olympus_Mons", time.Now(), time.Time{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
}
