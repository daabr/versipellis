package http

import (
	"context"
	"fmt"
	"log/slog"
	"math/bits"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/daabr/versipellis/pkg/config"
)

const (
	retryTypeNone     = "none"
	retryTypeDisabled = "disabled"
	retryTypeBackoff  = "backoff"
	retryTypeStatic   = "static"

	retryCoeffDisabled = 0
	retryCoeffStatic   = 1
	retryCoeffBackoff  = 2

	defaultRetryType           = retryTypeBackoff
	defaultMaxAttempts   int64 = 3
	defaultRetryInterval       = "1s"
	defaultMaxInterval         = "20s"
)

var validRetryTypes = []string{
	retryTypeNone,
	retryTypeDisabled,
	retryTypeBackoff,
	retryTypeStatic,
}

type retries struct {
	Coefficient int
	MaxAttempts int
	Interval    time.Duration
	MaxInterval time.Duration
}

func parseRetries(rawCfg any, method, name string) (*retries, error) {
	if rawCfg == nil {
		rawCfg = map[string]any{}
	}
	cfg, ok := rawCfg.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q must be a table of key-value pairs, got %T", "retries", rawCfg)
	}

	// Some HTTP methods do not get a default retry policy, they require an explicit
	// config if desired, because they are not guaranteed to be stateless or idempotent.
	if (method == http.MethodPatch || method == http.MethodPost) && len(cfg) == 0 {
		return &retries{Coefficient: retryCoeffDisabled, MaxAttempts: 1}, nil
	}

	t := strings.ToLower(strings.TrimSpace(config.Value(cfg, "type", defaultRetryType)))
	if !slices.Contains(validRetryTypes, t) {
		return nil, fmt.Errorf("unrecognized retry type %q", t)
	}
	if t == retryTypeNone || t == retryTypeDisabled {
		return &retries{Coefficient: retryCoeffDisabled, MaxAttempts: 1}, nil
	}

	r := &retries{Coefficient: retryCoeffBackoff}
	n := config.Value(cfg, "max_attempts", defaultMaxAttempts)
	r.MaxAttempts = config.BoundedInt(n, 1, 10, name, "maximum retry attempts")
	if r.MaxAttempts == 1 {
		r.Coefficient = retryCoeffDisabled
		return r, nil
	}

	var err error
	s := config.Value(cfg, "interval", defaultRetryInterval)
	r.Interval, err = parseRetryInterval(s, time.Millisecond, time.Minute, name)
	if err != nil {
		return nil, fmt.Errorf("invalid retry interval: %w", err)
	}
	if t == retryTypeStatic {
		r.Coefficient = retryCoeffStatic
		return r, nil
	}

	s = config.Value(cfg, "max_interval", defaultMaxInterval)
	r.MaxInterval, err = parseRetryInterval(s, r.Interval, 5*time.Minute, name)
	if err != nil {
		return nil, fmt.Errorf("invalid retry max interval: %w", err)
	}
	if r.MaxInterval == r.Interval {
		r.Coefficient = retryCoeffStatic
		r.MaxInterval = 0
	}

	return r, nil
}

func parseRetryInterval(value string, minInterval, maxInterval time.Duration, name string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("time parsing error: %w", err)
	}

	if d < minInterval {
		slog.Warn("forcing lower bound on retry interval", slog.String("name", name),
			slog.Duration("below_min", d), slog.Duration("new_value", minInterval),
		)
		d = minInterval
	}
	if d > maxInterval {
		slog.Warn("forcing upper bound on retry interval", slog.String("name", name),
			slog.Duration("above_max", d), slog.Duration("new_value", maxInterval),
		)
		d = maxInterval
	}

	return d, nil
}

func (r *retries) waitBeforeRetry(schedCtx, execCtx context.Context, attempt int) {
	interval := r.Interval
	switch {
	case attempt >= r.MaxAttempts-1:
		return // About to exit the retry loop, no need to sleep anymore.
	case r.Coefficient == retryCoeffBackoff:
		interval = r.exponentialBackoffInterval(attempt, true)
	}

	select {
	case <-schedCtx.Done():
		return
	case <-execCtx.Done():
		return
	case <-time.After(interval):
		return
	}
}

// exponentialBackoffInterval is never called on a [retries] instance with invalid values,
// which means that [parseRetries] guarantees these invariants:
// [retries.MaxInterval] >= [retries.Interval] >= 1ms.
//
// The caller only invokes this method for [retryCoeffBackoff] policies, with attempt
// as a non-negative integer less than [retries.MaxAttempts].
func (r *retries) exponentialBackoffInterval(attempt int, withJitter bool) time.Duration {
	iterations := bits.Len64(uint64(r.MaxInterval / r.Interval)) //gosec:disable G115 // Checked above.
	interval := r.Interval * (1 << (attempt % iterations))
	if !withJitter {
		return interval
	}

	// Jitter up to 20% of the calculated interval.
	jitter := time.Duration(rand.Int64N(int64(interval) / 5)) //gosec:disable G404 // Need speed here, not security.

	// Effective interval with jitter: between 90% and 110% of the calculated interval.
	return 9*interval/10 + jitter
}
