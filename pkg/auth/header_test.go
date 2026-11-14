package auth

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"golang.org/x/oauth2"
)

type failingTokenSource struct{}

func (failingTokenSource) Token() (*oauth2.Token, error) {
	return nil, errors.New("token endpoint unavailable")
}

func TestConfigSetHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *Config
		hdr     http.Header
		want    http.Header
		cancel  bool
		wantErr bool
	}{
		{
			name: "nil_config_and_headers",
			want: http.Header{},
		},
		{
			name: "nil_config",
			hdr:  http.Header{"Accept": {"*/*"}},
			want: http.Header{"Accept": {"*/*"}},
		},
		{
			name: "nil_headers",
			cfg:  &Config{authType: configTypeStatic, headerName: "Authorization", headerValue: "Bearer s3cr3t"},
			want: http.Header{"Authorization": {"Bearer s3cr3t"}},
		},
		{
			name: "none",
			cfg:  &Config{authType: configTypeNone, headerName: "Authorization"},
			hdr:  http.Header{"Accept": {"*/*"}},
			want: http.Header{"Accept": {"*/*"}},
		},
		{
			name: "basic",
			cfg:  &Config{authType: configTypeBasic, headerName: "Authorization", headerValue: "Basic dXNlcjpwYXNz"},
			hdr:  http.Header{"Accept": {"*/*"}},
			want: http.Header{"Accept": {"*/*"}, "Authorization": {"Basic dXNlcjpwYXNz"}},
		},
		{
			name: "static_with_custom_header",
			cfg:  &Config{authType: configTypeStatic, headerName: "X-Api-Key", headerValue: "s3cr3t"},
			hdr:  http.Header{"Accept": {"*/*"}},
			want: http.Header{"Accept": {"*/*"}, "X-Api-Key": {"s3cr3t"}},
		},
		{
			name: "static_overrides_existing_header",
			cfg:  &Config{authType: configTypeStatic, headerName: "Authorization", headerValue: "Bearer new"},
			hdr:  http.Header{"Authorization": {"Bearer old", "Bearer older"}},
			want: http.Header{"Authorization": {"Bearer new"}},
		},
		{
			name: "oauth_without_token_source",
			cfg:  &Config{authType: configTypeOAuth, headerName: "Authorization", headerValue: "Bearer "},
			hdr:  http.Header{"Accept": {"*/*"}},
			want: http.Header{"Accept": {"*/*"}},
		},
		{
			name: "oauth_with_token",
			cfg: &Config{
				authType:      configTypeOAuth,
				headerName:    "Authorization",
				headerValue:   "Bearer ",
				oauthTokenSrc: newSharedTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "t0k3n"}), nil),
			},
			hdr:  http.Header{"Accept": {"*/*"}},
			want: http.Header{"Accept": {"*/*"}, "Authorization": {"Bearer t0k3n"}},
		},
		{
			name: "oauth_with_token_error",
			cfg: &Config{
				authType:      configTypeOAuth,
				headerName:    "Authorization",
				headerValue:   "Bearer ",
				oauthTokenSrc: newSharedTokenSource(failingTokenSource{}, nil),
			},
			hdr:     http.Header{"Accept": {"*/*"}},
			wantErr: true,
		},
		{
			name: "oauth_with_canceled_request",
			cfg: &Config{
				authType:      configTypeOAuth,
				headerName:    "Authorization",
				headerValue:   "Bearer ",
				oauthTokenSrc: newSharedTokenSource(failingTokenSource{}, nil),
			},
			hdr:     http.Header{"Accept": {"*/*"}},
			cancel:  true,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.cancel {
				cancel()
			}

			orig := tt.hdr.Clone()
			got, err := tt.cfg.SetHeader(ctx, tt.hdr, tt.name)
			if (err != nil) != tt.wantErr {
				t.Errorf("Config.SetHeader() error = %v, want error: %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Config.SetHeader() = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.hdr, orig) {
				t.Errorf("Config.SetHeader() modified its input headers: got %v, want %v", tt.hdr, orig)
			}
		})
	}
}

func TestConfigHeaderName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{"nil_config", nil, ""},
		{"none", &Config{authType: configTypeNone}, ""},
		{"basic", &Config{authType: configTypeBasic, headerName: "Authorization"}, "Authorization"},
		{"static_custom_header", &Config{authType: configTypeStatic, headerName: "X-Api-Key"}, "X-Api-Key"},
		{"oauth_custom_header", &Config{authType: configTypeOAuth, headerName: "X-Token"}, "X-Token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.cfg.HeaderName(); got != tt.want {
				t.Errorf("HeaderName() = %q, want %q", got, tt.want)
			}
		})
	}
}
