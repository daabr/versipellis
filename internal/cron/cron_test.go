package cron_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/daabr/versipellis/internal/cron"
)

func TestParseAndNext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		spec  string
		after string
		want  string
	}{
		{
			name:  "every_minute",
			spec:  "* * * * *",
			after: "2026-01-01 00:00:00",
			want:  "2026-01-01 00:01:00",
		},
		{
			name:  "every_minute_truncated",
			spec:  "* * * * *",
			after: "2026-01-01 00:00:59",
			want:  "2026-01-01 00:01:00",
		},
		{
			name:  "every_hour",
			spec:  "5 * * * *",
			after: "2026-01-01 00:00:00",
			want:  "2026-01-01 00:05:00",
		},
		{
			name:  "every_hour_after_cutoff",
			spec:  "5 * * * *",
			after: "2026-01-01 00:05:00",
			want:  "2026-01-01 01:05:00",
		},
		{
			name:  "every_day_at_noon",
			spec:  "0 12 * * *",
			after: "2026-01-01 00:00:00",
			want:  "2026-01-01 12:00:00",
		},
		{
			name:  "midnight_on_jan_1",
			spec:  "0 0 1 1 *",
			after: "2026-06-15 00:00:00",
			want:  "2027-01-01 00:00:00",
		},

		// Lists.

		{
			name:  "0430_on_1st_and_15th",
			spec:  "30 4 1,15 * *",
			after: "2026-01-01 00:00:00",
			want:  "2026-01-01 04:30:00",
		},
		{
			name:  "0430_on_1st_and_15th_after_5am",
			spec:  "30 4 1,15 * *",
			after: "2026-01-01 05:00:00",
			want:  "2026-01-15 04:30:00",
		},
		{
			name:  "0430_on_1st_and_15th_and_30th",
			spec:  "30 4 1,15,30 * *",
			after: "2026-01-15 05:00:00",
			want:  "2026-01-30 04:30:00",
		},

		// Months.

		{
			name:  "month_ids",
			spec:  "0 0 1 1,6 *",
			after: "2026-03-01 00:00:00",
			want:  "2026-06-01 00:00:00",
		},
		{
			name:  "month_names",
			spec:  "0 0 1 Jan,Jun *",
			after: "2026-07-01 00:00:00",
			want:  "2027-01-01 00:00:00",
		},

		// Ranges.

		{
			name:  "work_hours_range_today",
			spec:  "0 9-17 * * *",
			after: "2026-01-01 16:00:00",
			want:  "2026-01-01 17:00:00",
		},
		{
			name:  "work_hours_range_tomorrow",
			spec:  "0 9-17 * * *",
			after: "2026-01-01 18:00:00",
			want:  "2026-01-02 09:00:00",
		},

		// Steps.

		{
			name:  "15_minute_increments",
			spec:  "*/15 * * * *",
			after: "2026-01-01 00:00:00",
			want:  "2026-01-01 00:15:00",
		},
		{
			name:  "10_minute_increments_inside_range",
			spec:  "3-50/10 * * * *",
			after: "2026-01-01 00:09:00",
			want:  "2026-01-01 00:13:00",
		},
		{
			name:  "large_but_valid_step_for_minutes",
			spec:  "*/59 * * * *",
			after: "2026-01-01 00:59:00",
			want:  "2026-01-01 01:00:00",
		},
		{
			name:  "large_but_valid_step_for_hours",
			spec:  "* */23 * * *",
			after: "2026-01-01 22:59:00",
			want:  "2026-01-01 23:00:00",
		},

		// Days of week.

		{
			name:  "day_of_week_id",
			spec:  "30 10 * * 5",
			after: "2026-01-01 00:00:00",
			want:  "2026-01-02 10:30:00",
		},
		{
			name:  "lower_case",
			spec:  "0 9 * * mon-fri",
			after: "2024-01-06 00:00:00",
			want:  "2024-01-08 09:00:00",
		},
		{
			name:  "upper_case",
			spec:  "0 9 * * MON-FRI",
			after: "2024-01-06 00:00:00",
			want:  "2024-01-08 09:00:00",
		},

		// Both day-of-month and day-of-week are restricted.

		{
			name:  "match_day_of_month",
			spec:  "40 20 1,15 * 5",
			after: "2026-01-01 05:00:00",
			want:  "2026-01-01 20:40:00",
		},
		{
			name:  "match_day_of_week",
			spec:  "40 20 1,15 * 5",
			after: "2026-01-01 21:00:00",
			want:  "2026-01-02 20:40:00",
		},

		// Predefined schedules / nicknames / aliases.

		{
			name:  "minutely",
			spec:  "@minutely",
			after: "2026-01-01 00:00:00",
			want:  "2026-01-01 00:01:00",
		},
		{
			name:  "hourly",
			spec:  "@hourly",
			after: "2026-01-01 00:00:00",
			want:  "2026-01-01 01:00:00",
		},
		{
			name:  "daily",
			spec:  "@daily",
			after: "2026-01-01 00:00:00",
			want:  "2026-01-02 00:00:00",
		},
		{
			name:  "midnight",
			spec:  "@midnight",
			after: "2026-01-01 23:59:59",
			want:  "2026-01-02 00:00:00",
		},
		{
			name:  "weekly",
			spec:  "@weekly",
			after: "2026-01-01 00:00:00",
			want:  "2026-01-04 00:00:00",
		},
		{
			name:  "monthly",
			spec:  "@monthly",
			after: "2026-01-01 00:00:00",
			want:  "2026-02-01 00:00:00",
		},
		{
			name:  "yearly",
			spec:  "@yearly",
			after: "2026-01-01 00:00:00",
			want:  "2027-01-01 00:00:00",
		},
		{
			name:  "annually",
			spec:  "@annually",
			after: "2026-12-01 00:00:00",
			want:  "2027-01-01 00:00:00",
		},

		// Every.

		{
			name:  "every_3h",
			spec:  "@every 3h",
			after: "2026-12-25 00:02:01",
			want:  "2026-12-25 03:02:01",
		},
		{
			name:  "every_6m",
			spec:  "@every 6m",
			after: "2026-12-25 03:02:01",
			want:  "2026-12-25 03:08:01",
		},
		{
			name:  "every_9s",
			spec:  "@every 9s",
			after: "2026-12-25 03:08:51",
			want:  "2026-12-25 03:09:00",
		},
		{
			name:  "every_with_complex_duration",
			spec:  "@every 11h12m13s",
			after: "2026-12-25 03:09:00",
			want:  "2026-12-25 14:21:13",
		},
		{
			name:  "every_with_spaced_duration",
			spec:  "@every 11h 12m 13s",
			after: "2026-12-25 03:09:00",
			want:  "2026-12-25 14:21:13",
		},
		{
			name:  "every_truncate_sub_second",
			spec:  "@every 1s2ms3us4ns",
			after: "2026-12-25 03:09:00",
			want:  "2026-12-25 03:09:01",
		},

		// Sundays.

		{
			name:  "sunday_as_0",
			spec:  "0 0 * * 0",
			after: "2026-01-01 00:00:00",
			want:  "2026-01-04 00:00:00",
		},
		{
			name:  "sunday_as_7",
			spec:  "0 0 * * 7",
			after: "2026-01-01 00:00:00",
			want:  "2026-01-04 00:00:00",
		},
		{
			name:  "sun_lower",
			spec:  "0 0 * * sun",
			after: "2026-01-01 00:00:00",
			want:  "2026-01-04 00:00:00",
		},
		{
			name:  "sun_upper",
			spec:  "0 0 * * SUN",
			after: "2026-01-01 00:00:00",
			want:  "2026-01-04 00:00:00",
		},
		{
			name:  "weekend_daily_by_name",
			spec:  "0 0 * * FRI-SUN", // Sunday is also 0.
			after: "2026-01-01 00:00:00",
			want:  "2026-01-02 00:00:00",
		},
		{
			name:  "weekend_daily_by_id",
			spec:  "0 0 * * 5-7", // Sunday is also 0.
			after: "2026-01-01 00:00:00",
			want:  "2026-01-02 00:00:00",
		},

		// Special cases.

		{
			name:  "leap_year",
			spec:  "0 0 29 2 *",
			after: "2024-02-29 00:00:00",
			want:  "2028-02-29 00:00:00",
		},
		{
			name:  "extreme_leap_year",
			spec:  "0 0 29 2 *",
			after: "2096-02-29 00:00:00",
			want:  "2104-02-29 00:00:00",
		},
		{
			name:  "impossible_but_valid_date",
			spec:  "* * 31 2 *",
			after: "2024-02-29 00:00:00",
			want:  "0001-01-01 00:00:00",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sched, err := cron.Parse(tt.spec, nil)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tt.spec, err)
			}

			after := mustParseTime(t, tt.after, time.UTC)
			want := mustParseTime(t, tt.want, time.UTC)
			if got := sched.Next(after); !got.Equal(want) {
				t.Errorf("Next(%v) = %v, want %v", after, got, want)
			}
		})
	}
}

