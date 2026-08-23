// Package cronsched answers one question about a CronJob that Kubernetes does
// not: when does it run next.
//
// The API server stores `spec.schedule` and reports `status.lastScheduleTime`,
// and nothing in between — the next firing time exists only inside the
// kube-controller-manager's own loop, so a console that wants to show a
// countdown has to evaluate the schedule itself. Doing that in the browser was
// rejected for the reason every other Kubernetes shape is normalised server
// side: the timezone a schedule is written in is `spec.timeZone`, resolving it
// needs a zoneinfo database, and a browser evaluating cron in the reader's own
// local time would count down to the wrong minute for every cluster that is not
// in the reader's own zone.
//
// This is a deliberate reimplementation rather than a dependency. It is the
// five-field standard cron the upstream CronJob controller accepts, which is a
// bounded grammar, and an air-gapped install mirrors this binary by hand — a
// module added here is a supply-chain artefact for ~150 lines of field matching.
// The matching rules below mirror the controller's own (robfig/cron) exactly,
// including the day-of-month/day-of-week disjunction, because a countdown that
// disagrees with the controller is worse than no countdown.
package cronsched

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	// The runtime image is distroless/static, which carries no
	// /usr/share/zoneinfo — without this, every `spec.timeZone` on a deployed
	// install would resolve to "unknown time zone" while working fine in a
	// developer's container. The cost is ~450 kB in a binary that already
	// embeds the console.
	_ "time/tzdata"
)

// ErrUnsupported is returned for a schedule this build cannot evaluate. It is a
// distinct error because the caller reports it to an operator rather than
// failing: a schedule KubeMG cannot read is a missing countdown on one row, not
// a failed list.
var ErrUnsupported = errors.New("unsupported schedule")

// yearLimit bounds the search. A schedule can be satisfiable so rarely that
// finding it is a scan (`0 0 31 2 *` never fires at all), and the caller wants
// an answer in microseconds, not a walk to the end of time. Five years is well
// past any interval a countdown is useful for.
const yearLimit = 5

// Schedule is a parsed cron expression: one bitmask per field, plus the fixed
// interval form (`@every 5m`), which has no fields at all.
type Schedule struct {
	minute, hour, dom, month, dow uint64

	// domStar/dowStar record whether the field was written as `*` or `?`. The
	// two day fields are ANDed when either is unrestricted and ORed when both
	// are restricted, which is cron's oldest wart and the one thing a
	// hand-rolled matcher gets wrong.
	domStar, dowStar bool

	// every is set for `@every <duration>`, whose next firing is measured from
	// the last one rather than from a calendar.
	every time.Duration
}

var descriptors = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

var monthNames = map[string]uint{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dayNames = map[string]uint{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

type bounds struct {
	min, max uint
	names    map[string]uint
}

var (
	minutes = bounds{0, 59, nil}
	hours   = bounds{0, 23, nil}
	doms    = bounds{1, 31, nil}
	months  = bounds{1, 12, monthNames}
	dows    = bounds{0, 6, dayNames}
)

// Parse reads a CronJob's `spec.schedule`.
func Parse(spec string) (Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Schedule{}, fmt.Errorf("%w: empty schedule", ErrUnsupported)
	}

	if strings.HasPrefix(spec, "@") {
		lower := strings.ToLower(spec)
		if expanded, ok := descriptors[lower]; ok {
			spec = expanded
		} else if rest, ok := strings.CutPrefix(lower, "@every "); ok {
			every, err := time.ParseDuration(strings.TrimSpace(rest))
			if err != nil || every <= 0 {
				return Schedule{}, fmt.Errorf("%w: %q is not a duration", ErrUnsupported, spec)
			}
			return Schedule{every: every}, nil
		} else {
			return Schedule{}, fmt.Errorf("%w: unknown descriptor %q", ErrUnsupported, spec)
		}
	}

	fields := strings.Fields(spec)
	// Six fields is a seconds-precision schedule, which the CronJob controller
	// itself refuses — naming that is more useful than "wrong number of fields".
	if len(fields) == 6 {
		return Schedule{}, fmt.Errorf("%w: six-field schedules are not accepted by Kubernetes", ErrUnsupported)
	}
	if len(fields) != 5 {
		return Schedule{}, fmt.Errorf("%w: want 5 fields, got %d", ErrUnsupported, len(fields))
	}

	var (
		out Schedule
		err error
	)
	if out.minute, _, err = parseField(fields[0], minutes); err != nil {
		return Schedule{}, err
	}
	if out.hour, _, err = parseField(fields[1], hours); err != nil {
		return Schedule{}, err
	}
	if out.dom, out.domStar, err = parseField(fields[2], doms); err != nil {
		return Schedule{}, err
	}
	if out.month, _, err = parseField(fields[3], months); err != nil {
		return Schedule{}, err
	}
	if out.dow, out.dowStar, err = parseField(fields[4], dows); err != nil {
		return Schedule{}, err
	}
	return out, nil
}

// parseField reads one comma-separated field, reporting whether it was written
// as an unrestricted `*`/`?`.
func parseField(field string, b bounds) (uint64, bool, error) {
	var (
		bits uint64
		star bool
	)
	for _, term := range strings.Split(field, ",") {
		termBits, termStar, err := parseTerm(term, b)
		if err != nil {
			return 0, false, err
		}
		bits |= termBits
		star = star || termStar
	}
	if bits == 0 {
		return 0, false, fmt.Errorf("%w: %q matches nothing", ErrUnsupported, field)
	}
	return bits, star, nil
}

func parseTerm(term string, b bounds) (uint64, bool, error) {
	term = strings.TrimSpace(term)
	if term == "" {
		return 0, false, fmt.Errorf("%w: empty term", ErrUnsupported)
	}

	step := uint(1)
	if base, stepText, ok := strings.Cut(term, "/"); ok {
		parsed, err := strconv.ParseUint(strings.TrimSpace(stepText), 10, 8)
		if err != nil || parsed == 0 {
			return 0, false, fmt.Errorf("%w: bad step in %q", ErrUnsupported, term)
		}
		step = uint(parsed)
		term = strings.TrimSpace(base)
		// `5/10` means "from 5 to the end of the range, every 10" — the same as
		// `5-max/10`, not a single value.
		if term != "*" && term != "?" && !strings.Contains(term, "-") {
			single, err := parseValue(term, b)
			if err != nil {
				return 0, false, err
			}
			return rangeBits(single, b.max, step), false, nil
		}
	}

	if term == "*" || term == "?" {
		return rangeBits(b.min, b.max, step), true, nil
	}

	if lowText, highText, ok := strings.Cut(term, "-"); ok {
		low, err := parseValue(lowText, b)
		if err != nil {
			return 0, false, err
		}
		high, err := parseValue(highText, b)
		if err != nil {
			return 0, false, err
		}
		if low > high {
			return 0, false, fmt.Errorf("%w: %q is an inverted range", ErrUnsupported, term)
		}
		return rangeBits(low, high, step), false, nil
	}

	value, err := parseValue(term, b)
	if err != nil {
		return 0, false, err
	}
	return 1 << value, false, nil
}

func parseValue(text string, b bounds) (uint, error) {
	text = strings.ToLower(strings.TrimSpace(text))
	if b.names != nil {
		if named, ok := b.names[text]; ok {
			return named, nil
		}
	}
	value, err := strconv.ParseUint(text, 10, 8)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is not a value", ErrUnsupported, text)
	}
	// Sunday is both 0 and 7 in every cron implementation, upstream's included.
	if b.names != nil && b.max == 6 && value == 7 {
		return 0, nil
	}
	if uint(value) < b.min || uint(value) > b.max {
		return 0, fmt.Errorf("%w: %q is out of range %d-%d", ErrUnsupported, text, b.min, b.max)
	}
	return uint(value), nil
}

