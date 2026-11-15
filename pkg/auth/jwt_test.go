package auth

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestParseNumericDate(t *testing.T) {
	t.Parallel()

	utc := numericDate(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC))
	local := numericDate(time.Date(2026, time.January, 2, 3, 4, 5, 0, hostTimeZone()))
	localMidnight := numericDate(time.Date(2026, time.January, 2, 0, 0, 0, 0, hostTimeZone()))

	tests := []struct {
		in      string
		want    any
		wantErr bool
	}{
		// Decimal numbers.
		{in: "1767225600", want: json.Number("1767225600")},
		{in: " -1 ", want: json.Number("-1")},
		{in: "9223372036854775807", want: json.Number("9223372036854775807")},
		{in: "1767225600.5", want: 1767225600.5},
		{in: "1e9", want: 1e9},
		{in: "NaN", wantErr: true},
		{in: "-Inf", wantErr: true},
		{in: "infinity", wantErr: true},

		// Date-times, with or without a time zone offset, a time, and fractional seconds (which are truncated).
		{in: "2026-01-02T03:04:05Z", want: utc},
		{in: "2026-01-02T05:04:05+02:00", want: utc},
		{in: "2026-01-02 02:04:05-01:00", want: utc},
		{in: " 2026-01-02T03:04:05.999Z ", want: utc},
		{in: "2026-01-02T03:04:05", want: local},
		{in: "2026-01-02 03:04:05", want: local},
		{in: "2026-01-02T03:04:05.5", want: local},
		{in: "2026-01-02", want: localMidnight},
		{in: "03:04:05", wantErr: true},
		{in: "2026-01-02T03:04", wantErr: true},
		{in: "2026-13-02", wantErr: true},

		// Neither.
		{in: "", wantErr: true},
		{in: "tomorrow", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			got, err := parseNumericDate(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseNumericDate(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseNumericDate(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}
