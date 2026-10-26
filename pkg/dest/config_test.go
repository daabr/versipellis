package dest

import (
	"testing"
)

func TestParseDLQ(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		cfg          any
		wantMaxSize  int64
		wantWarnSize int64
		wantErr      bool
	}{
		{"no_section", nil, defaultDLQMaxSize, defaultDLQWarnSize, false},
		{"empty_section", map[string]any{}, defaultDLQMaxSize, defaultDLQWarnSize, false},
		{"custom_values", map[string]any{"warning_size": int64(10), "max_size": int64(20)}, 20, 10, false},
		{"disabled_warning", map[string]any{"warning_size": int64(0)}, 0, 0, false},
		{"negative_values", map[string]any{"warning_size": int64(-1), "max_size": int64(-1)}, 0, 0, false},
		{
			"values_above_maximum",
			map[string]any{"warning_size": maxDLQSize + 1, "max_size": maxDLQSize + 1},
			maxDLQSize,
			maxDLQSize,
			false,
		},
		{"wrong_value_type", map[string]any{"max_size": "1 GiB"}, defaultDLQMaxSize, defaultDLQWarnSize, false},
		{"unrecognized_field", map[string]any{"max_size": int64(1), "foo": "bar"}, 1, defaultDLQWarnSize, false},
		{"array_of_tables", []map[string]any{{"max_size": int64(1)}}, 0, 0, true},
		{"not_a_table", "invalid", 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			maxSize, warnSize, err := parseDLQ(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseDLQ() error = %v, want error = %v", err, tt.wantErr)
			}
			if maxSize != tt.wantMaxSize || warnSize != tt.wantWarnSize {
				t.Errorf("parseDLQ() = (%d, %d), want (%d, %d)", maxSize, warnSize, tt.wantMaxSize, tt.wantWarnSize)
			}
		})
	}
}
