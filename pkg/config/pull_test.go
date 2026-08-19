package config_test

import (
	"testing"

	"github.com/daabr/versipellis/pkg/config"
)

func TestNewGenericPuller(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  map[string]any
		wantErr bool
	}{
		{
			name:    "invalid_type",
			config:  map[string]any{"type": "invalid", "schedule": "0 * * * *"},
			wantErr: true,
		},
		{
			name:    "implicit_none",
			config:  map[string]any{},
			wantErr: false,
		},
		{
			name:    "explicit_none",
			config:  map[string]any{"type": "none"},
			wantErr: false,
		},
		{
			name:    "none_with_schedule",
			config:  map[string]any{"type": "none", "schedule": "* * * * *"},
			wantErr: true,
		},
		{
			name:    "none_with_trigger",
			config:  map[string]any{"type": "none", "trigger": "my_trigger"},
			wantErr: true,
		},
		{
			name:    "http_without_schedule_or_trigger",
			config:  map[string]any{"type": "http"},
			wantErr: true,
		},
		{
			name:    "http_with_schedule",
			config:  map[string]any{"type": "http", "schedule": "@hourly"},
			wantErr: false,
		},
		{
			name:    "sql_with_trigger",
			config:  map[string]any{"type": "sql", "trigger": "my_trigger"},
			wantErr: false,
		},
		{
			name: "both_schedule_and_trigger",
			config: map[string]any{
				"type":     "http",
				"schedule": "@daily",
				"trigger":  "my_trigger",
			},
			wantErr: true,
		},
		{
			name:    "http3_with_invalid_timezone",
			config:  map[string]any{"type": "http/3", "schedule": "@hourly", "timezone": "invalid"},
			wantErr: true,
		},
		{
			name:    "http3_with_invalid_schedule",
			config:  map[string]any{"type": "http/3", "schedule": "invalid"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := config.NewGenericPuller(tt.config); (err != nil) != tt.wantErr {
				t.Errorf("NewGenericPuller() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestLoadLocation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		timezone string
		wantNil  bool
	}{
		{
			name:     "empty",
			timezone: "",
			wantNil:  false,
		},
		{
			name:     "utc_lower_case",
			timezone: "utc",
			wantNil:  false,
		},
		{
			name:     "utc_upper_case",
			timezone: "UTC",
			wantNil:  false,
		},
		{
			name:     "local_mixed_case",
			timezone: "Local",
			wantNil:  false,
		},
		{
			name:     "iana",
			timezone: "America/New_York",
			wantNil:  false,
		},
		{
			name:     "invalid",
			timezone: "invalid",
			wantNil:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, _ := config.LoadLocation(tt.timezone); (got == nil) != tt.wantNil {
				t.Errorf("LoadLocation() nilness = %v, wantNil %v", got, tt.wantNil)
			}
		})
	}
}