func TestParseAndNextOnSundays(t *testing.T) {
	spec := "0 0 * * 0-7" // Day-of-week "SUN-SUN" is actually the same as "0", not "*".
	sched, err := cron.Parse(spec, nil)
	if err != nil {
		t.Fatalf("Parse(%q) error: %v", spec, err)
	}

	after := mustParseTime(t, "2026-01-01 00:00:00", time.UTC)
	wants := []time.Time{
		mustParseTime(t, "2026-01-04 00:00:00", time.UTC),
		mustParseTime(t, "2026-01-11 00:00:00", time.UTC),
		mustParseTime(t, "2026-01-18 00:00:00", time.UTC),
	}
	for i, want := range wants {
		t.Run(fmt.Sprintf("want_%d", i), func(t *testing.T) {
			if got := sched.Next(after); !got.Equal(want) {
				t.Errorf("Next(%v) = %v, want %v", after, got, want)
			}
			after = want
		})
	}
}

func TestParseAndNextWithReboot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec string
	}{
		{
			name: "reboot",
			spec: "@reboot",
		},
		{
			name: "once",
			spec: "@once",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := cron.Parse(tt.spec, nil)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tt.spec, err)
			}

			after := mustParseTime(t, "2027-01-01 00:00:00", time.UTC)
			got := s.Next(after)
			if got.IsZero() {
				t.Errorf("Next(%v) = zero, want non-zero", after)
			}
			got = s.Next(got)
			if !got.IsZero() {
				t.Errorf("Next(%v) = %v, want zero", after, got)
			}
		})
	}
}

