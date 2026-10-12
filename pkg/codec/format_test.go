package codec_test

import (
	"maps"
	"testing"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/config"
)

func TestParseFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     map[string]any
		want    config.Format
		wantErr bool
	}{
		{
			name: "nil_config",
			want: codec.FormatDefault,
		},
		{
			name: "missing",
			cfg:  map[string]any{"other": "json"},
			want: codec.FormatDefault,
		},
		{
			name: "explicit_default",
			cfg:  map[string]any{"format": "default"},
			want: codec.FormatDefault,
		},
		{
			name: "json",
			cfg:  map[string]any{"format": "json"},
			want: codec.FormatJSON,
		},
		{
			name: "json_array_mixed_case_and_spaces",
			cfg:  map[string]any{"format": " JSON_Array "},
			want: codec.FormatJSONArray,
		},
		{
			name: "ndjson",
			cfg:  map[string]any{"format": "NDJSON"},
			want: codec.FormatNDJSON,
		},
		{
			name: "raw",
			cfg:  map[string]any{"format": "raw"},
			want: codec.FormatRaw,
		},
		{
			name:    "invalid",
			cfg:     map[string]any{"format": "jsonl"},
			wantErr: true,
		},
		{
			name: "wrong_type",
			cfg:  map[string]any{"format": int64(1)},
			want: codec.FormatDefault,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := codec.ParseFormat(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseFormat() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseFormat() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseStructuredFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     map[string]any
		want    config.Format
		wantErr bool
	}{
		{name: "missing", want: codec.FormatDefault},
		{name: "default", cfg: map[string]any{"format": "default"}, want: codec.FormatDefault},
		{name: "json", cfg: map[string]any{"format": "JSON"}, want: codec.FormatJSON},
		{name: "json_array", cfg: map[string]any{"format": "json_array"}, want: codec.FormatJSONArray},
		{name: "ndjson", cfg: map[string]any{"format": "ndjson"}, want: codec.FormatNDJSON},
		{name: "raw", cfg: map[string]any{"format": "raw"}, wantErr: true},
		{name: "invalid", cfg: map[string]any{"format": "jsonl"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := codec.ParseStructuredFormat(tt.cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseStructuredFormat() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseStructuredFormat() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		preferred config.Format
		fallback  config.Format
		wantName  config.Format
		wantFmtr  codec.Formatter
	}{
		{"preferred", codec.FormatJSONArray, codec.FormatNDJSON, codec.FormatJSONArray, codec.JSONArray},
		{"fallback_for_default", codec.FormatDefault, codec.FormatJSON, codec.FormatJSON, codec.JSON},
		{"fallback_for_raw", codec.FormatRaw, codec.FormatNDJSON, codec.FormatNDJSON, codec.NDJSON},
		{"fallback_for_invalid", "invalid", codec.FormatJSON, codec.FormatJSON, codec.JSON},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotName, gotFmtr := codec.Resolve(tt.preferred, tt.fallback)
			if gotName != tt.wantName || gotFmtr != tt.wantFmtr {
				t.Errorf("Resolve(%q, %q) = (%q, %v), want (%q, %v)",
					tt.preferred, tt.fallback, gotName, gotFmtr, tt.wantName, tt.wantFmtr,
				)
			}
		})
	}
}

func TestResolveInvalidFallback(t *testing.T) {
	t.Parallel()

	for _, fallback := range []config.Format{codec.FormatDefault, codec.FormatRaw, "invalid"} {
		t.Run(string(fallback), func(t *testing.T) {
			t.Parallel()

			defer func() {
				if recover() == nil {
					t.Errorf("Resolve(%q, %q) didn't panic", codec.FormatDefault, fallback)
				}
			}()
			codec.Resolve(codec.FormatDefault, fallback)
		})
	}
}

func TestStructuredFormats(t *testing.T) {
	t.Parallel()

	want := map[config.Format]codec.Formatter{
		codec.FormatJSON:      codec.JSON,
		codec.FormatJSONArray: codec.JSONArray,
		codec.FormatNDJSON:    codec.NDJSON,
	}
	got := maps.Collect(codec.StructuredFormats())
	if !maps.Equal(got, want) {
		t.Errorf("StructuredFormats() = %v, want %v", got, want)
	}
}
