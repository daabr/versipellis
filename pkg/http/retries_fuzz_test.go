package http

import (
	"testing"
	"time"
)

func FuzzRetriesExponentialBackoffIntervalWithJitter(f *testing.F) {
	for _, maxInterval := range []time.Duration{time.Second, time.Minute, 5 * time.Minute} {
		for _, interval := range []time.Duration{time.Millisecond, time.Minute} {
			for _, maxAttempts := range []int{2, 10} {
				for attempt := range maxAttempts {
					if validFuzz(attempt, maxAttempts, int64(interval), int64(maxInterval)) {
						f.Add(attempt, maxAttempts, int64(interval), int64(maxInterval))
					}
				}
			}
		}
	}
	f.Fuzz(func(t *testing.T, attempt, maxAttempts int, interval, maxInterval int64) {
		if !validFuzz(attempt, maxAttempts, interval, maxInterval) {
			return
		}

		r := &retries{
			MaxAttempts: maxAttempts,
			Coefficient: retryCoeffBackoff,
			Interval:    time.Duration(interval),
			MaxInterval: time.Duration(maxInterval),
		}

		baseInterval := r.exponentialBackoffInterval(attempt, false)
		got := r.exponentialBackoffInterval(attempt, true)

		wantMin := 9 * baseInterval / 10
		wantMax := 11 * baseInterval / 10

		if got < wantMin || got > wantMax {
			t.Errorf("exponentialBackoffInterval(%d, true) for %+v = %v, want between %v (90%%) and %v (110%%) of %v",
				attempt, r, got, wantMin, wantMax, baseInterval,
			)
		}
	})
}

func validFuzz(attempt, maxAttempts int, interval, maxInterval int64) bool {
	switch {
	case maxAttempts < 1 || maxAttempts > 10:
		return false
	case attempt < 0 || attempt >= maxAttempts:
		return false
	case interval < int64(time.Millisecond) || interval > int64(time.Minute):
		return false
	case maxInterval < interval || maxInterval > int64(5*time.Minute):
		return false
	default:
		return true
	}
}