func TestParseAndNextWithTimezone(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		spec     string
		timezone string
		after    string
		want     string
		wantTZ   *time.Location
	}{
		// DST transitions: spring forward.

		{
			name:     "before_spring_forward",
			spec:     "0 * * * *",
			timezone: "America/Los_Angeles",
			after:    "2026-03-08 00:00:00",
			want:     "2026-03-08 01:00:00",
		},
		{
			name:     "during_spring_forward",
			spec:     "0 * * * *",
			timezone: "America/Los_Angeles",
			after:    "2026-03-08 01:00:00",
			want:     "2026-03-08 03:00:00",
		},
		{
			name:     "after_spring_forward",
			spec:     "0 * * * *",
			timezone: "America/Los_Angeles",
			after:    "2026-03-08 03:00:00",
			want:     "2026-03-08 04:00:00",
		},
		{
			name:     "across_spring_forward",
			spec:     "0 0 9 3 *",
			timezone: "America/Los_Angeles",
			after:    "2026-03-07 00:00:00",
			want:     "2026-03-09 00:00:00",
		},
		{
			name:     "skip_gap_hour",
			spec:     "30 2 * * *",
			timezone: "America/Los_Angeles",
			after:    "2026-03-08 01:00:00",
			want:     "2026-03-09 02:30:00",
		},

		// DST transitions: fall back.

		{
			name:     "before_fall_back",
			spec:     "0 * * * *",
			timezone: "America/Los_Angeles",
			after:    "2026-11-01 00:00:00",
			want:     "2026-11-01 01:00:00",
		},
		{
			name:     "during_fall_back",
			spec:     "0 * * * *",
			timezone: "America/Los_Angeles",
			after:    "2026-11-01 01:00:00", // PDT, before the fall-back.
			want:     "2026-11-01 01:00:00", // PST, after the fall-back.
			wantTZ:   time.FixedZone("PST", -8*60*60),
		},
		{
			name:     "after_fall_back",
			spec:     "0 * * * *",
			timezone: "America/Los_Angeles",
			after:    "2026-11-01 02:00:00",
			want:     "2026-11-01 03:00:00",
		},
		{
			name:     "across_fall_back",
			spec:     "0 0 2 11 *",
			timezone: "America/Los_Angeles",
			after:    "2026-10-30 00:00:00",
			want:     "2026-11-02 00:00:00",
		},

		// DST transitions: Chile (midnight instead of 2 AM).

		{
			name:     "spring_forward_in_chile",
			spec:     "0 * * * *",
			timezone: "America/Santiago",
			after:    "2026-09-05 23:00:00",
			want:     "2026-09-06 01:00:00",
		},
		{
			name:     "skip_gap_hour_in_chile",
			spec:     "30 0 * * *",
			timezone: "America/Santiago",
			after:    "2026-09-05 23:00:00",
			want:     "2026-09-07 00:30:00",
		},
		{
			name:     "fall_back_in_chile",
			spec:     "0 * * * *",
			timezone: "America/Santiago",
			after:    "2026-04-04 00:00:00", // Before the fall-back.
			want:     "2026-04-04 00:00:00", // After the fall-back.
			wantTZ:   time.FixedZone("-0400 -04", -4*60*60),
		},

		// Corner cases: DST transitions in Chile with day-of-month/week restrictions.

		{
			name:     "spring_forward_in_chile_with_restricted_dom",
			spec:     "0 * 6 9 *",
			timezone: "America/Santiago",
			after:    "2026-09-05 23:58:00",
			want:     "2026-09-06 01:00:00",
		},
		{
			name:     "spring_forward_in_chile_with_restricted_dom_naive",
			spec:     "0 0 6 9 *",
			timezone: "America/Santiago",
			after:    "2026-09-05 23:58:00",
			want:     "2027-09-06 00:00:00",
		},

		// Timezones with non-whole-hour offsets.

		{
			name:     "newfoundland_canada_30_minute_offset_1",
			spec:     "0 0 * * *",
			timezone: "America/St_Johns",
			after:    "2026-01-01 00:00:00",
			want:     "2026-01-02 00:00:00",
		},
		{
			name:     "newfoundland_canada_30_minute_offset_2",
			spec:     "0 1 * * *",
			timezone: "Canada/Newfoundland",
			after:    "2026-01-01 00:01:00",
			want:     "2026-01-01 01:00:00",
		},
		{
			name:     "new_delhi_india_30_minute_offset",
			spec:     "0 2 * * *",
			timezone: "Asia/Kolkata",
			after:    "2026-01-01 00:02:00",
			want:     "2026-01-01 02:00:00",
		},
		{
			name:     "kathmandu_nepal_45_minute_offset",
			spec:     "0 3 * * *",
			timezone: "Asia/Kathmandu",
			after:    "2026-01-01 00:03:00",
			want:     "2026-01-01 03:00:00",
		},
		{
			name:     "chatham_islands_new_zealand_45_minute_offset",
			spec:     "0 4 * * *",
			timezone: "Pacific/Chatham",
			after:    "2026-01-01 00:04:00",
			want:     "2026-01-01 04:00:00",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			loc, err := time.LoadLocation(tt.timezone)
			if err != nil {
				t.Fatalf("time.LoadLocation(%q) error: %v", tt.timezone, err)
			}

			s, err := cron.Parse(tt.spec, loc)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tt.spec, err)
			}

			after := mustParseTime(t, tt.after, loc)
			if tt.wantTZ != nil {
				loc = tt.wantTZ
			}
			want := mustParseTime(t, tt.want, loc)
			if got := s.Next(after); !got.Equal(want) {
				t.Errorf("Next(%v) = %v, want %v", after, got, want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()

	bad := []string{
		"",
		"0",
		"*",
		"* * * *",
		"* * * * * *",

		"-1 * * * *",
		"60 * * * *",
		"* -1 * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * 32 * *",
		"* * * 0 *",
		"* * * 13 *",
		"* * * * -1",
		"* * * * 8",

		"abc * * * *",
		"* * * * abc",

		"20-10 * * * *",
		"0 9--17 * * *",

		"*/-1 * * * *",
		"*/0 * * * *",
		"*//2 * * * *",
		"*/100 * * * *",
		"1/1 * * * *",

		"@every 0s",
		"@every 999ms",
		"@every -1h",
	}
	for _, spec := range bad {
		t.Run(spec, func(t *testing.T) {
			t.Parallel()

			if _, err := cron.Parse(spec, time.UTC); err == nil {
				t.Errorf("Parse(%q) expected error", spec)
			}
		})
	}
}

func mustParseTime(t *testing.T, dateTime string, timezone *time.Location) time.Time {
	t.Helper()

	ts, err := time.ParseInLocation(time.DateTime, dateTime, timezone)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}
