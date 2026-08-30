package cron_test

import (
	"testing"
	"time"

	"github.com/daabr/versipellis/pkg/cron"
)

func FuzzParse(f *testing.F) {
	seeds := []string{
		"* * * * *",
		"@hourly",
		"@daily",
		"@every 1h30m",
		"0-30/5 9-17 1,15 Jan-Dec Mon-Fri",
		"59 23 31 12 0",
		"0 0 1 1 7",
		"*/0 * * * *",
		"100-200 * * * *",
		"-1 * * * *",
		"0 0 31 2 *",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, spec string) {
		sched, err := cron.Parse(spec, time.UTC)
		if err != nil {
			return // Expected for invalid syntax.
		}
		if sched == nil {
			t.Fatal("expected non-nil schedule when err is nil for spec: " + spec)
		}
	})
}
