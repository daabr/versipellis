package auth

import (
	"reflect"
	"testing"
)

func TestCheckOAuthURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "empty_optional_url", url: ""},
		{name: "https", url: "https://example.com/token"},
		{name: "https_uppercase_scheme", url: "HTTPS://example.com/token"},
		{name: "http_localhost", url: "http://localhost:8080/callback"},
		{name: "http_localhost_uppercase", url: "http://LOCALHOST/callback"},
		{name: "http_ipv4_loopback", url: "http://127.0.0.1:8080/callback"},
		{name: "http_ipv4_loopback_range", url: "http://127.1.2.3/callback"},
		{name: "http_ipv6_loopback", url: "http://[::1]:8080/callback"},
		{name: "http_remote_host", url: "http://example.com/token", wantErr: true},
		{name: "http_localhost_subdomain", url: "http://localhost.example.com/", wantErr: true},
		{name: "http_private_ip", url: "http://192.168.1.1/token", wantErr: true},
		{name: "http_unspecified_ip", url: "http://0.0.0.0/token", wantErr: true},
		{name: "relative_url", url: "/token", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := checkOAuthURL(tt.url, "test"); (err != nil) != tt.wantErr {
				t.Fatalf("checkOAuthURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestParseOAuthScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     map[string]any
		want    []string
		wantErr bool
	}{
		{name: "missing", cfg: map[string]any{}, want: nil},
		{name: "empty_string", cfg: map[string]any{"oauth_scopes": ""}, want: nil},
		{name: "whitespace_string", cfg: map[string]any{"oauth_scopes": " \t "}, want: nil},
		{name: "whitespace_any_slice", cfg: map[string]any{"oauth_scopes": []any{"", " \t "}}, want: nil},
		{name: "empty_any_slice", cfg: map[string]any{"oauth_scopes": []any{}}, want: nil},
		{name: "unexpected_type", cfg: map[string]any{"oauth_scopes": int64(1)}, wantErr: true},
		{
			name: "space_separated_string",
			cfg:  map[string]any{"oauth_scopes": "  read\twrite  admin "},
			want: []string{"read", "write", "admin"},
		},
		{
			name: "slice",
			cfg:  map[string]any{"oauth_scopes": []any{" read ", "write"}},
			want: []string{"read", "write"},
		},
		{
			name: "slice_splits_items",
			cfg:  map[string]any{"oauth_scopes": []any{"read write", " admin "}},
			want: []string{"read", "write", "admin"},
		},
		{
			name: "slice_skips_non_strings",
			cfg:  map[string]any{"oauth_scopes": []any{"read", int64(1), true}},
			want: []string{"read"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseOAuthScopes(tt.cfg, tt.name)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseOAuthScopes() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseOAuthScopes() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseOAuthParams(t *testing.T) {
	t.Parallel()

	reserved := []string{"scope", "state"}
	tests := []struct {
		name    string
		params  any // Omitted from the config if nil.
		want    map[string]string
		wantErr bool
	}{
		{name: "missing", want: nil},
		{name: "empty_table", params: map[string]any{}, want: map[string]string{}},
		{
			name:   "valid_params",
			params: map[string]any{"audience": "api", "prompt": "login consent", "empty": ""},
			want:   map[string]string{"audience": "api", "prompt": "login consent", "empty": ""},
		},
		{
			name:   "names_are_case_sensitive",
			params: map[string]any{"Scope": "admin"},
			want:   map[string]string{"Scope": "admin"},
		},
		{name: "not_a_table", params: "prompt=consent", wantErr: true},
		{name: "reserved_name", params: map[string]any{"scope": "admin"}, wantErr: true},
		{name: "empty_name", params: map[string]any{"": "value"}, wantErr: true},
		{name: "space_in_name", params: map[string]any{"max age": "60"}, wantErr: true},
		{name: "padded_name", params: map[string]any{" audience": "api"}, wantErr: true},
		{name: "tab_in_name", params: map[string]any{"max\tage": "60"}, wantErr: true},
		{name: "non_breaking_space_in_name", params: map[string]any{"max age": "60"}, wantErr: true},
		{name: "control_char_in_name", params: map[string]any{"audience\x00": "api"}, wantErr: true},
		{name: "non_string_value", params: map[string]any{"max_age": int64(60)}, wantErr: true},
		{name: "newline_in_value", params: map[string]any{"audience": "api\r\nX-Injected: 1"}, wantErr: true},
		{name: "control_char_in_value", params: map[string]any{"audience": "api\x7f"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := map[string]any{}
			if tt.params != nil {
				cfg["params"] = tt.params
			}
			got, err := parseOAuthParams(cfg, "params", reserved)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseOAuthParams() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseOAuthParams() = %q, want %q", got, tt.want)
			}
		})
	}
}
