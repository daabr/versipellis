package config_test

import (
	"bytes"
	_ "embed"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/dest"
)

func TestParseFile(t *testing.T) {
	dir := t.TempDir()

	empty := filepath.Join(dir, "empty.toml")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	invalid := filepath.Join(dir, "invalid.toml")
	if err := os.WriteFile(invalid, []byte("kaboom!"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())

	tests := []struct {
		name    string
		path    string
		want    map[string]any
		wantErr bool
	}{
		{
			name:    "default_file_not_found",
			path:    config.DefaultFilePath,
			want:    nil,
			wantErr: false,
		},
		{
			name:    "custom_file_not_found",
			path:    "missing_file.toml",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "directory_instead_of_file",
			path:    dir,
			want:    nil,
			wantErr: true,
		},
		{
			name:    "invalid_file",
			path:    invalid,
			want:    nil,
			wantErr: true,
		},
		{
			name:    "valid_empty_file",
			path:    empty,
			want:    map[string]any{},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotErr := config.ParseFile(tt.path)
			if (gotErr != nil) != tt.wantErr {
				t.Errorf("ParseFile() error = %v, want %v", gotErr, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseFile() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractSubmaps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  map[string]any
		key  string
		want map[string]map[string]any
	}{
		{
			name: "nil_cfg",
			cfg:  nil,
			key:  "target",
			want: map[string]map[string]any{},
		},
		{
			name: "no_matches",
			cfg:  map[string]any{},
			key:  "target",
			want: map[string]map[string]any{},
		},
		{
			name: "match_with_wrong_type",
			cfg: map[string]any{
				"target": "not_a_map",
			},
			key:  "target",
			want: map[string]map[string]any{},
		},
		{
			name: "immediate_match",
			cfg: map[string]any{
				"target": map[string]any{
					"key": "value",
				},
			},
			key: "target",
			want: map[string]map[string]any{
				"target": {
					"key": "value",
				},
			},
		},
		{
			name: "nested_match",
			cfg: map[string]any{
				"level1": map[string]any{
					"target": map[string]any{
						"key": "value",
					},
				},
			},
			key: "target",
			want: map[string]map[string]any{
				"level1.target": {
					"key": "value",
				},
			},
		},
		{
			name: "deeply_nested_match",
			cfg: map[string]any{
				"level1": map[string]any{
					"level2": map[string]any{
						"target": map[string]any{
							"key": "value",
						},
					},
				},
			},
			key: "target",
			want: map[string]map[string]any{
				"level1.level2.target": {
					"key": "value",
				},
			},
		},
		{
			name: "shallow_array_of_matches",
			cfg: map[string]any{
				"target": []any{
					map[string]any{
						"key": "value1",
					},
					map[string]any{
						"key": "value2",
					},
				},
			},
			key: "target",
			want: map[string]map[string]any{
				"target[1]": {
					"key": "value1",
				},
				"target[2]": {
					"key": "value2",
				},
			},
		},
		{
			name: "nested_array_of_matches",
			cfg: map[string]any{
				"level1": map[string]any{
					"level2": map[string]any{
						"target": []any{
							map[string]any{
								"key": "value1",
							},
							map[string]any{
								"key": "value2",
							},
						},
					},
				},
			},
			key: "target",
			want: map[string]map[string]any{
				"level1.level2.target[1]": {
					"key": "value1",
				},
				"level1.level2.target[2]": {
					"key": "value2",
				},
			},
		},
		{
			name: "nested_match_within_array",
			cfg: map[string]any{
				"level1": []any{
					42,
					"string",
					map[string]any{
						"int":    42,
						"string": "value",
						"level2": map[string]any{
							"target": map[string]any{
								"key": "value",
							},
						},
					},
				},
			},
			key: "target",
			want: map[string]map[string]any{
				"level1[3].level2.target": {
					"key": "value",
				},
			},
		},
		{
			name: "multiple_matches",
			cfg: map[string]any{
				"level1": map[string]any{
					"target": map[string]any{
						"key1": "value1",
					},
				},
				"level2": map[string]any{
					"target": map[string]any{
						"key2": "value2",
					},
				},
				"level3": map[string]any{
					"nontarget": map[string]any{
						"key3": "value3",
					},
				},
				"level4": map[string]any{
					"target": "not_a_map",
				},
				"level5": map[string]any{
					"level6": map[string]any{
						"target": map[string]any{
							"key4": "value4",
						},
					},
				},
				"target": []any{
					map[string]any{
						"key5": "value5",
					},
					"normal_string",
					42,
					map[string]any{
						"key6": "value6",
					},
				},
			},
			key: "target",
			want: map[string]map[string]any{
				"level1.target": {
					"key1": "value1",
				},
				"level2.target": {
					"key2": "value2",
				},
				"level5.level6.target": {
					"key4": "value4",
				},
				"target[1]": {
					"key5": "value5",
				},
				"target[4]": {
					"key6": "value6",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := config.ExtractSubmaps(tt.cfg, tt.key)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExtractSubmaps() = %v, want %v", got, tt.want)
			}
		})
	}
}

//go:embed testdata/parse_submaps.toml
var parseSubmaps []byte

func TestExtractSubSubmaps(t *testing.T) {
	t.Parallel()

	var cfg map[string]any
	r := bytes.NewReader(parseSubmaps)
	if err := toml.NewDecoder(r).Decode(&cfg); err != nil {
		t.Fatalf("failed to decode TOML test file: %v", err)
	}

	cfgs := config.ExtractSubmaps(cfg, "collector")
	if len(cfgs) != 6 {
		t.Errorf("ExtractSubmaps(): got %d collector submaps, want %d", len(cfgs), 6)
	}

	for gotName, cfg := range cfgs {
		wantName, ok := cfg["name"].(string)
		if !ok {
			t.Errorf("ExtractSubmaps(): missing %q field in collector config %q", "name", gotName)
		} else if gotName != wantName {
			t.Errorf("ExtractSubmaps(): collector name mismatch: got %q, want %q", gotName, wantName)
		}
	}

	validSenderTypes := []string{config.SenderTypeHTTP, config.SenderTypeHTTP3}
	cfgs = config.ExtractSubSubmaps(cfg, "sender", validSenderTypes)
	if len(cfgs) != 12 {
		t.Errorf("ExtractSubSubmaps(): got %d sender submaps, want %d", len(cfgs), 12)
	}

	for gotName, cfg := range cfgs {
		wantName, ok := cfg["name"].(string)
		if !ok {
			t.Errorf("ExtractSubSubmaps(): missing %q field in sender config %q", "name", gotName)
		} else if gotName != wantName {
			t.Errorf("ExtractSubSubmaps(): sender name mismatch: got %q, want %q", gotName, wantName)
		}
	}
}

func TestValue(t *testing.T) {
	t.Parallel()

	cfg := map[string]any{
		"string_key": "value",
		"int_key":    42,
	}

	tests := []struct {
		name string
		key  string
		def  any
		want any
	}{
		{
			name: "existing_string_key",
			key:  "string_key",
			def:  "default",
			want: "value",
		},
		{
			name: "existing_int_key",
			key:  "int_key",
			def:  0,
			want: 42,
		},
		{
			name: "nonexistent_key",
			key:  "nonexistent",
			def:  "default",
			want: "default",
		},
		{
			name: "type_mismatch",
			key:  "string_key",
			def:  0,
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			switch def := tt.def.(type) {
			case string:
				if got := config.Value(cfg, tt.key, def); got != tt.want {
					t.Errorf("Value(%q) = %v, want %v", tt.key, got, tt.want)
				}
			case int:
				if got := config.Value(cfg, tt.key, def); got != tt.want {
					t.Errorf("Value(%q) = %v, want %v", tt.key, got, tt.want)
				}
			default:
				t.Fatalf("unsupported default value type: %T", tt.def)
			}
		})
	}
}

func TestBoundedInt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		value    int64
		minValue int64
		maxValue int64
		want     int
	}{
		{
			name:     "below_min",
			value:    -1,
			minValue: 0,
			maxValue: 10,
			want:     0,
		},
		{
			name:     "above_max",
			value:    11,
			minValue: 0,
			maxValue: 10,
			want:     10,
		},
		{
			name:     "within_bounds",
			value:    5,
			minValue: 0,
			maxValue: 10,
			want:     5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := config.BoundedInt(tt.value, tt.minValue, tt.maxValue, tt.name, tt.name)
			if got != tt.want {
				t.Errorf("BoundedInt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConcurrencyLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  map[string]any
		want int
	}{
		{
			name: "default_when_omitted",
			cfg:  map[string]any{"type": "http", "trigger": "none"},
			want: 1,
		},
		{
			name: "explicit_zero",
			cfg:  map[string]any{"type": "http", "trigger": "none", "concurrency_limit": int64(0)},
			want: 0,
		},
		{
			name: "negative_to_min",
			cfg:  map[string]any{"type": "http", "trigger": "none", "concurrency_limit": int64(-1)},
			want: 0,
		},
		{
			name: "positive_in_range",
			cfg:  map[string]any{"type": "http", "trigger": "none", "concurrency_limit": int64(100)},
			want: 100,
		},
		{
			name: "overflow_to_max",
			cfg:  map[string]any{"type": "http", "trigger": "none", "concurrency_limit": int64(101)},
			want: 100,
		},
		{
			name: "invalid_type_to_default",
			cfg:  map[string]any{"type": "http", "trigger": "none", "concurrency_limit": "invalid"},
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base, err := config.NewBaseCollector(tt.cfg, tt.name, map[string]config.Sender{"": dest.Discard})
			if err != nil {
				t.Fatalf("NewBaseCollector() error = %v", err)
			}
			if base.Concurrency != tt.want {
				t.Errorf("concurrencyLimit = %d, want %d", base.Concurrency, tt.want)
			}
		})
	}
}
