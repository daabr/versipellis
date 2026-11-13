package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	testAuthURL     = "https://example.com/authorize"
	testCallbackURL = "http://localhost:8080/callback"
	testTokenURL    = "https://example.com/token" //gosec:disable G101 // URL, not a credential.
)

func generateTestKeys(t *testing.T) (*rsa.PrivateKey, *ecdsa.PrivateKey, ed25519.PrivateKey) {
	t.Helper()

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() error = %v", err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey() error = %v", err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey() error = %v", err)
	}
	return rsaKey, ecKey, edKey
}

func generateTestECKey(t *testing.T, curve elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()

	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey(%s) error = %v", curve.Params().Name, err)
	}
	return key
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}
}

func writeTestPEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()

	writeTestFile(t, path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}))
}

// jwtBearerConfig returns the expected result of parsing a valid JWT bearer config.
func jwtBearerConfig(method jwt.SigningMethod, key any, exp time.Duration, claims jwt.MapClaims) Config {
	return Config{
		authType:      configTypeOAuth,
		grant:         grantTypeJWTBearer,
		headerName:    "Authorization",
		headerValue:   "Bearer ",
		oauthConfig:   &oauth2.Config{Endpoint: oauth2.Endpoint{TokenURL: testTokenURL}},
		jwtTemplate:   jwt.NewWithClaims(method, claims),
		jwtExpiration: exp,
		jwtSigningKey: key,
	}
}

// clientCredsConfig returns the expected result of parsing a valid client credentials config.
func clientCredsConfig(scopes []string) Config {
	return Config{
		authType:    configTypeOAuth,
		grant:       grantTypeClientCreds,
		headerName:  "Authorization",
		headerValue: "Bearer ",
		oauthClientCreds: &clientcredentials.Config{
			ClientID:       "id",
			ClientSecret:   "secret",
			TokenURL:       testTokenURL,
			Scopes:         scopes,
			EndpointParams: url.Values{},
		},
	}
}

