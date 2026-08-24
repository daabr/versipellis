// Package cron provides a simple-yet-robust scheduling mechanism
// that parses cronspec expressions to manage the execution of
// recurring goroutines. Think of it as a simplified but more
// modern version of https://github.com/robfig/cron.
package cron

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var (
	dayNames = map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}

	monthNames = map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}

	predefined = map[string]string{
		"@minutely": "* * * * *",
		"@hourly":   "0 * * * *",
		"@daily":    "0 0 * * *",
		"@midnight": "0 0 * * *",
		"@weekly":   "0 0 * * 0",
		"@monthly":  "0 0 1 * *",
		"@yearly":   "0 0 1 1 *",
		"@annually": "0 0 1 1 *",
	}

	rebootAliases = []string{"@reboot", "@startup", "@once"}
)

// Schedule represents a parsed cronspec expression, ready to start a given goroutine.
type Schedule struct {
	oneTimeLeft atomic.Bool // @reboot / @startup / @once.
	oneTime     bool        // Same, but never changes.

	interval time.Duration // @every.

	minutes     []bool // 0-59.
	hours       []bool // 0-23.
	daysOfMonth []bool // 1-31 (index 0 unused).
	months      []bool // 1-12 (index 0 unused).
	daysOfWeek  []bool // 0-6 (Sunday = 0 or 7).

	domRestricted bool // True if day of month is restricted (not "*").
	dowRestricted bool // True if day of week is restricted (not "*").

	timezone *time.Location
}

// Parse parses a cronspec expression and returns a runnable representation of it.
// If the parsing fails, the returned error specifies the reason for the failure.
// If the provided timezone [time.Location] is nil, UTC will be used.
func Parse(spec string, tz *time.Location) (*Schedule, error) {
	spec = strings.ToLower(strings.TrimSpace(spec))
	if sched, ok := predefined[spec]; ok {
		spec = sched
	}

	fields := strings.Fields(spec)
	switch {
	case len(fields) == 1 && slices.Contains(rebootAliases, fields[0]):
		s := &Schedule{oneTime: true}
		s.oneTimeLeft.Store(true)
		return s, nil
	case len(fields) > 1 && fields[0] == "@every":
		expr := strings.Join(fields[1:], "")
		d, err := time.ParseDuration(expr)
		if err != nil || d < time.Second {
			return nil, fmt.Errorf("@every with invalid duration %q", expr)
		}
		return &Schedule{interval: d.Truncate(time.Second)}, nil
	case len(fields) != 5:
		return nil, fmt.Errorf("expected 5 fields, got %d", len(fields))
	}

	s := &Schedule{
		minutes:     make([]bool, 60), // 0-59.
		hours:       make([]bool, 24), // 0-23.
		daysOfMonth: make([]bool, 32), // 1-31 (index 0 unused).
		months:      make([]bool, 13), // 1-12 (index 0 unused).
		daysOfWeek:  make([]bool, 7),  // 0-6 (Sunday = 0 or 7).
	}

	if err := parseField(fields[0], s.minutes, false, nil); err != nil {
		return nil, fmt.Errorf("minute(s): %w", err)
	}
	if err := parseField(fields[1], s.hours, false, nil); err != nil {
		return nil, fmt.Errorf("hour(s): %w", err)
	}
	if err := parseField(fields[2], s.daysOfMonth, true, nil); err != nil {
		return nil, fmt.Errorf("day(s) of month: %w", err)
	}
	if err := parseField(fields[3], s.months, true, monthNames); err != nil {
		return nil, fmt.Errorf("month(s): %w", err)
	}
	if err := parseField(fields[4], s.daysOfWeek, false, dayNames); err != nil {
		return nil, fmt.Errorf("day(s) of week: %w", err)
	}

	s.domRestricted = fields[2] != "*"
	s.dowRestricted = fields[4] != "*"

	if tz == nil {
		tz = time.UTC
	}
	s.timezone = tz

	return s, nil
}

func parseField(input string, output []bool, zeroUnused bool, names map[string]int) error {
	for part := range strings.SplitSeq(input, ",") {
		if err := parsePart(part, output, zeroUnused, names); err != nil {
			return err
		}
	}
	return nil
}

