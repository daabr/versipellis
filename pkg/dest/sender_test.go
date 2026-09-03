package dest_test

import (
	"testing"

	"github.com/daabr/versipellis/pkg/dest"
)

func TestSenders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		wantNil bool
		wantOK  bool
	}{
		{
			name:    "implicit",
			key:     "",
			wantNil: true,
			wantOK:  true,
		},
		{
			name:    "discard",
			key:     "discard",
			wantNil: true,
			wantOK:  true,
		},
		{
			name:    "none",
			key:     "none",
			wantNil: true,
			wantOK:  true,
		},
		{
			name:    "stdout",
			key:     "stdout",
			wantNil: false,
			wantOK:  true,
		},
		{
			name:    "unrecognized",
			key:     "unrecognized",
			wantNil: true,
			wantOK:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, gotOK := dest.Senders[tt.key]
			if (got == nil) != tt.wantNil {
				t.Errorf("Senders[%q] = %v, wantNil %v", tt.key, got, tt.wantNil)
			}
			if gotOK != tt.wantOK {
				t.Errorf("Senders[%q] ok = %v, want %v", tt.key, gotOK, tt.wantOK)
			}
		})
	}
}