func rangeBits(low, high, step uint) uint64 {
	var bits uint64
	for value := low; value <= high; value += step {
		bits |= 1 << value
	}
	return bits
}

// Next returns the first firing at or after `after`, exclusive of `after`
// itself, in the schedule's own location. The zero time means the schedule has
// no firing within yearLimit years — `0 0 31 2 *` is a valid expression that
// never runs, and reporting nothing is more honest than reporting a guess.
//
// last is the previous firing, and matters only for `@every`, whose interval is
// measured from it; a zero last falls back to `after`.
func (s Schedule) Next(after time.Time, last time.Time) time.Time {
	if s.every > 0 {
		base := last
		if base.IsZero() {
			base = after
		}
		next := base.Add(s.every)
		// A controller that was down, or a CronJob whose last run predates
		// several intervals, must not report a firing in the past.
		if !next.After(after) {
			missed := after.Sub(base) / s.every
			next = base.Add((missed + 1) * s.every)
		}
		return next
	}

	// Seconds are not part of the grammar, so the search starts at the top of
	// the next minute.
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := t.Year() + yearLimit

	for {
		if t.Year() > limit {
			return time.Time{}
		}

		if s.month&(1<<uint(t.Month())) == 0 {
			t = t.AddDate(0, 1, 0)
			t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !s.matchDay(t) {
			t = t.AddDate(0, 0, 1)
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
			continue
		}
		if s.hour&(1<<uint(t.Hour())) == 0 {
			t = t.Add(time.Hour)
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
			continue
		}
		if s.minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
}

// matchDay applies cron's day disjunction: when one of the two day fields is
// unrestricted the pair is ANDed, and when both are restricted a day matching
// either one fires. `0 0 1 * 1` really does mean "the 1st and every Monday".
func (s Schedule) matchDay(t time.Time) bool {
	domHit := s.dom&(1<<uint(t.Day())) != 0
	dowHit := s.dow&(1<<uint(t.Weekday())) != 0
	if s.domStar || s.dowStar {
		return domHit && dowHit
	}
	return domHit || dowHit
}

// NextIn resolves the schedule and its `spec.timeZone` together, which is the
// call every caller here actually wants. An empty zone means the controller's
// own — reported by the caller rather than assumed here, because that is a
// property of the install and not of the expression.
func NextIn(spec, timeZone string, after, last time.Time) (time.Time, error) {
	schedule, err := Parse(spec)
	if err != nil {
		return time.Time{}, err
	}

	location := time.UTC
	if timeZone != "" {
		loaded, err := time.LoadLocation(timeZone)
		if err != nil {
			return time.Time{}, fmt.Errorf("%w: unknown time zone %q", ErrUnsupported, timeZone)
		}
		location = loaded
	}

	next := schedule.Next(after.In(location), last.In(location))
	if next.IsZero() {
		return time.Time{}, nil
	}
	return next.UTC(), nil
}
