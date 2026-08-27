package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
			path:    DefaultFilePath,
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
			got, gotErr := ParseFile(tt.path)
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
				"": {
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
				"level1": {
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
				"level1.level2": {
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
				"[1]": {
					"key": "value1",
				},
				"[2]": {
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
				"level1.level2[1]": {
					"key": "value1",
				},
				"level1.level2[2]": {
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
				"level1[3].level2": {
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
				"level1": {
					"key1": "value1",
				},
				"level2": {
					"key2": "value2",
				},
				"level5.level6": {
					"key4": "value4",
				},
				"[1]": {
					"key5": "value5",
				},
				"[4]": {
					"key6": "value6",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ExtractSubmaps(tt.cfg, tt.key)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExtractSubmaps() = %v, want %v", got, tt.want)
			}
		})
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
				if got := Value(cfg, tt.key, def); got != tt.want {
					t.Errorf("Value(%q) = %v, want %v", tt.key, got, tt.want)
				}
			case int:
				if got := Value(cfg, tt.key, def); got != tt.want {
					t.Errorf("Value(%q) = %v, want %v", tt.key, got, tt.want)
				}
			default:
				t.Fatalf("unsupported default value type: %T", tt.def)
			}
		})
	}
}
