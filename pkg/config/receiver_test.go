package config_test

import (
	"testing"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/dest"
)

func TestNewBaseReceiver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     map[string]any
		wantErr bool
	}{
		{
			name:    "invalid_type",
			cfg:     map[string]any{"type": "invalid"},
			wantErr: true,
		},
		{
			name:    "destination_without_type",
			cfg:     map[string]any{"destination": config.SenderTypeDiscard},
			wantErr: true,
		},
		{
			name:    "implicit_destination_none",
			cfg:     map[string]any{"type": config.ReceiverTypeHTTP},
			wantErr: false,
		},
		{
			name:    "explicit_destination_discard",
			cfg:     map[string]any{"type": config.ReceiverTypeHTTP3, "destination": config.SenderTypeDiscard},
			wantErr: false,
		},
		{
			name:    "invalid_destination",
			cfg:     map[string]any{"type": config.ReceiverTypeHTTP3, "destination": "invalid"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			senders := map[string]config.Sender{"": dest.Discard, "discard": dest.Discard}
			if _, err := config.NewBaseReceiver(tt.cfg, tt.name, senders); (err != nil) != tt.wantErr {
				t.Errorf("NewBaseReceiver(%s) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
		})
	}
}
