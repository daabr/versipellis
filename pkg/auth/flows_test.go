package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/oauth2"
)

// fakeTokenEndpoint is an [http.RoundTripper] that responds to every request with a fixed
// token endpoint response, and records the received requests, without any network I/O.
type fakeTokenEndpoint struct {
	status int
	body   string

	mu    sync.Mutex
	forms []url.Values
	auths []string
}

func (f *fakeTokenEndpoint) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := r.ParseForm(); err != nil {
		return nil, err //nolint:wrapcheck // Test helper.
	}

	f.mu.Lock()
	f.forms = append(f.forms, r.PostForm)
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	f.mu.Unlock()

	return &http.Response{
		StatusCode: f.status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Request:    r,
	}, nil
}

func (f *fakeTokenEndpoint) requests() ([]url.Values, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forms, f.auths
}

func TestConfigDoInitialOAuthFlow(t *testing.T) {
	t.Parallel()

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey() error = %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatalf("x509.MarshalPKCS8PrivateKey() error = %v", err)
	}
	ecFile := filepath.Join(t.TempDir(), "ec.pem")
	writeTestPEM(t, ecFile, "PRIVATE KEY", der)

	clientCreds := map[string]any{
		"type":                "oauth",
		"oauth_client_id":     "id",
		"oauth_client_secret": "secret",
		"oauth_scopes":        "read write",
		"oauth_token_url":     testTokenURL,
	}
	okBody := `{"access_token":"t0k3n","token_type":"Bearer","expires_in":3600}`
	basicAuth := "Basic aWQ6c2VjcmV0" // "id:secret".

	tests := []struct {
		name       string
		cfg        map[string]any
		status     int
		body       string
		wantForms  []url.Values // One per token request.
		wantAuths  []string     // One per token request.
		wantHeader string
		wantErr    bool
	}{
		{
			name: "none_is_a_no_op",
			cfg:  map[string]any{"type": "none"},
		},
		{
			name:       "static_is_a_no_op",
			cfg:        map[string]any{"type": "static", "static_secret": "s3cr3t"},
			wantHeader: "Bearer s3cr3t",
		},
		{
			name:       "client_credentials",
			cfg:        clientCreds,
			status:     http.StatusOK,
			body:       okBody,
			wantForms:  []url.Values{{"grant_type": {"client_credentials"}, "scope": {"read write"}}},
			wantAuths:  []string{basicAuth},
			wantHeader: "Bearer t0k3n",
		},
		{
			name: "client_credentials_with_token_params",
			cfg: map[string]any{
				"type":                "oauth",
				"oauth_client_id":     "id",
				"oauth_client_secret": "secret",
				"oauth_token_url":     testTokenURL,
				"oauth_token_params":  map[string]any{"audience": "api"},
			},
			status:     http.StatusOK,
			body:       okBody,
			wantForms:  []url.Values{{"grant_type": {"client_credentials"}, "audience": {"api"}}},
			wantAuths:  []string{basicAuth},
			wantHeader: "Bearer t0k3n",
		},
		{
			name: "refresh_token",
			cfg: map[string]any{
				"type":                "oauth",
				"oauth_client_id":     "id",
				"oauth_client_secret": "secret",
				"oauth_refresh_token": "refresh",
				"oauth_token_url":     testTokenURL,
			},
			status:     http.StatusOK,
			body:       okBody,
			wantForms:  []url.Values{{"grant_type": {"refresh_token"}, "refresh_token": {"refresh"}}},
			wantAuths:  []string{basicAuth},
			wantHeader: "Bearer t0k3n",
		},
		{
			name:   "client_credentials_rejected",
			cfg:    clientCreds,
			status: http.StatusUnauthorized,
			body:   `{"error":"invalid_client"}`,
			// With the default [oauth2.AuthStyleAutoDetect], after the first failure, oauth2
			// tries again with the client credentials in the form, instead of the header.
			wantForms: []url.Values{
				{"grant_type": {"client_credentials"}, "scope": {"read write"}},
				{"grant_type": {"client_credentials"}, "scope": {"read write"}, "client_id": {"id"}, "client_secret": {"secret"}},
			},
			wantAuths: []string{basicAuth, ""},
			wantErr:   true,
		},
		{
			name: "authorization_code_not_implemented",
			cfg: map[string]any{
				"type":               "oauth",
				"oauth_client_id":    "id",
				"oauth_pkce":         true,
				"oauth_auth_url":     testAuthURL,
				"oauth_callback_url": testCallbackURL,
				"oauth_token_url":    testTokenURL,
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_not_implemented",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecFile,
				"jwt_claims":           map[string]any{"iss": "issuer", "nbf": int64(1e9)}, // Signable integer claim.
			},
			wantErr: true, // Returned only after the JWT is signed successfully.
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, err := ParseConfig(tt.cfg, tt.name)
			if err != nil {
				t.Fatalf("ParseConfig() error = %v", err)
			}

			endpoint := &fakeTokenEndpoint{status: tt.status, body: tt.body}
			ctx := context.WithValue(t.Context(), oauth2.HTTPClient, &http.Client{Transport: endpoint})

			if err := c.DoInitialOAuthFlow(ctx); (err != nil) != tt.wantErr {
				t.Fatalf("Config.DoInitialOAuthFlow() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Sensitive parsed configuration details are cleared after the flow, regardless of its result.
			if c.oauthConfig != nil || c.oauthClientCreds != nil ||
				c.oauthRefreshToken != "" || c.jwtTemplate != nil || c.jwtSigningKey != nil {
				t.Errorf("Config.DoInitialOAuthFlow() didn't clear parsed config details: %+v", c)
			}

			// The token must be reused, instead of requesting a new one for each header. After
			// a failed flow, there must be no token source that would keep requesting tokens.
			for range 2 {
				h, err := c.SetHeader(t.Context(), nil, tt.name)
				if err != nil {
					t.Fatalf("Config.SetHeader() error = %v", err)
				}
				if got := h.Get("Authorization"); got != tt.wantHeader {
					t.Errorf("Config.SetHeader() Authorization = %q, want %q", got, tt.wantHeader)
				}
			}

			forms, auths := endpoint.requests()
			if len(forms) != len(tt.wantForms) {
				t.Fatalf("token endpoint got %d requests, want %d", len(forms), len(tt.wantForms))
			}
			for i, form := range forms {
				if got, want := form.Encode(), tt.wantForms[i].Encode(); got != want {
					t.Errorf("token request %d form = %q, want %q", i+1, got, want)
				}
				if auths[i] != tt.wantAuths[i] {
					t.Errorf("token request %d Authorization = %q, want %q", i+1, auths[i], tt.wantAuths[i])
				}
			}
		})
	}
}

