package http

import (
	"net/http"
	"reflect"
	"testing"
	"testing/synctest"
	"time"
)

func TestParseRetries(t *testing.T) {
	t.Parallel()

	defaultRetries := &retries{
		Coefficient: retryCoeffBackoff,
		MaxAttempts: int(defaultMaxAttempts),
		Interval:    time.Second,
		MaxInterval: 20 * time.Second,
	}

	tests := []struct {
		name    string
		rawCfg  any
		method  string
		want    *retries
		wantErr bool
	}{
		{
			name:    "nil",
			rawCfg:  nil,
			want:    defaultRetries,
			wantErr: false,
		},
		{
			name:    "empty",
			rawCfg:  map[string]any{},
			want:    defaultRetries,
			wantErr: false,
		},
		{
			name:    "invalid_type",
			rawCfg:  map[string]any{"type": "invalid"},
			want:    nil,
			wantErr: true,
		},
		{
			name:   "patch_method_disabled_by_default",
			rawCfg: nil,
			method: http.MethodPatch,
			want:   &retries{Coefficient: retryCoeffDisabled, MaxAttempts: 1},
		},
		{
			name:   "patch_method_with_explicit_config",
			rawCfg: map[string]any{"type": retryTypeBackoff},
			method: http.MethodPatch,
			want:   defaultRetries,
		},
		{
			name:   "post_method_disabled_by_default",
			rawCfg: nil,
			method: http.MethodPost,
			want:   &retries{Coefficient: retryCoeffDisabled, MaxAttempts: 1},
		},
		{
			name:   "post_method_with_explicit_config",
			rawCfg: map[string]any{"type": retryTypeBackoff},
			method: http.MethodPost,
			want:   defaultRetries,
		},
		{
			name:   "disabled",
			rawCfg: map[string]any{"type": retryTypeDisabled},
			want:   &retries{Coefficient: retryCoeffDisabled, MaxAttempts: 1},
		},
		{
			name:   "static_type",
			rawCfg: map[string]any{"type": retryTypeStatic},
			want:   &retries{Coefficient: retryCoeffStatic, MaxAttempts: int(defaultMaxAttempts), Interval: time.Second},
		},
		{
			name:   "effectively_single_attempt",
			rawCfg: map[string]any{"max_attempts": int64(1)},
			want:   &retries{Coefficient: retryCoeffDisabled, MaxAttempts: 1},
		},
		{
			name:   "effectively_static_type",
			rawCfg: map[string]any{"max_interval": "1s"},
			want:   &retries{Coefficient: retryCoeffStatic, MaxAttempts: int(defaultMaxAttempts), Interval: time.Second},
		},
		{
			name:    "invalid_interval",
			rawCfg:  map[string]any{"interval": "not-a-duration"},
			want:    nil,
			wantErr: true,
		},
		{
			name:    "invalid_max_interval",
			rawCfg:  map[string]any{"max_interval": "not-a-duration"},
			want:    nil,
			wantErr: true,
		},
		{
			name:   "max_attempts_below_min",
			rawCfg: map[string]any{"max_attempts": int64(0)},
			want:   &retries{MaxAttempts: 1},
		},
		{
			name:   "max_attempts_above_max",
			rawCfg: map[string]any{"max_attempts": int64(1000)},
			want: &retries{
				MaxAttempts: 10,
				Coefficient: retryCoeffBackoff,
				Interval:    time.Second,
				MaxInterval: 20 * time.Second,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, gotErr := parseRetries(tt.rawCfg, tt.method, tt.name)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("parseRetries() error: %v, wantErr = %v", gotErr, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseRetries() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseRetryInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		value    string
		minValue time.Duration
		maxValue time.Duration
		want     time.Duration
		wantErr  bool
	}{
		{
			name:     "invalid_duration",
			value:    "not-a-duration",
			minValue: time.Second,
			maxValue: 10 * time.Second,
			want:     0,
			wantErr:  true,
		},
		{
			name:     "below_min",
			value:    "500ms",
			minValue: time.Second,
			maxValue: 10 * time.Second,
			want:     time.Second,
			wantErr:  false,
		},
		{
			name:     "above_max",
			value:    "20s",
			minValue: time.Second,
			maxValue: 10 * time.Second,
			want:     10 * time.Second,
			wantErr:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, gotErr := parseRetryInterval(tt.value, tt.minValue, tt.maxValue, tt.name)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("parseRetryInterval() error: %v, wantErr = %v", gotErr, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseRetryInterval() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestRetriesExponentialBackoffInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		attempt int
		want    time.Duration
	}{
		{
			name:    "attempt_1_without_jitter",
			attempt: 0,
			want:    time.Second,
		},
		{
			name:    "attempt_2_without_jitter",
			attempt: 1,
			want:    2 * time.Second,
		},
		{
			name:    "attempt_3_without_jitter",
			attempt: 2,
			want:    4 * time.Second,
		},
		{
			name:    "attempt_4_without_jitter",
			attempt: 3,
			want:    8 * time.Second,
		},
		{
			name:    "attempt_5_without_jitter",
			attempt: 4,
			want:    16 * time.Second,
		},
		{
			name:    "attempt_6_without_jitter",
			attempt: 5,
			want:    1 * time.Second,
		},
		{
			name:    "attempt_7_without_jitter",
			attempt: 6,
			want:    2 * time.Second,
		},
		{
			name:    "attempt_8_without_jitter",
			attempt: 7,
			want:    4 * time.Second,
		},
		{
			name:    "attempt_9_without_jitter",
			attempt: 8,
			want:    8 * time.Second,
		},
		{
			name:    "attempt_10_without_jitter",
			attempt: 9,
			want:    16 * time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &retries{
				MaxAttempts: 10,
				Coefficient: retryCoeffBackoff,
				Interval:    time.Second,
				MaxInterval: 20 * time.Second,
			}
			if got := r.exponentialBackoffInterval(tt.attempt, false); got != tt.want {
				t.Errorf("retries.exponentialBackoffInterval() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestRetriesWaitBeforeRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		retries   *retries
		attempt   int
		wantSleep time.Duration
	}{
		{
			name:      "no_retries",
			retries:   &retries{Coefficient: retryCoeffDisabled, MaxAttempts: 1},
			attempt:   0,
			wantSleep: 0,
		},
		{
			name:      "static_attempt_1",
			retries:   &retries{Coefficient: retryCoeffStatic, MaxAttempts: 3, Interval: time.Second},
			attempt:   0,
			wantSleep: time.Second,
		},
		{
			name:      "static_attempt_2",
			retries:   &retries{Coefficient: retryCoeffStatic, MaxAttempts: 3, Interval: time.Second},
			attempt:   1,
			wantSleep: time.Second,
		},
		{
			name:      "static_attempt_3",
			retries:   &retries{Coefficient: retryCoeffStatic, MaxAttempts: 3, Interval: time.Second},
			attempt:   2,
			wantSleep: 0,
		},
		{
			name: "backoff_attempt_1",
			retries: &retries{
				Coefficient: retryCoeffBackoff,
				MaxAttempts: 8,
				Interval:    time.Second,
				MaxInterval: 20 * time.Second,
			},
			attempt:   0,
			wantSleep: 1 * time.Second,
		},
		{
			name: "backoff_attempt_2",
			retries: &retries{
				Coefficient: retryCoeffBackoff,
				MaxAttempts: 8,
				Interval:    time.Second,
				MaxInterval: 20 * time.Second,
			},
			attempt:   1,
			wantSleep: 2 * time.Second,
		},
		{
			name: "backoff_attempt_3",
			retries: &retries{
				Coefficient: retryCoeffBackoff,
				MaxAttempts: 8,
				Interval:    time.Second,
				MaxInterval: 20 * time.Second,
			},
			attempt:   2,
			wantSleep: 4 * time.Second,
		},
		{
			name: "backoff_attempt_4",
			retries: &retries{
				Coefficient: retryCoeffBackoff,
				MaxAttempts: 8,
				Interval:    time.Second,
				MaxInterval: 20 * time.Second,
			},
			attempt:   3,
			wantSleep: 8 * time.Second,
		},
		{
			name: "backoff_attempt_5",
			retries: &retries{
				Coefficient: retryCoeffBackoff,
				MaxAttempts: 8,
				Interval:    time.Second,
				MaxInterval: 20 * time.Second,
			},
			attempt:   4,
			wantSleep: 16 * time.Second,
		},
		{
			name: "backoff_attempt_6",
			retries: &retries{
				Coefficient: retryCoeffBackoff,
				MaxAttempts: 8,
				Interval:    time.Second,
				MaxInterval: 20 * time.Second,
			},
			attempt:   5,
			wantSleep: 1 * time.Second,
		},
		{
			name: "backoff_attempt_7",
			retries: &retries{
				Coefficient: retryCoeffBackoff,
				MaxAttempts: 8,
				Interval:    time.Second,
				MaxInterval: 20 * time.Second,
			},
			attempt:   6,
			wantSleep: 2 * time.Second,
		},
		{
			name: "backoff_attempt_8",
			retries: &retries{
				Coefficient: retryCoeffBackoff,
				MaxAttempts: 8,
				Interval:    time.Second,
				MaxInterval: 20 * time.Second,
			},
			attempt:   7,
			wantSleep: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				wantMin := 9 * tt.wantSleep / 10
				wantMax := 11 * tt.wantSleep / 10

				start := time.Now()
				tt.retries.waitBeforeRetry(t.Context(), t.Context(), tt.attempt)
				got := time.Since(start)

				if tt.retries.Coefficient != retryCoeffBackoff && got != tt.wantSleep {
					t.Errorf("retries.waitBeforeRetry() for %q = %v, want %v", tt.name, got, tt.wantSleep)
				}
				if tt.retries.Coefficient == retryCoeffBackoff && (got < wantMin || got > wantMax) {
					t.Errorf("retries.waitBeforeRetry() for %q = %v, want between %v (90%%) and %v (110%%) of %v",
						tt.name, got, wantMin, wantMax, tt.wantSleep,
					)
				}
			})
		})
	}
}