func parsePart(input string, output []bool, zeroUnused bool, names map[string]int) error {
	from, to := 0, len(output)-1 // Start with "*" as a default.
	if zeroUnused {
		from = 1
	}

	step := 1
	var err error
	before, after, hasStep := strings.Cut(input, "/")
	if hasStep {
		step, err = strconv.Atoi(after)
		if err != nil || step <= 0 || step > to {
			return fmt.Errorf("invalid step %q", after)
		}
		input = before
	}

	if input != "*" {
		before, after, isRange := strings.Cut(input, "-")
		from, err = parseValue(before, to, zeroUnused, names)
		if err != nil {
			return err
		}
		switch {
		case isRange:
			to, err = parseValue(after, to, zeroUnused, names)
			if err != nil {
				return err
			}
		case hasStep:
			return fmt.Errorf("a single value (%d) cannot have a step filter (%d)", from, step)
		default:
			to = from
		}
	}

	// Special case: allow wraparound for day-of-week (0 = Sunday = 7).
	toSunday := from > 0 && to == 0 && names != nil && names["sat"] == 6
	if from > to && !toSunday {
		tip := "use list instead of range for wrap-around, or 2 schedules with valid ranges"
		return fmt.Errorf("inverted range: %d-%d (%s)", from, to, tip)
	}
	if toSunday {
		to = 7
	}

	for i := from; i <= to; i += step {
		output[i%len(output)] = true
	}

	return nil
}

func parseValue(input string, lastIndex int, zeroUnused bool, names map[string]int) (int, error) {
	if names != nil {
		if v, ok := names[input]; ok {
			return v, nil
		}
	}

	v, err := strconv.Atoi(input)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", input)
	}

	from, to := 0, lastIndex
	if zeroUnused {
		from = 1
	}

	if from == 0 && to == 6 && v == 7 {
		v = 0 // Day of week: treat 7 as Sunday (0).
	}
	if v < from || v > to {
		return 0, fmt.Errorf("value %d out of range [%d, %d]", v, from, to)
	}

	return v, nil
}

// RunsOnlyOnce returns true if the schedule is @reboot/@startup/@once,
// regardless of whether it has already run or not.
func (s *Schedule) RunsOnlyOnce() bool {
	return s.oneTime
}

// Next returns the earliest timestamp that matches the schedule after the given time.
// This is truncated to the nearest minute, unless it's an interval-based schedule (@every).
// If no matching time is found, this function returns the zero value of [time.Time].
// The first call for a @reboot/@startup/@once schedule returns the current UTC time.
func (s *Schedule) Next(after time.Time) time.Time {
	if s.interval > 0 {
		return after.Add(s.interval).Truncate(time.Second)
	}

	if s.oneTimeLeft.Swap(false) {
		return time.Now().UTC().Truncate(time.Second)
	}
	if s.timezone == nil {
		return time.Time{} // After a single run of a @reboot/@once schedule, Next() returns the zero value.
	}

	after = after.In(s.timezone)
	next := after.Add(time.Minute).Truncate(time.Minute)
	limit := after.AddDate(8, 0, 1) // Handle rare valid schedules, but avoid infinite loops for invalid ones.

	for next.Before(limit) {
		switch {
		case !s.months[int(next.Month())]:
			t := time.Date(next.Year(), next.Month()+1, 1, 0, 0, 0, 0, next.Location())
			if !t.After(next) { // Workaround for spring-forward DST transitions at midnight.
				t = time.Date(next.Year(), next.Month()+1, 1, 1, 0, 0, 0, next.Location())
			}
			next = t
			continue
		case !s.dayMatches(next):
			t := time.Date(next.Year(), next.Month(), next.Day()+1, 0, 0, 0, 0, next.Location())
			if !t.After(next) { // Workaround for spring-forward DST transitions at midnight.
				t = time.Date(next.Year(), next.Month(), next.Day()+1, 1, 0, 0, 0, next.Location())
			}
			next = t
			continue
		case !s.hours[next.Hour()]:
			next = next.Add(time.Duration(60-next.Minute()) * time.Minute)
			continue
		case !s.minutes[next.Minute()]:
			i := 1 + slices.Index(s.minutes[next.Minute()+1:], true)
			if i == 0 {
				i = 60 - next.Minute() // No match found, roll over to the next hour.
			}
			next = next.Add(time.Duration(i) * time.Minute)
			continue
		default:
			return next
		}
	}

	return time.Time{}
}

func (s *Schedule) dayMatches(t time.Time) bool {
	dom, dow := t.Day(), int(t.Weekday())
	switch {
	case s.domRestricted && s.dowRestricted:
		// If both day fields are restricted (i.e., neither of them is "*"), match either of them.
		return s.daysOfMonth[dom] || s.daysOfWeek[dow]
	case s.domRestricted || s.dowRestricted:
		// If only one of them is restricted (i.e., only the other one is "*"), match both of them
		// (only the restricted one matters, because the unrestricted one matches every day).
		return s.daysOfMonth[dom] && s.daysOfWeek[dow]
	default:
		return true // If neither day field is restricted (i.e., both are "*"), every day is a match.
	}
}
