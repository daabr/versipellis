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