func TestParseConfig(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	mustDER := func(der []byte, err error) []byte {
		t.Helper()
		if err != nil {
			t.Fatalf("failed to marshal test key: %v", err)
		}
		return der
	}

	rsaKey, ecKey, edKey := generateTestKeys(t)
	rsaFile := filepath.Join(tempDir, "rsa_pkcs1.pem")
	writeTestPEM(t, rsaFile, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(rsaKey))
	ecPKCS8File := filepath.Join(tempDir, "ec_pkcs8.pem")
	writeTestPEM(t, ecPKCS8File, "PRIVATE KEY", mustDER(x509.MarshalPKCS8PrivateKey(ecKey)))
	ecSEC1File := filepath.Join(tempDir, "ec_sec1.pem")
	writeTestPEM(t, ecSEC1File, "EC PRIVATE KEY", mustDER(x509.MarshalECPrivateKey(ecKey)))
	edFile := filepath.Join(tempDir, "ed25519_pkcs8.pem")
	writeTestPEM(t, edFile, "PRIVATE KEY", mustDER(x509.MarshalPKCS8PrivateKey(edKey)))

	ecP384Key, ecP521Key := generateTestECKey(t, elliptic.P384()), generateTestECKey(t, elliptic.P521())
	ecP384File := filepath.Join(tempDir, "ec_p384.pem")
	writeTestPEM(t, ecP384File, "EC PRIVATE KEY", mustDER(x509.MarshalECPrivateKey(ecP384Key)))
	ecP521File := filepath.Join(tempDir, "ec_p521.pem")
	writeTestPEM(t, ecP521File, "PRIVATE KEY", mustDER(x509.MarshalPKCS8PrivateKey(ecP521Key)))

	ecP224Key := generateTestECKey(t, elliptic.P224())
	ecP224File := filepath.Join(tempDir, "ec_p224.pem")
	writeTestPEM(t, ecP224File, "EC PRIVATE KEY", mustDER(x509.MarshalECPrivateKey(ecP224Key)))
	publicKeyFile := filepath.Join(tempDir, "public.pem")
	writeTestPEM(t, publicKeyFile, "PUBLIC KEY", mustDER(x509.MarshalPKIXPublicKey(ecKey.Public())))
	notPEMFile := filepath.Join(tempDir, "not.pem")
	writeTestFile(t, notPEMFile, []byte("NOT A PEM"))

	noAuth := Config{authType: configTypeNone}
	newYear := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	newYearUnix := json.Number("1767225600")
	localNewYearUnix := json.Number(strconv.FormatInt(time.Date(2026, time.January, 1, 0, 0, 0, 0, hostTimeZone()).Unix(), 10))
	issuerClaims := jwt.MapClaims{"iss": "issuer"}

	tests := []struct {
		name    string
		cfg     any
		want    Config
		wantErr bool
	}{
		{
			name: "nil_defaults_to_none",
			cfg:  nil,
			want: noAuth,
		},
		{
			name: "empty_table_defaults_to_none",
			cfg:  map[string]any{},
			want: noAuth,
		},
		{
			name: "type_is_case_insensitive_and_trimmed",
			cfg:  map[string]any{"type": "  NoNe  "},
			want: noAuth,
		},
		{
			name: "basic",
			cfg:  map[string]any{"type": "basic", "basic_username": " user ", "basic_password": " pass "},
			want: Config{
				authType:    configTypeBasic,
				headerName:  "Authorization",
				headerValue: "Basic dXNlcjogcGFzcyA=",
			},
		},
		{
			name: "basic_ignores_static_settings",
			cfg: map[string]any{
				"type":           "basic",
				"basic_username": "user",
				"basic_password": "pass",
				"static_header":  "x api key", // Invalid, but unused.
				"static_prefix":  "Token\x00", // Invalid, but unused.
				"static_secret":  "s3cr3t",
			},
			want: Config{
				authType:    configTypeBasic,
				headerName:  "Authorization",
				headerValue: "Basic dXNlcjpwYXNz",
			},
		},
		{
			name: "static_with_defaults",
			cfg:  map[string]any{"type": "static", "static_secret": "s3cr3t"},
			want: Config{
				authType:    configTypeStatic,
				headerName:  "Authorization",
				headerValue: "Bearer s3cr3t",
			},
		},
		{
			name: "static_with_canonicalized_header_and_custom_prefix",
			cfg: map[string]any{
				"type":          "static",
				"static_header": " x-api-key ",
				"static_prefix": " Token ",
				"static_secret": " not trimmed ",
			},
			want: Config{
				authType:    configTypeStatic,
				headerName:  "X-Api-Key",
				headerValue: "Token  not trimmed ",
			},
		},
		{
			name: "static_with_empty_prefix",
			cfg:  map[string]any{"type": "static", "static_prefix": "  ", "static_secret": "s3cr3t"},
			want: Config{
				authType:    configTypeStatic,
				headerName:  "Authorization",
				headerValue: "s3cr3t",
			},
		},
		{
			name: "oauth_explicit_client_credentials",
			cfg: map[string]any{
				"type":                "oauth",
				"oauth_grant_type":    "Client_Credentials",
				"oauth_client_id":     "id",
				"oauth_client_secret": "secret",
				"oauth_scopes":        "read write",
				"oauth_token_url":     testTokenURL,
			},
			want: clientCredsConfig([]string{"read", "write"}),
		},
		{
			name: "oauth_explicit_jwt_bearer_urn",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_grant_type":     string(grantTypeJWTBearer),
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			want: jwtBearerConfig(jwt.SigningMethodES256, ecKey, defaultJWTExpiration, issuerClaims),
		},
		{
			name: "oauth_inferred_authorization_code_with_pkce",
			cfg: map[string]any{
				"type":               "oauth",
				"oauth_client_id":    "id",
				"oauth_pkce":         true,
				"oauth_auth_url":     testAuthURL,
				"oauth_callback_url": testCallbackURL,
				"oauth_token_url":    testTokenURL,
			},
			want: Config{
				authType:    configTypeOAuth,
				grant:       grantTypeAuthCode,
				headerName:  "Authorization",
				headerValue: "Bearer ",
				oauthConfig: &oauth2.Config{
					ClientID:    "id",
					Endpoint:    oauth2.Endpoint{AuthURL: testAuthURL, TokenURL: testTokenURL},
					RedirectURL: testCallbackURL,
				},
				oauthPKCE: true,
			},
		},
		{
			name: "oauth_explicit_authorization_code_with_secret_and_scopes",
			cfg: map[string]any{
				"type":                "oauth",
				"oauth_grant_type":    "authorization_code",
				"oauth_client_id":     "id",
				"oauth_client_secret": "secret",
				"oauth_scopes":        []any{"read", "write"},
				"oauth_auth_url":      testAuthURL,
				"oauth_callback_url":  testCallbackURL,
				"oauth_token_url":     testTokenURL,
			},
			want: Config{
				authType:    configTypeOAuth,
				grant:       grantTypeAuthCode,
				headerName:  "Authorization",
				headerValue: "Bearer ",
				oauthConfig: &oauth2.Config{
					ClientID:     "id",
					ClientSecret: "secret",
					Endpoint:     oauth2.Endpoint{AuthURL: testAuthURL, TokenURL: testTokenURL},
					RedirectURL:  testCallbackURL,
					Scopes:       []string{"read", "write"},
				},
			},
		},
		{
			name: "oauth_inferred_refresh_token",
			cfg: map[string]any{
				"type":                "oauth",
				"oauth_client_id":     "id",
				"oauth_refresh_token": "refresh",
				"oauth_token_url":     testTokenURL,
			},
			want: Config{
				authType:    configTypeOAuth,
				grant:       grantTypeRefreshToken,
				headerName:  "Authorization",
				headerValue: "Bearer ",
				oauthConfig: &oauth2.Config{
					ClientID: "id",
					Endpoint: oauth2.Endpoint{TokenURL: testTokenURL},
				},
				oauthRefreshToken: "refresh",
			},
		},
		{
			name: "oauth_inferred_client_credentials",
			cfg: map[string]any{
				"type":                "oauth",
				"oauth_client_id":     "id",
				"oauth_client_secret": "secret",
				"oauth_token_url":     testTokenURL,
			},
			want: clientCredsConfig(nil),
		},
		{
			name: "oauth_inferred_jwt_bearer",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			want: jwtBearerConfig(jwt.SigningMethodES256, ecKey, defaultJWTExpiration, issuerClaims),
		},
		{
			name: "oauth_custom_static_prefix",
			cfg: map[string]any{
				"type":                "oauth",
				"static_header":       "x-token",
				"static_prefix":       "Token",
				"oauth_client_id":     "id",
				"oauth_client_secret": "secret",
				"oauth_token_url":     testTokenURL,
			},
			want: func() Config {
				c := clientCredsConfig(nil)
				c.headerName, c.headerValue = "X-Token", "Token "
				return c
			}(),
		},
		{
			name: "pkce_without_authorization_code_only_warns",
			cfg: map[string]any{
				"type":                "oauth",
				"oauth_client_id":     "id",
				"oauth_client_secret": "secret",
				"oauth_pkce":          true,
				"oauth_token_url":     testTokenURL,
			},
			want: func() Config {
				c := clientCredsConfig(nil)
				c.oauthPKCE = true
				return c
			}(),
		},
		{
			name: "authorization_code_with_auth_params",
			cfg: map[string]any{
				"type":               "oauth",
				"oauth_client_id":    "id",
				"oauth_pkce":         true,
				"oauth_auth_url":     testAuthURL,
				"oauth_callback_url": testCallbackURL,
				"oauth_token_url":    testTokenURL,
				"oauth_auth_params": map[string]any{
					"access_type": "offline",
					"prompt":      "login consent", // Space-delimited list.
				},
			},
			want: Config{
				authType:    configTypeOAuth,
				grant:       grantTypeAuthCode,
				headerName:  "Authorization",
				headerValue: "Bearer ",
				oauthConfig: &oauth2.Config{
					ClientID:    "id",
					Endpoint:    oauth2.Endpoint{AuthURL: testAuthURL, TokenURL: testTokenURL},
					RedirectURL: testCallbackURL,
				},
				oauthAuthParams: map[string]string{"access_type": "offline", "prompt": "login consent"},
				oauthPKCE:       true,
			},
		},
		{
			name: "client_credentials_with_token_params",
			cfg: map[string]any{
				"type":                "oauth",
				"oauth_client_id":     "id",
				"oauth_client_secret": "secret",
				"oauth_token_url":     testTokenURL,
				"oauth_token_params":  map[string]any{"audience": "api", "grant_type": "custom"},
			},
			want: func() Config {
				c := clientCredsConfig(nil)
				c.oauthTokenParams = map[string]string{"audience": "api", "grant_type": "custom"}
				c.oauthClientCreds.EndpointParams = url.Values{"audience": {"api"}, "grant_type": {"custom"}}
				return c
			}(),
		},
		{
			name: "oauth_params_with_other_grant_types_only_warn",
			cfg: map[string]any{
				"type":                "oauth",
				"oauth_client_id":     "id",
				"oauth_client_secret": "secret",
				"oauth_token_url":     testTokenURL,
				"oauth_auth_params":   map[string]any{"prompt": "consent"},
				"oauth_token_params":  map[string]any{},
			},
			want: func() Config {
				c := clientCredsConfig(nil)
				c.oauthAuthParams = map[string]string{"prompt": "consent"}
				c.oauthTokenParams = map[string]string{}
				return c
			}(),
		},
		{
			name: "oauth_token_params_with_refresh_token_only_warn",
			cfg: map[string]any{
				"type":                "oauth",
				"oauth_client_id":     "id",
				"oauth_refresh_token": "refresh",
				"oauth_token_url":     testTokenURL,
				"oauth_token_params":  map[string]any{"audience": "api"},
			},
			want: Config{
				authType:    configTypeOAuth,
				grant:       grantTypeRefreshToken,
				headerName:  "Authorization",
				headerValue: "Bearer ",
				oauthConfig: &oauth2.Config{
					ClientID: "id",
					Endpoint: oauth2.Endpoint{TokenURL: testTokenURL},
				},
				oauthTokenParams:  map[string]string{"audience": "api"},
				oauthRefreshToken: "refresh",
			},
		},
		{
			name: "pkce_ignored_without_oauth_type",
			cfg:  map[string]any{"type": "none", "oauth_pkce": true},
			want: noAuth,
		},
		{
			name: "oauth_grant_type_ignored_without_oauth_type",
			cfg: map[string]any{
				"type":             "static",
				"static_secret":    "s3cr3t",
				"oauth_grant_type": "jwt_bearer",
			},
			want: Config{
				authType:    configTypeStatic,
				headerName:  "Authorization",
				headerValue: "Bearer s3cr3t",
			},
		},
		{
			name: "authorization_code_grant_ignored_without_oauth_type",
			cfg:  map[string]any{"type": "none", "oauth_grant_type": "authorization_code"},
			want: noAuth,
		},
		{
			name: "none_ignores_invalid_settings",
			cfg:  map[string]any{"static_header": "x api key", "static_prefix": "Token\x00", "oauth_grant_type": "foo"},
			want: noAuth,
		},
		{
			name: "unexpected_value_types_use_defaults",
			cfg:  map[string]any{"type": int64(1), "static_header": true, "oauth_pkce": "yes"},
			want: noAuth,
		},
		{
			name: "jwt_bearer_with_custom_expiration",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_grant_type":     "jwt_bearer",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": " " + ecPKCS8File + " ",
				"jwt_expiration":       " 10m ",
				"jwt_claims":           map[string]any{"iss": "issuer", "sub": " not trimmed "},
			},
			want: jwtBearerConfig(jwt.SigningMethodES256, ecKey, 10*time.Minute,
				jwt.MapClaims{"iss": "issuer", "sub": " not trimmed "},
			),
		},
		{
			name: "jwt_bearer_with_non_string_claims",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims": map[string]any{
					"iss": "issuer", "aud": []any{"a", "b"}, "flag": true, "f": 1.5,
					"n": int64(-1), "nbf": int64(1e9), "max": int64(math.MaxInt64),
				},
			},
			want: jwtBearerConfig(jwt.SigningMethodES256, ecKey, defaultJWTExpiration, jwt.MapClaims{
				"iss": "issuer", "aud": []any{"a", "b"}, "flag": true, "f": 1.5,
				"n": json.Number("-1"), "nbf": json.Number("1000000000"), "max": json.Number("9223372036854775807"),
			}),
		},
		{
			name: "jwt_bearer_drops_configured_integer_exp_and_iat_claims",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer", "exp": int64(1e9), "iat": int64(1e9)},
			},
			want: jwtBearerConfig(jwt.SigningMethodES256, ecKey, defaultJWTExpiration, issuerClaims),
		},
		{
			name: "jwt_bearer_drops_configured_float_exp_and_iat_claims",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer", "exp": float64(1e9), "iat": float64(1e9)},
			},
			want: jwtBearerConfig(jwt.SigningMethodES256, ecKey, defaultJWTExpiration, issuerClaims),
		},
		{
			name: "jwt_bearer_drops_configured_date_time_exp_and_iat_claims",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer", "exp": newYear, "iat": newYear.Format(time.RFC3339)},
			},
			want: jwtBearerConfig(jwt.SigningMethodES256, ecKey, defaultJWTExpiration, issuerClaims),
		},
		{
			name: "jwt_bearer_converts_quoted_number_claims",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer", "nbf": " 1767225600 ", "note": "1.5"},
			},
			want: jwtBearerConfig(jwt.SigningMethodES256, ecKey, defaultJWTExpiration, jwt.MapClaims{
				"iss": "issuer", "nbf": newYearUnix, "note": "1.5",
			}),
		},
		{
			name: "jwt_bearer_converts_date_time_claims",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims": map[string]any{
					"iss":       "issuer",
					"nbf":       " 2026-01-01T02:00:00.5+02:00 ",                                   // String in a numeric date claim.
					"auth_time": newYear.Add(500 * time.Millisecond).In(time.FixedZone("", -3600)), // TOML date-time.
					"note":      "2026-01-01T00:00:00Z",                                            // String in a custom claim.
				},
			},
			want: jwtBearerConfig(jwt.SigningMethodES256, ecKey, defaultJWTExpiration, jwt.MapClaims{
				"iss": "issuer", "nbf": newYearUnix, "auth_time": newYearUnix, "note": "2026-01-01T00:00:00Z",
			}),
		},
		{
			name: "jwt_bearer_converts_local_date_time_claims",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims": map[string]any{
					"iss":       "issuer",
					"nbf":       "2026-01-01 00:00:00.5", // String in a numeric date claim.
					"auth_time": toml.LocalDateTime{LocalDate: toml.LocalDate{Year: 2026, Month: 1, Day: 1}},
					"birthday":  toml.LocalDate{Year: 2026, Month: 1, Day: 1},
					"alarm":     toml.LocalTime{Hour: 7, Minute: 32}, // Not a point in time, so kept as-is.
				},
			},
			want: jwtBearerConfig(jwt.SigningMethodES256, ecKey, defaultJWTExpiration, jwt.MapClaims{
				"iss": "issuer", "nbf": localNewYearUnix, "auth_time": localNewYearUnix, "birthday": localNewYearUnix,
				"alarm": toml.LocalTime{Hour: 7, Minute: 32},
			}),
		},
		{
			name: "jwt_bearer_keeps_configured_scope_claim",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer", "scope": "custom"},
			},
			want: jwtBearerConfig(jwt.SigningMethodES256, ecKey, defaultJWTExpiration,
				jwt.MapClaims{"iss": "issuer", "scope": "custom"},
			),
		},
		{
			name: "jwt_bearer_scope_claim_overrides_oauth_scopes",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"oauth_scopes":         "read write",
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer", "scope": "custom"},
			},
			want: func() Config {
				c := jwtBearerConfig(jwt.SigningMethodES256, ecKey, defaultJWTExpiration,
					jwt.MapClaims{"iss": "issuer", "scope": "custom"},
				)
				c.oauthConfig.Scopes = []string{"read", "write"}
				return c
			}(),
		},
		{
			name: "jwt_bearer_oauth_scopes_set_missing_scope_claim",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"oauth_scopes":         []any{"read", "write"},
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			want: func() Config {
				c := jwtBearerConfig(jwt.SigningMethodES256, ecKey, defaultJWTExpiration,
					jwt.MapClaims{"iss": "issuer", "scope": "read write"},
				)
				c.oauthConfig.Scopes = []string{"read", "write"}
				return c
			}(),
		},
		{
			name: "jwt_bearer_with_ec_sec1_key",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecSEC1File,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			want: jwtBearerConfig(jwt.SigningMethodES256, ecKey, defaultJWTExpiration, issuerClaims),
		},
		{
			name: "jwt_bearer_with_rsa_pkcs1_key",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": rsaFile,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			want: jwtBearerConfig(jwt.SigningMethodRS256, rsaKey, defaultJWTExpiration, issuerClaims),
		},
		{
			name: "jwt_bearer_with_ed25519_key",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": edFile,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			want: jwtBearerConfig(jwt.SigningMethodEdDSA, edKey, defaultJWTExpiration, issuerClaims),
		},
		{
			name: "jwt_bearer_with_ec_p384_key",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecP384File,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			want: jwtBearerConfig(jwt.SigningMethodES384, ecP384Key, defaultJWTExpiration, issuerClaims),
		},
		{
			name: "jwt_bearer_with_ec_p521_key",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecP521File,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			want: jwtBearerConfig(jwt.SigningMethodES512, ecP521Key, defaultJWTExpiration, issuerClaims),
		},
		{
			name: "jwt_expiration_below_min_is_clamped",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer"},
				"jwt_expiration":       "1s",
			},
			want: jwtBearerConfig(jwt.SigningMethodES256, ecKey, time.Minute, issuerClaims),
		},
		{
			name: "jwt_expiration_above_max_is_clamped",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer"},
				"jwt_expiration":       "2h",
			},
			want: jwtBearerConfig(jwt.SigningMethodES256, ecKey, time.Hour, issuerClaims),
		},
		{
			name: "leftover_oauth_settings_ignored_without_oauth_type",
			cfg: map[string]any{
				"type":            "static",
				"static_secret":   "s3cr3t",
				"oauth_token_url": "not-a-url",
				"oauth_scopes":    int64(1),
				"jwt_expiration":  "5 minutes",
				"jwt_claims":      "iss=issuer",
			},
			want: Config{
				authType:    configTypeStatic,
				headerName:  "Authorization",
				headerValue: "Bearer s3cr3t",
			},
		},
		{
			name: "jwt_private_key_file_ignored_without_jwt_bearer",
			cfg:  map[string]any{"jwt_private_key_file": filepath.Join(tempDir, "nonexistent.pem")},
			want: noAuth,
		},
		{
			name:    "not_a_table",
			cfg:     "basic",
			wantErr: true,
		},
		{
			name:    "empty_type",
			cfg:     map[string]any{"type": "  "},
			wantErr: true,
		},
		{
			name:    "unrecognized_type",
			cfg:     map[string]any{"type": "digest"},
			wantErr: true,
		},
		{
			name:    "basic_without_username",
			cfg:     map[string]any{"type": "basic", "basic_password": "pass"},
			wantErr: true,
		},
		{
			name:    "basic_username_with_colon",
			cfg:     map[string]any{"type": "basic", "basic_username": "us:er", "basic_password": "pass"},
			wantErr: true,
		},
		{
			name:    "basic_without_password",
			cfg:     map[string]any{"type": "basic", "basic_username": "user"},
			wantErr: true,
		},
		{
			name:    "static_without_secret",
			cfg:     map[string]any{"type": "static"},
			wantErr: true,
		},
		{
			name:    "static_with_empty_header",
			cfg:     map[string]any{"type": "static", "static_header": " ", "static_secret": "s3cr3t"},
			wantErr: true,
		},
		{
			name:    "static_with_invalid_header",
			cfg:     map[string]any{"type": "static", "static_header": "x api key", "static_secret": "s3cr3t"},
			wantErr: true,
		},
		{
			name: "oauth_with_invalid_header",
			cfg: map[string]any{
				"type":                "oauth",
				"static_header":       "x api key",
				"oauth_client_id":     "id",
				"oauth_client_secret": "secret",
				"oauth_token_url":     testTokenURL,
			},
			wantErr: true,
		},
		{
			name:    "static_with_control_chars_in_secret",
			cfg:     map[string]any{"type": "static", "static_secret": "s3cr3t\r\nX-Injected: 1"},
			wantErr: true,
		},
		{
			name:    "static_with_control_chars_in_prefix",
			cfg:     map[string]any{"type": "static", "static_prefix": "Bearer\x00", "static_secret": "s3cr3t"},
			wantErr: true,
		},
		{
			name:    "unrecognized_grant_type",
			cfg:     map[string]any{"type": "oauth", "oauth_grant_type": "password"},
			wantErr: true,
		},
		{
			name: "invalid_auth_url",
			cfg: map[string]any{
				"type":               "oauth",
				"oauth_client_id":    "id",
				"oauth_pkce":         true,
				"oauth_auth_url":     "/relative",
				"oauth_callback_url": testCallbackURL,
				"oauth_token_url":    testTokenURL,
			},
			wantErr: true,
		},
		{
			name: "invalid_callback_url",
			cfg: map[string]any{
				"type":               "oauth",
				"oauth_client_id":    "id",
				"oauth_pkce":         true,
				"oauth_auth_url":     testAuthURL,
				"oauth_callback_url": "ftp://example.com",
				"oauth_token_url":    testTokenURL,
			},
			wantErr: true,
		},
		{
			name: "invalid_token_url",
			cfg: map[string]any{
				"type":                "oauth",
				"oauth_client_id":     "id",
				"oauth_client_secret": "secret",
				"oauth_token_url":     "https://example.com:99999", //gosec:disable G101 // URL, not a credential.
			},
			wantErr: true,
		},
		{
			name: "oauth_without_client_id",
			cfg: map[string]any{
				"type":             "oauth",
				"oauth_grant_type": "client_credentials",
				"oauth_token_url":  testTokenURL,
			},
			wantErr: true,
		},
		{
			name: "authorization_code_without_secret_or_pkce",
			cfg: map[string]any{
				"type":               "oauth",
				"oauth_client_id":    "id",
				"oauth_auth_url":     testAuthURL,
				"oauth_callback_url": testCallbackURL,
				"oauth_token_url":    testTokenURL,
			},
			wantErr: true,
		},
		{
			name: "authorization_code_without_auth_url",
			cfg: map[string]any{
				"type":               "oauth",
				"oauth_client_id":    "id",
				"oauth_pkce":         true,
				"oauth_callback_url": testCallbackURL,
				"oauth_token_url":    testTokenURL,
			},
			wantErr: true,
		},
		{
			name: "authorization_code_without_callback_url",
			cfg: map[string]any{
				"type":            "oauth",
				"oauth_client_id": "id",
				"oauth_pkce":      true,
				"oauth_auth_url":  testAuthURL,
				"oauth_token_url": testTokenURL,
			},
			wantErr: true,
		},
		{
			name:    "oauth_without_token_url",
			cfg:     map[string]any{"type": "oauth", "oauth_client_id": "id", "oauth_client_secret": "secret"},
			wantErr: true,
		},
		{
			name:    "client_credentials_without_secret",
			cfg:     map[string]any{"type": "oauth", "oauth_client_id": "id", "oauth_token_url": testTokenURL},
			wantErr: true,
		},
		{
			name: "refresh_token_grant_without_token",
			cfg: map[string]any{
				"type":             "oauth",
				"oauth_grant_type": "refresh_token",
				"oauth_client_id":  "id",
				"oauth_token_url":  testTokenURL,
			},
			wantErr: true,
		},
		{
			name: "oauth_scopes_with_unexpected_type",
			cfg: map[string]any{
				"type":                "oauth",
				"oauth_client_id":     "id",
				"oauth_client_secret": "secret",
				"oauth_token_url":     testTokenURL,
				"oauth_scopes":        int64(1),
			},
			wantErr: true,
		},
		{
			name: "oauth_auth_params_with_reserved_param",
			cfg: map[string]any{
				"type":                "oauth",
				"oauth_client_id":     "id",
				"oauth_client_secret": "secret",
				"oauth_token_url":     testTokenURL,
				"oauth_auth_params":   map[string]any{"state": "fixed"},
			},
			wantErr: true,
		},
		{
			name: "oauth_token_params_with_reserved_param",
			cfg: map[string]any{
				"type":                "oauth",
				"oauth_client_id":     "id",
				"oauth_client_secret": "secret",
				"oauth_token_url":     testTokenURL,
				"oauth_token_params":  map[string]any{"scope": "admin"},
			},
			wantErr: true,
		},
		{
			name: "invalid_jwt_expiration",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer"},
				"jwt_expiration":       "5 minutes",
			},
			wantErr: true,
		},
		{
			name: "jwt_claims_not_a_table",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           "iss=issuer",
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_without_private_key_file",
			cfg: map[string]any{
				"type":            "oauth",
				"oauth_token_url": testTokenURL,
				"jwt_claims":      map[string]any{"iss": "issuer"},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_without_claims",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_empty_claims",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_only_ignored_claims",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"exp": int64(1e9)},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_nan_claim",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer", "nbf": math.NaN()},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_unparsable_date_claim",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer", "nbf": "tomorrow"},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_quoted_nan_claim",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer", "nbf": "NaN"},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_local_time_claim",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer", "nbf": toml.LocalTime{Hour: 7, Minute: 32}},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_non_numeric_date_claim",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer", "exp": true},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_infinite_claim",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecPKCS8File,
				"jwt_claims":           map[string]any{"iss": "issuer", "nbf": math.Inf(-1)},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_nonexistent_private_key_file",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": filepath.Join(tempDir, "nonexistent.pem"),
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_non_pem_private_key_file",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": notPEMFile,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_public_key_file",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": publicKeyFile,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_unsupported_ec_curve",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      testTokenURL,
				"jwt_private_key_file": ecP224File,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseConfig(tt.cfg, tt.name)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if got != nil {
					t.Errorf("ParseConfig() = %+v, want nil", got)
				}
				return
			}

			if !keysEqual(got.jwtSigningKey, tt.want.jwtSigningKey) {
				t.Errorf("ParseConfig() JWT private key = %T, want %T", got.jwtSigningKey, tt.want.jwtSigningKey)
			}
			got.jwtSigningKey, tt.want.jwtSigningKey = nil, nil
			// The only error field is deep inside the token source (in its [singleflight.Group]), which is nil here.
			if !reflect.DeepEqual(*got, tt.want) { //nolint:govet // See above.
				t.Errorf("ParseConfig() = %+v, want %+v", *got, tt.want)
			}
		})
	}
}

// keysEqual compares private keys with their Equal method, because their internal
// (precomputed) fields aren't guaranteed to be deeply equal after a round trip.
func keysEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == b
	}
	k, ok := a.(interface {
		Equal(x crypto.PrivateKey) bool
	})
	return ok && k.Equal(b)
}
