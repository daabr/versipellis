package codec

import (
	"slices"
	"testing"
)

func TestColumnNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cols []string
		want []string
	}{
		{
			name: "nil",
		},
		{
			name: "unique",
			cols: []string{"a", "b"},
			want: []string{"a", "b"},
		},
		{
			name: "duplicates",
			cols: []string{"id", "x", "id", "id"},
			want: []string{"id", "x", "id_3", "id_4"},
		},
		{
			name: "empty",
			cols: []string{"", "a", ""},
			want: []string{"_1", "a", "_3"},
		},
		{
			name: "suffix_conflict",
			cols: []string{"id", "id", "id_2"},
			want: []string{"id", "id_2_2", "id_2"},
		},
		{
			name: "empty_suffix_conflict",
			cols: []string{"_1", ""},
			want: []string{"_1", "_2"},
		},
		{
			name: "repeated_suffix_conflict",
			cols: []string{"", "_1", "_1_1"},
			want: []string{"_1_1_1", "_1", "_1_1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := columnNames(tt.cols)
			if !slices.Equal(got, tt.want) {
				t.Errorf("columnNames(%q) = %q, want %q", tt.cols, got, tt.want)
			}
			// Unique and non-empty names are returned without copying.
			if slices.Equal(tt.cols, tt.want) && len(got) > 0 && &got[0] != &tt.cols[0] {
				t.Errorf("columnNames(%q) copied the input slice", tt.cols)
			}
		})
	}
}
