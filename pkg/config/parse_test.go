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

func TestValue(t *testing.T) {
	config := map[string]any{
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
			switch def := tt.def.(type) {
			case string:
				if got := value(config, tt.key, def); got != tt.want {
					t.Errorf("value(%q) = %v, want %v", tt.key, got, tt.want)
				}
			case int:
				if got := value(config, tt.key, def); got != tt.want {
					t.Errorf("value(%q) = %v, want %v", tt.key, got, tt.want)
				}
			default:
				t.Fatalf("unsupported default value type: %T", tt.def)
			}
		})
	}
}
