package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func mustParseURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()

	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", rawURL, err)
	}
	return u
}

func generateTestKeys(t *testing.T) (*ecdsa.PrivateKey, *rsa.PrivateKey, ed25519.PrivateKey) {
	t.Helper()

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey() error = %v", err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() error = %v", err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey() error = %v", err)
	}
	return ecKey, rsaKey, edKey
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

// keysEqual compares private keys with their Equal method, because their internal
// (precomputed) fields aren't guaranteed to be deeply equal after a round trip.
func keysEqual(a, b crypto.Signer) bool {
	if a == nil || b == nil {
		return a == b
	}
	k, ok := a.(interface {
		Equal(x crypto.PrivateKey) bool
	})
	return ok && k.Equal(b)
}

func TestParseConfig(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	ecKey, rsaKey, edKey := generateTestKeys(t)
	mustDER := func(der []byte, err error) []byte {
		t.Helper()
		if err != nil {
			t.Fatalf("failed to marshal test key: %v", err)
		}
		return der
	}

	keyFile := filepath.Join(tempDir, "ec_pkcs8.pem")
	writeTestPEM(t, keyFile, "PRIVATE KEY", mustDER(x509.MarshalPKCS8PrivateKey(ecKey)))
	ecSEC1File := filepath.Join(tempDir, "ec_sec1.pem")
	writeTestPEM(t, ecSEC1File, "EC PRIVATE KEY", mustDER(x509.MarshalECPrivateKey(ecKey)))
	rsaFile := filepath.Join(tempDir, "rsa_pkcs1.pem")
	writeTestPEM(t, rsaFile, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(rsaKey))
	edFile := filepath.Join(tempDir, "ed25519_pkcs8.pem")
	writeTestPEM(t, edFile, "PRIVATE KEY", mustDER(x509.MarshalPKCS8PrivateKey(edKey)))
	ecP384Key, ecP521Key, ecP224Key := generateTestECKey(t, elliptic.P384()), generateTestECKey(t, elliptic.P521()),
		generateTestECKey(t, elliptic.P224())
	ecP384File := filepath.Join(tempDir, "ec_p384.pem")
	writeTestPEM(t, ecP384File, "EC PRIVATE KEY", mustDER(x509.MarshalECPrivateKey(ecP384Key)))
	ecP521File := filepath.Join(tempDir, "ec_p521.pem")
	writeTestPEM(t, ecP521File, "PRIVATE KEY", mustDER(x509.MarshalPKCS8PrivateKey(ecP521Key)))
	ecP224File := filepath.Join(tempDir, "ec_p224.pem")
	writeTestPEM(t, ecP224File, "EC PRIVATE KEY", mustDER(x509.MarshalECPrivateKey(ecP224Key)))
	publicKeyFile := filepath.Join(tempDir, "public.pem")
	writeTestPEM(t, publicKeyFile, "PUBLIC KEY", mustDER(x509.MarshalPKIXPublicKey(ecKey.Public())))

	notPEMFile := filepath.Join(tempDir, "not.pem")
	writeTestFile(t, notPEMFile, []byte("NOT A PEM"))

	authURL := "https://example.com/authorize"
	callbackURL := "http://localhost:8080/callback"
	tokenURL := "https://example.com/token" //gosec:disable G101 // URL, not a credential.

	tests := []struct {
		name    string
		cfg     any
		want    Config
		wantErr bool
	}{
		{
			name: "nil_defaults_to_none",
			cfg:  nil,
			want: Config{
				Type:         ConfigTypeNone,
				StaticHeader: "Authorization",
				StaticPrefix: "Bearer ",
			},
		},
		{
			name: "empty_table_defaults_to_none",
			cfg:  map[string]any{},
			want: Config{
				Type:         ConfigTypeNone,
				StaticHeader: "Authorization",
				StaticPrefix: "Bearer ",
			},
		},
		{
			name: "type_is_case_insensitive_and_trimmed",
			cfg:  map[string]any{"type": "  NoNe  "},
			want: Config{
				Type:         ConfigTypeNone,
				StaticHeader: "Authorization",
				StaticPrefix: "Bearer ",
			},
		},
		{
			name: "basic",
			cfg:  map[string]any{"type": "basic", "basic_username": " user ", "basic_password": " pass "},
			want: Config{
				Type:          ConfigTypeBasic,
				BasicUsername: "user",
				BasicPassword: " pass ",
				StaticHeader:  "Authorization",
				StaticPrefix:  "Bearer ",
			},
		},
		{
			name: "static_with_defaults",
			cfg:  map[string]any{"type": "static", "static_secret": "s3cr3t"},
			want: Config{
				Type:         ConfigTypeStatic,
				StaticHeader: "Authorization",
				StaticPrefix: "Bearer ",
				StaticSecret: "s3cr3t",
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
				Type:         ConfigTypeStatic,
				StaticHeader: "X-Api-Key",
				StaticPrefix: "Token ",
				StaticSecret: " not trimmed ",
			},
		},
		{
			name: "static_with_empty_prefix",
			cfg:  map[string]any{"type": "static", "static_prefix": "  ", "static_secret": "s3cr3t"},
			want: Config{
				Type:         ConfigTypeStatic,
				StaticHeader: "Authorization",
				StaticSecret: "s3cr3t",
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
				"oauth_token_url":     tokenURL,
			},
			want: Config{
				Type:              ConfigTypeOAuth,
				StaticHeader:      "Authorization",
				StaticPrefix:      "Bearer ",
				OAuthGrantType:    GrantTypeClientCreds,
				OAuthClientID:     "id",
				OAuthClientSecret: "secret",
				OAuthScopes:       []string{"read", "write"},
				OAuthTokenURL:     mustParseURL(t, tokenURL),
				JWTExpiration:     defaultJWTExpiration,
			},
		},
		{
			name: "oauth_explicit_jwt_bearer_urn",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_grant_type":     string(GrantTypeJWTBearer),
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": keyFile,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			want: Config{
				Type:           ConfigTypeOAuth,
				StaticHeader:   "Authorization",
				StaticPrefix:   "Bearer ",
				OAuthGrantType: GrantTypeJWTBearer,
				OAuthTokenURL:  mustParseURL(t, tokenURL),
				JWTClaims:      map[string]string{"iss": "issuer"},
				JWTExpiration:  defaultJWTExpiration,
				JWTAlgorithm:   "ES256",
				JWTSigner:      ecKey,
			},
		},
		{
			name: "oauth_inferred_authorization_code_with_pkce",
			cfg: map[string]any{
				"type":               "oauth",
				"oauth_client_id":    "id",
				"oauth_pkce":         true,
				"oauth_auth_url":     authURL,
				"oauth_callback_url": callbackURL,
				"oauth_token_url":    tokenURL,
			},
			want: Config{
				Type:             ConfigTypeOAuth,
				StaticHeader:     "Authorization",
				StaticPrefix:     "Bearer ",
				OAuthGrantType:   GrantTypeAuthzCode,
				OAuthClientID:    "id",
				OAuthPKCE:        true,
				OAuthAuthURL:     mustParseURL(t, authURL),
				OAuthCallbackURL: mustParseURL(t, callbackURL),
				OAuthTokenURL:    mustParseURL(t, tokenURL),
				JWTExpiration:    defaultJWTExpiration,
			},
		},
		{
			name: "oauth_inferred_refresh_token",
			cfg: map[string]any{
				"type":                "oauth",
				"oauth_client_id":     "id",
				"oauth_refresh_token": "refresh",
				"oauth_token_url":     tokenURL,
			},
			want: Config{
				Type:              ConfigTypeOAuth,
				StaticHeader:      "Authorization",
				StaticPrefix:      "Bearer ",
				OAuthGrantType:    GrantTypeRefreshToken,
				OAuthClientID:     "id",
				OAuthRefreshToken: "refresh",
				OAuthTokenURL:     mustParseURL(t, tokenURL),
				JWTExpiration:     defaultJWTExpiration,
			},
		},
		{
			name: "oauth_inferred_client_credentials",
			cfg: map[string]any{
				"type":                "oauth",
				"oauth_client_id":     "id",
				"oauth_client_secret": "secret",
				"oauth_token_url":     tokenURL,
			},
			want: Config{
				Type:              ConfigTypeOAuth,
				StaticHeader:      "Authorization",
				StaticPrefix:      "Bearer ",
				OAuthGrantType:    GrantTypeClientCreds,
				OAuthClientID:     "id",
				OAuthClientSecret: "secret",
				OAuthTokenURL:     mustParseURL(t, tokenURL),
				JWTExpiration:     defaultJWTExpiration,
			},
		},
		{
			name: "oauth_inferred_jwt_bearer",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": keyFile,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			want: Config{
				Type:           ConfigTypeOAuth,
				StaticHeader:   "Authorization",
				StaticPrefix:   "Bearer ",
				OAuthGrantType: GrantTypeJWTBearer,
				OAuthTokenURL:  mustParseURL(t, tokenURL),
				JWTClaims:      map[string]string{"iss": "issuer"},
				JWTExpiration:  defaultJWTExpiration,
				JWTAlgorithm:   "ES256",
				JWTSigner:      ecKey,
			},
		},
		{
			name: "pkce_without_authorization_code_only_warns",
			cfg:  map[string]any{"type": "none", "oauth_pkce": true},
			want: Config{
				Type:         ConfigTypeNone,
				StaticHeader: "Authorization",
				StaticPrefix: "Bearer ",
				OAuthPKCE:    true,
			},
		},
		{
			name: "oauth_grant_type_ignored_without_oauth_type",
			cfg: map[string]any{
				"type":             "static",
				"static_secret":    "s3cr3t",
				"oauth_grant_type": "jwt_bearer",
			},
			want: Config{
				Type:         ConfigTypeStatic,
				StaticHeader: "Authorization",
				StaticPrefix: "Bearer ",
				StaticSecret: "s3cr3t",
			},
		},
		{
			name: "authorization_code_grant_ignored_without_oauth_type",
			cfg:  map[string]any{"type": "none", "oauth_grant_type": "authorization_code"},
			want: Config{
				Type:         ConfigTypeNone,
				StaticHeader: "Authorization",
				StaticPrefix: "Bearer ",
			},
		},
		{
			name: "unexpected_value_types_use_defaults",
			cfg:  map[string]any{"type": int64(1), "static_header": true, "oauth_pkce": "yes"},
			want: Config{
				Type:         ConfigTypeNone,
				StaticHeader: "Authorization",
				StaticPrefix: "Bearer ",
			},
		},
		{
			name: "jwt_bearer_with_custom_expiration",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_grant_type":     "jwt_bearer",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": " " + keyFile + " ",
				"jwt_expiration":       " 10m ",
				"jwt_claims":           map[string]any{"iss": "issuer", "sub": " not trimmed "},
			},
			want: Config{
				Type:           ConfigTypeOAuth,
				StaticHeader:   "Authorization",
				StaticPrefix:   "Bearer ",
				OAuthGrantType: GrantTypeJWTBearer,
				OAuthTokenURL:  mustParseURL(t, tokenURL),
				JWTClaims:      map[string]string{"iss": "issuer", "sub": " not trimmed "},
				JWTExpiration:  10 * time.Minute,
				JWTAlgorithm:   "ES256",
				JWTSigner:      ecKey,
			},
		},
		{
			name: "jwt_bearer_with_ec_sec1_key",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": ecSEC1File,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			want: Config{
				Type:           ConfigTypeOAuth,
				StaticHeader:   "Authorization",
				StaticPrefix:   "Bearer ",
				OAuthGrantType: GrantTypeJWTBearer,
				OAuthTokenURL:  mustParseURL(t, tokenURL),
				JWTClaims:      map[string]string{"iss": "issuer"},
				JWTExpiration:  defaultJWTExpiration,
				JWTAlgorithm:   "ES256",
				JWTSigner:      ecKey,
			},
		},
		{
			name: "jwt_bearer_with_rsa_pkcs1_key",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": rsaFile,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			want: Config{
				Type:           ConfigTypeOAuth,
				StaticHeader:   "Authorization",
				StaticPrefix:   "Bearer ",
				OAuthGrantType: GrantTypeJWTBearer,
				OAuthTokenURL:  mustParseURL(t, tokenURL),
				JWTClaims:      map[string]string{"iss": "issuer"},
				JWTExpiration:  defaultJWTExpiration,
				JWTAlgorithm:   "RS256",
				JWTSigner:      rsaKey,
			},
		},
		{
			name: "jwt_bearer_with_ed25519_key",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": edFile,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			want: Config{
				Type:           ConfigTypeOAuth,
				StaticHeader:   "Authorization",
				StaticPrefix:   "Bearer ",
				OAuthGrantType: GrantTypeJWTBearer,
				OAuthTokenURL:  mustParseURL(t, tokenURL),
				JWTClaims:      map[string]string{"iss": "issuer"},
				JWTExpiration:  defaultJWTExpiration,
				JWTAlgorithm:   "EdDSA",
				JWTSigner:      edKey,
			},
		},
		{
			name: "jwt_bearer_with_ec_p384_key",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": ecP384File,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			want: Config{
				Type:           ConfigTypeOAuth,
				StaticHeader:   "Authorization",
				StaticPrefix:   "Bearer ",
				OAuthGrantType: GrantTypeJWTBearer,
				OAuthTokenURL:  mustParseURL(t, tokenURL),
				JWTClaims:      map[string]string{"iss": "issuer"},
				JWTExpiration:  defaultJWTExpiration,
				JWTAlgorithm:   "ES384",
				JWTSigner:      ecP384Key,
			},
		},
		{
			name: "jwt_bearer_with_ec_p521_key",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": ecP521File,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			want: Config{
				Type:           ConfigTypeOAuth,
				StaticHeader:   "Authorization",
				StaticPrefix:   "Bearer ",
				OAuthGrantType: GrantTypeJWTBearer,
				OAuthTokenURL:  mustParseURL(t, tokenURL),
				JWTClaims:      map[string]string{"iss": "issuer"},
				JWTExpiration:  defaultJWTExpiration,
				JWTAlgorithm:   "ES512",
				JWTSigner:      ecP521Key,
			},
		},
		{
			name: "jwt_expiration_below_min_is_clamped",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": keyFile,
				"jwt_claims":           map[string]any{"iss": "issuer"},
				"jwt_expiration":       "1s",
			},
			want: Config{
				Type:           ConfigTypeOAuth,
				StaticHeader:   "Authorization",
				StaticPrefix:   "Bearer ",
				OAuthGrantType: GrantTypeJWTBearer,
				OAuthTokenURL:  mustParseURL(t, tokenURL),
				JWTClaims:      map[string]string{"iss": "issuer"},
				JWTExpiration:  time.Minute,
				JWTAlgorithm:   "ES256",
				JWTSigner:      ecKey,
			},
		},
		{
			name: "jwt_expiration_above_max_is_clamped",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": keyFile,
				"jwt_claims":           map[string]any{"iss": "issuer"},
				"jwt_expiration":       "2h",
			},
			want: Config{
				Type:           ConfigTypeOAuth,
				StaticHeader:   "Authorization",
				StaticPrefix:   "Bearer ",
				OAuthGrantType: GrantTypeJWTBearer,
				OAuthTokenURL:  mustParseURL(t, tokenURL),
				JWTClaims:      map[string]string{"iss": "issuer"},
				JWTExpiration:  time.Hour,
				JWTAlgorithm:   "ES256",
				JWTSigner:      ecKey,
			},
		},
		{
			name: "leftover_oauth_settings_ignored_without_oauth_type",
			cfg: map[string]any{
				"type":            "static",
				"static_secret":   "s3cr3t",
				"oauth_token_url": "not-a-url",
				"jwt_expiration":  "5 minutes",
				"jwt_claims":      "iss=issuer",
			},
			want: Config{
				Type:         ConfigTypeStatic,
				StaticHeader: "Authorization",
				StaticPrefix: "Bearer ",
				StaticSecret: "s3cr3t",
			},
		},
		{
			name: "jwt_private_key_file_ignored_without_jwt_bearer",
			cfg:  map[string]any{"jwt_private_key_file": filepath.Join(tempDir, "nonexistent.pem")},
			want: Config{
				Type:         ConfigTypeNone,
				StaticHeader: "Authorization",
				StaticPrefix: "Bearer ",
			},
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
			name:    "invalid_auth_url",
			cfg:     map[string]any{"type": "oauth", "oauth_auth_url": "/relative"},
			wantErr: true,
		},
		{
			name:    "invalid_callback_url",
			cfg:     map[string]any{"type": "oauth", "oauth_callback_url": "ftp://example.com"},
			wantErr: true,
		},
		{
			name:    "invalid_token_url",
			cfg:     map[string]any{"type": "oauth", "oauth_token_url": "https://example.com:99999"}, //gosec:disable G101 // URL.
			wantErr: true,
		},
		{
			name: "oauth_without_client_id",
			cfg: map[string]any{
				"type":             "oauth",
				"oauth_grant_type": "client_credentials",
				"oauth_token_url":  tokenURL,
			},
			wantErr: true,
		},
		{
			name: "authorization_code_without_secret_or_pkce",
			cfg: map[string]any{
				"type":               "oauth",
				"oauth_client_id":    "id",
				"oauth_auth_url":     authURL,
				"oauth_callback_url": callbackURL,
				"oauth_token_url":    tokenURL,
			},
			wantErr: true,
		},
		{
			name: "authorization_code_without_auth_url",
			cfg: map[string]any{
				"type":               "oauth",
				"oauth_client_id":    "id",
				"oauth_pkce":         true,
				"oauth_callback_url": callbackURL,
				"oauth_token_url":    tokenURL,
			},
			wantErr: true,
		},
		{
			name: "authorization_code_without_callback_url",
			cfg: map[string]any{
				"type":            "oauth",
				"oauth_client_id": "id",
				"oauth_pkce":      true,
				"oauth_auth_url":  authURL,
				"oauth_token_url": tokenURL,
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
			cfg:     map[string]any{"type": "oauth", "oauth_client_id": "id", "oauth_token_url": tokenURL},
			wantErr: true,
		},
		{
			name: "refresh_token_grant_without_token",
			cfg: map[string]any{
				"type":             "oauth",
				"oauth_grant_type": "refresh_token",
				"oauth_client_id":  "id",
				"oauth_token_url":  tokenURL,
			},
			wantErr: true,
		},
		{
			name: "invalid_jwt_expiration",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": keyFile,
				"jwt_claims":           map[string]any{"iss": "issuer"},
				"jwt_expiration":       "5 minutes",
			},
			wantErr: true,
		},
		{
			name: "jwt_claims_not_a_table",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": keyFile,
				"jwt_claims":           "iss=issuer",
			},
			wantErr: true,
		},
		{
			name: "jwt_claim_not_a_string",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": keyFile,
				"jwt_claims":           map[string]any{"iss": "issuer", "exp": int64(1)},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_without_private_key_file",
			cfg: map[string]any{
				"type":            "oauth",
				"oauth_token_url": tokenURL,
				"jwt_claims":      map[string]any{"iss": "issuer"},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_without_claims",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": keyFile,
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_empty_claims",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": keyFile,
				"jwt_claims":           map[string]any{},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_nonexistent_private_key_file",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": filepath.Join(tempDir, "nonexistent.pem"),
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_non_pem_private_key_file",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": notPEMFile,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_public_key_file",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
				"jwt_private_key_file": publicKeyFile,
				"jwt_claims":           map[string]any{"iss": "issuer"},
			},
			wantErr: true,
		},
		{
			name: "jwt_bearer_with_unsupported_ec_curve",
			cfg: map[string]any{
				"type":                 "oauth",
				"oauth_token_url":      tokenURL,
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
			if !keysEqual(got.JWTSigner, tt.want.JWTSigner) {
				t.Errorf("ParseConfig() JWT private key = %T, want %T", got.JWTSigner, tt.want.JWTSigner)
			}
			got.JWTSigner, tt.want.JWTSigner = nil, nil
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseConfig() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseOAuthScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  map[string]any
		want []string
	}{
		{name: "missing", cfg: map[string]any{}, want: nil},
		{name: "empty_string", cfg: map[string]any{"oauth_scopes": ""}, want: nil},
		{name: "whitespace_string", cfg: map[string]any{"oauth_scopes": " \t "}, want: nil},
		{name: "whitespace_any_slice", cfg: map[string]any{"oauth_scopes": []any{"", " \t "}}, want: nil},
		{name: "empty_any_slice", cfg: map[string]any{"oauth_scopes": []any{}}, want: nil},
		{name: "unexpected_type", cfg: map[string]any{"oauth_scopes": int64(1)}, want: nil},
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

			if got := parseOAuthScopes(tt.cfg, tt.name); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseOAuthScopes() = %q, want %q", got, tt.want)
			}
		})
	}
}