func TestConfigDoInitialOAuthFlowWithUnhandledGrant(t *testing.T) {
	t.Parallel()

	c := &Config{authType: configTypeOAuth, grant: "custom"}
	if err := c.DoInitialOAuthFlow(t.Context()); err == nil {
		t.Error("Config.DoInitialOAuthFlow() error = nil, want error")
	}
}

func TestConfigOAuthAuthCodeURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pkce bool
	}{
		{name: "without_pkce"},
		{name: "with_pkce", pkce: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, err := ParseConfig(map[string]any{
				"type":                "oauth",
				"oauth_client_id":     "id",
				"oauth_client_secret": "secret",
				"oauth_pkce":          tt.pkce,
				"oauth_scopes":        "read write",
				"oauth_auth_url":      testAuthURL,
				"oauth_callback_url":  testCallbackURL,
				"oauth_token_url":     testTokenURL,
				"oauth_auth_params":   map[string]any{"access_type": "offline"},
			}, tt.name)
			if err != nil {
				t.Fatalf("ParseConfig() error = %v", err)
			}

			u, err := url.Parse(c.oauthAuthCodeURL())
			if err != nil {
				t.Fatalf("url.Parse(Config.oauthAuthCodeURL()) error = %v", err)
			}
			state, verifier := c.OAuthExchangeValues()
			if state == "" {
				t.Error("Config.OAuthExchangeValues() state is empty")
			}
			if (verifier != "") != tt.pkce {
				t.Errorf("Config.OAuthExchangeValues() verifier = %q, want non-empty: %v", verifier, tt.pkce)
			}

			q := u.Query()
			want := url.Values{
				"access_type":   {"offline"},
				"client_id":     {"id"},
				"redirect_uri":  {testCallbackURL},
				"response_type": {"code"},
				"scope":         {"read write"},
				"state":         {state},
			}
			if tt.pkce {
				want.Set("code_challenge", oauth2.S256ChallengeFromVerifier(verifier))
				want.Set("code_challenge_method", "S256")
			}
			if got := u.Scheme + "://" + u.Host + u.Path; got != testAuthURL {
				t.Errorf("Config.oauthAuthCodeURL() base = %q, want %q", got, testAuthURL)
			}
			if q.Encode() != want.Encode() {
				t.Errorf("Config.oauthAuthCodeURL() query = %q, want %q", q.Encode(), want.Encode())
			}
		})
	}
}

func TestConfigOAuthAuthCodeURLWithoutOAuthConfig(t *testing.T) {
	t.Parallel()

	c := &Config{authType: configTypeNone}
	if got := c.oauthAuthCodeURL(); got != "" {
		t.Errorf("Config.oauthAuthCodeURL() = %q, want empty", got)
	}
	if state, verifier := c.OAuthExchangeValues(); state != "" || verifier != "" {
		t.Errorf("Config.OAuthExchangeValues() = (%q, %q), want empty", state, verifier)
	}
}
