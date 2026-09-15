package http

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/daabr/versipellis/pkg/config"
)

func TestParseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rawURL   string
		protoVer string
		want     string
		wantErr  bool
	}{
		{
			name:    "empty_url",
			rawURL:  "",
			wantErr: true,
		},
		{
			name:    "invalid_url",
			rawURL:  "://invalid-url",
			wantErr: true,
		},
		{
			name:    "relative_url",
			rawURL:  "/relative/path",
			wantErr: true,
		},
		{
			name:     "http3_with_http_scheme",
			rawURL:   "http://example.com",
			protoVer: config.CollectorTypeHTTP3,
			wantErr:  true,
		},
		{
			name:     "http3_with_https_scheme",
			rawURL:   "https://example.com",
			protoVer: config.CollectorTypeHTTP3,
			want:     "https://example.com",
			wantErr:  false,
		},
		{
			name:     "http_with_http_scheme",
			rawURL:   "http://example.com",
			protoVer: config.CollectorTypeHTTP,
			want:     "http://example.com",
			wantErr:  false,
		},
		{
			name:     "http_with_https_scheme",
			rawURL:   "https://example.com/",
			protoVer: config.CollectorTypeHTTP,
			want:     "https://example.com/",
			wantErr:  false,
		},
		{
			name:     "invalid_scheme",
			rawURL:   "invalid://example.com/",
			protoVer: config.CollectorTypeHTTP,
			want:     "",
			wantErr:  true,
		},
		{
			name:    "url_with_opaque_part",
			rawURL:  "https:opaque-part",
			wantErr: true,
		},
		{
			name:    "url_without_host",
			rawURL:  "https://",
			wantErr: true,
		},
		{
			name:    "url_with_invalid_port_number_1",
			rawURL:  "https://example.com:99999",
			wantErr: true,
		},
		{
			name:    "url_with_invalid_port_number_2",
			rawURL:  "https://example.com:0",
			wantErr: true,
		},
		{
			name:    "url_with_invalid_port_number_3",
			rawURL:  "https://example.com:-1",
			wantErr: true,
		},
		{
			name:    "url_with_non_numeric_port",
			rawURL:  "https://example.com:port",
			wantErr: true,
		},
		{
			name:    "url_with_query_and_fragment",
			rawURL:  "https://example.com/path?query=1&param2=value2#fragment",
			want:    "https://example.com/path?query=1&param2=value2#fragment",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, gotErr := parseURL(tt.rawURL, tt.protoVer, "role")
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("parseURL() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if (got == nil) != tt.wantErr || (got != nil && got.String() != tt.want) {
				t.Errorf("parseURL() = %v, want %q", got, tt.want)
			}
		})
	}
}

func TestParseMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		method  string
		wantErr bool
	}{
		{"empty", "", true},
		{"get_upper_case", "GET", false},
		{"get_lower_case", "get", false},
		{"post_mixed_case_1", "Post", false},
		{"post_lower_case_2", "posT", false},
		{"patch_upper_case", http.MethodPatch, false},
		{"put_upper_case", http.MethodPut, false},
		{"trace_upper_case", http.MethodTrace, true},
		{"invalid", "BlAh", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, gotErr := parseMethod(tt.method, "action"); (gotErr != nil) != tt.wantErr {
				t.Errorf("parseMethod() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
		})
	}
}

func TestParseQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		url     string
		cfg     any
		want    string
		wantErr bool
	}{
		{
			name: "nil_raw_leaves_url_untouched",
			url:  "https://example.com?z=9&a=1",
			cfg:  nil,
			want: "z=9&a=1", // Not re-encoded/sorted, since mergeQuery returns early.
		},
		{
			name: "empty_table_does_not_reencode_existing_query",
			url:  "https://example.com?z=9&a=1",
			cfg:  map[string]any{},
			want: "z=9&a=1", // Not re-encoded/sorted, since mergeQuery returns early.
		},
		{
			name: "adds_new_params_to_existing_query",
			url:  "https://example.com?a=1",
			cfg:  map[string]any{"b": "2"},
			want: "a=1&b=2",
		},
		{
			name: "overrides_same-name_param",
			url:  "https://example.com?a=1",
			cfg:  map[string]any{"a": "2"},
			want: "a=2",
		},
		{
			name: "no_existing_query",
			url:  "https://example.com",
			cfg:  map[string]any{"a": "1"},
			want: "a=1",
		},
		{
			name:    "raw_not_a_table",
			url:     "https://example.com",
			cfg:     "a=1",
			wantErr: true,
		},
		{
			name:    "value_not_a_string",
			url:     "https://example.com",
			cfg:     map[string]any{"a": 1},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			u, err := url.Parse(tt.url)
			if err != nil {
				t.Fatalf("url.Parse(%q) error: %v", tt.url, err)
			}

			gotErr := parseQuery(u, tt.cfg, "role")
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("parseQuery() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if !tt.wantErr && u.RawQuery != tt.want {
				t.Errorf("parseQuery() RawQuery = %q, want %q", u.RawQuery, tt.want)
			}
		})
	}
}

func TestParseHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     any
		want    http.Header
		wantErr bool
	}{
		{
			name: "no_headers",
			cfg:  nil,
			want: http.Header{},
		},
		{
			name: "empty_table",
			cfg:  map[string]any{},
			want: http.Header{},
		},
		{
			name: "single_header",
			cfg:  map[string]any{"Accept": "application/json"},
			want: http.Header{"Accept": {"application/json"}},
		},
		{
			name: "lowercase_key_is_canonicalized",
			cfg:  map[string]any{"accept": "text/plain"},
			want: http.Header{"Accept": {"text/plain"}},
		},
		{
			name: "comma-separated_value_is_kept_as_a_single_value",
			cfg:  map[string]any{"Accept": "text/plain, application/json, application/xml"},
			want: http.Header{"Accept": {"text/plain, application/json, application/xml"}},
		},
		{
			name: "multiple_headers",
			cfg: map[string]any{
				"Accept":        "application/json",
				"Authorization": "Bearer token",
			},
			want: http.Header{
				"Accept":        {"application/json"},
				"Authorization": {"Bearer token"},
			},
		},
		{
			name:    "headers_not_a_table",
			cfg:     "Accept: application/json",
			wantErr: true,
		},
		{
			name:    "invalid_key",
			cfg:     map[string]any{" ": "value"},
			wantErr: true,
		},
		{
			name:    "value_not_a_string",
			cfg:     map[string]any{"Key": 1},
			wantErr: true,
		},
		{
			name:    "invalid_value",
			cfg:     map[string]any{"Key": "text/plain\napplication/json"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, gotErr := parseHeaders(tt.cfg, "role")
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("parseHeaders() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseHeaders() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
