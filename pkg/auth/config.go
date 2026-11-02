// Package auth provides configurable HTTP authentication and authorization mechanisms.
package auth

import (
	"crypto"
	"crypto/elliptic"
	"errors"
	"fmt"
	"log/slog"
	"net/textproto"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/net/http/httpguts"

	"github.com/daabr/versipellis/pkg/config"
)

type (
	// ConfigType is a shared Go type of all the ConfigType* constants in this package.
	ConfigType string
	// GrantType is a shared Go type of all the GrantType* constants in this package.
	GrantType string
)

// ConfigType* constants represent all the available configuration modes of "auth" sections in the TOML file.
const (
	ConfigTypeNone   ConfigType = "none"
	ConfigTypeBasic  ConfigType = "basic"
	ConfigTypeStatic ConfigType = "static"
	ConfigTypeOAuth  ConfigType = "oauth"
)

// GrantType* constants represent all the available OAuth grant/flow types for "auth" sections in the TOML file.
const (
	GrantTypeAuthzCode    GrantType = "authorization_code"
	GrantTypeClientCreds  GrantType = "client_credentials"
	GrantTypeRefreshToken GrantType = "refresh_token"
	GrantTypeJWTAlias     GrantType = "jwt_bearer"
	GrantTypeJWTBearer    GrantType = "urn:ietf:params:oauth:grant-type:jwt-bearer" //gosec:disable G101 // ID, not cred.
)

const (
	defaultConfigType   = string(ConfigTypeNone)
	defaultStaticHeader = "Authorization"
	defaultStaticPrefix = "Bearer"

	defaultJWTExpiration = 5 * time.Minute
	minJWTExpiration     = time.Minute
	maxJWTExpiration     = time.Hour
)

var (
	validConfigTypes = []ConfigType{
		ConfigTypeNone,
		ConfigTypeBasic,
		ConfigTypeStatic,
		ConfigTypeOAuth,
	}

	validGrantTypes = []GrantType{
		GrantTypeAuthzCode,
		GrantTypeClientCreds,
		GrantTypeRefreshToken,
		GrantTypeJWTBearer,
	}
)

// Config contains all the configuration and state details for enabling
// HTTP collectors and senders to send authenticated client requests.
type Config struct {
	Type ConfigType

	BasicUsername string
	BasicPassword string

	StaticHeader string
	StaticPrefix string
	StaticSecret string

	OAuthGrantType    GrantType
	OAuthClientID     string
	OAuthClientSecret string
	OAuthRefreshToken string
	OAuthPKCE         bool

	OAuthScopes []string

	OAuthAuthURL     *url.URL
	OAuthCallbackURL *url.URL
	OAuthTokenURL    *url.URL

	JWTClaims     map[string]string
	JWTExpiration time.Duration
	JWTAlgorithm  string
	JWTSigner     crypto.Signer
}

// ParseConfig parses the provided "auth" section when initializing a new collector or sender.
func ParseConfig(rawCfg any, name string) (Config, error) {
	if rawCfg == nil {
		rawCfg = map[string]any{}
	}
	cfg, ok := rawCfg.(map[string]any)
	if !ok {
		return Config{}, fmt.Errorf("%q must be a table of key-value pairs, got %T", "auth", rawCfg)
	}

	parsed := Config{
		Type: ConfigType(strings.ToLower(optionalString(cfg, "type", defaultConfigType))),

		BasicUsername: optionalString(cfg, "basic_username", ""),
		BasicPassword: config.Value(cfg, "basic_password", ""),

		StaticHeader: textproto.CanonicalMIMEHeaderKey(optionalString(cfg, "static_header", defaultStaticHeader)),
		StaticPrefix: optionalString(cfg, "static_prefix", defaultStaticPrefix),
		StaticSecret: config.Value(cfg, "static_secret", ""),

		OAuthGrantType:    GrantType(strings.ToLower(optionalString(cfg, "oauth_grant_type", ""))),
		OAuthClientID:     optionalString(cfg, "oauth_client_id", ""),
		OAuthClientSecret: config.Value(cfg, "oauth_client_secret", ""),
		OAuthRefreshToken: config.Value(cfg, "oauth_refresh_token", ""),
		OAuthPKCE:         config.Value(cfg, "oauth_pkce", false),
		OAuthScopes:       parseOAuthScopes(cfg, name),
	}
	if parsed.StaticPrefix != "" {
		parsed.StaticPrefix += " "
	}
	if parsed.OAuthGrantType == GrantTypeJWTAlias {
		parsed.OAuthGrantType = GrantTypeJWTBearer // Logic simplification.
	}
	if parsed.Type != ConfigTypeOAuth {
		parsed.OAuthGrantType = "" // Ignore leftover OAuth settings in other modes.
	}

	switch {
	case parsed.Type == "":
		return Config{}, errors.New("type field is required but not found")
	case !slices.Contains(validConfigTypes, parsed.Type):
		return Config{}, fmt.Errorf("unrecognized type %q", parsed.Type)
	case parsed.Type == ConfigTypeBasic && (parsed.BasicUsername == "" || parsed.BasicPassword == ""):
		return Config{}, errors.New("both username and password are required in basic mode")
	case parsed.Type == ConfigTypeBasic && strings.Contains(parsed.BasicUsername, ":"):
		return Config{}, errors.New("basic-mode username must not contain a ':' character")
	case parsed.Type == ConfigTypeStatic && parsed.StaticSecret == "":
		return Config{}, errors.New("secret key/token is required in static mode")
	case parsed.Type == ConfigTypeStatic && !httpguts.ValidHeaderFieldName(parsed.StaticHeader):
		return Config{}, fmt.Errorf("invalid HTTP header name %q in static mode", parsed.StaticHeader)
	case parsed.Type == ConfigTypeStatic && !httpguts.ValidHeaderFieldValue(parsed.StaticPrefix+parsed.StaticSecret):
		return Config{}, errors.New("static-mode prefix and secret must not contain control characters")
	case parsed.OAuthGrantType != "" && !slices.Contains(validGrantTypes, parsed.OAuthGrantType):
		return Config{}, fmt.Errorf("unrecognized OAuth grant type %q", parsed.OAuthGrantType)
	}

	var err error
	if parsed.Type == ConfigTypeOAuth {
		rawURL := config.Value(cfg, "oauth_auth_url", "")
		if parsed.OAuthAuthURL, err = config.ParseURL(rawURL, "", false); err != nil {
			return Config{}, fmt.Errorf("invalid OAuth auth URL: %w", err)
		}
		rawURL = config.Value(cfg, "oauth_callback_url", "")
		if parsed.OAuthCallbackURL, err = config.ParseURL(rawURL, "", false); err != nil {
			return Config{}, fmt.Errorf("invalid OAuth callback URL: %w", err)
		}
		rawURL = config.Value(cfg, "oauth_token_url", "")
		if parsed.OAuthTokenURL, err = config.ParseURL(rawURL, "", false); err != nil {
			return Config{}, fmt.Errorf("invalid OAuth token URL: %w", err)
		}
		if parsed.JWTExpiration, err = parseJWTExpiration(cfg, name); err != nil {
			return Config{}, err
		}
		if parsed.JWTClaims, err = parseJWTClaims(cfg); err != nil {
			return Config{}, err
		}
	}

	if parsed.Type == ConfigTypeOAuth && parsed.OAuthGrantType == "" {
		switch {
		case parsed.OAuthAuthURL != nil || parsed.OAuthCallbackURL != nil:
			parsed.OAuthGrantType = GrantTypeAuthzCode
		case parsed.OAuthRefreshToken != "":
			parsed.OAuthGrantType = GrantTypeRefreshToken
		case parsed.OAuthClientID != "":
			parsed.OAuthGrantType = GrantTypeClientCreds
		default:
			parsed.OAuthGrantType = GrantTypeJWTBearer
		}
	}

	if parsed.OAuthGrantType == GrantTypeJWTBearer {
		data, err := config.LoadPEMFile(config.Value(cfg, "jwt_private_key_file", ""), "JWT private key", true)
		if err != nil {
			return Config{}, err //nolint:wrapcheck // No need to contextualize a sibling error.
		}
		if parsed.JWTSigner, parsed.JWTAlgorithm, err = parseJWTPrivateKey(data); err != nil {
			return Config{}, err
		}
	}

	switch {
	case parsed.Type == ConfigTypeOAuth && parsed.OAuthGrantType != GrantTypeJWTBearer && parsed.OAuthClientID == "":
		return Config{}, fmt.Errorf("OAuth client ID is required for grant type %q", parsed.OAuthGrantType)
	case parsed.OAuthGrantType == GrantTypeAuthzCode && !parsed.OAuthPKCE && parsed.OAuthClientSecret == "":
		return Config{}, fmt.Errorf("OAuth client secret is required for non-PKCE %q grant type", GrantTypeAuthzCode)
	case parsed.OAuthGrantType == GrantTypeClientCreds && parsed.OAuthClientSecret == "":
		return Config{}, fmt.Errorf("OAuth client secret is required for grant type %q", GrantTypeClientCreds)
	case parsed.OAuthGrantType == GrantTypeAuthzCode && parsed.OAuthAuthURL == nil:
		return Config{}, fmt.Errorf("OAuth auth URL is required for grant type %q", GrantTypeAuthzCode)
	case parsed.OAuthGrantType == GrantTypeAuthzCode && parsed.OAuthCallbackURL == nil:
		return Config{}, fmt.Errorf("OAuth callback URL is required for grant type %q", GrantTypeAuthzCode)
	case parsed.Type == ConfigTypeOAuth && parsed.OAuthTokenURL == nil:
		return Config{}, fmt.Errorf("OAuth token URL is required for grant type %q", parsed.OAuthGrantType)
	case parsed.OAuthGrantType == GrantTypeRefreshToken && parsed.OAuthRefreshToken == "":
		return Config{}, fmt.Errorf("OAuth refresh token is required for grant type %q", GrantTypeRefreshToken)
	case parsed.OAuthGrantType == GrantTypeJWTBearer && len(parsed.JWTClaims) == 0:
		return Config{}, fmt.Errorf("JWT claims are required for OAuth grant type %q", GrantTypeJWTBearer)
	}

	if (parsed.Type != ConfigTypeOAuth || parsed.OAuthGrantType != GrantTypeAuthzCode) && parsed.OAuthPKCE {
		slog.Warn(`PKCE is only supported by the OAuth grant type "authorization_code"`, slog.String("name", name))
	}

	return parsed, nil
}

func optionalString(cfg map[string]any, key, defaultValue string) string {
	return strings.TrimSpace(config.Value(cfg, key, defaultValue))
}

func parseOAuthScopes(cfg map[string]any, name string) []string {
	anyValue, found := cfg["oauth_scopes"]
	if !found {
		return nil
	}

	var scopes []string
	switch typedValue := anyValue.(type) {
	case string:
		scopes = append(scopes, strings.Fields(typedValue)...)
	case []any:
		for _, v := range typedValue {
			if s, ok := v.(string); ok {
				scopes = append(scopes, strings.Fields(s)...)
				continue
			}
			slog.Warn("skipping non-string value in OAuth scopes array",
				slog.String("name", name), slog.String("type", fmt.Sprintf("%T", v)),
			)
		}
	default:
		slog.Warn("TOML config field has an unexpected type, using default value", slog.String("name", name),
			slog.String("field", "oauth_scopes"), slog.Any("default", scopes),
			slog.String("type", fmt.Sprintf("%T", anyValue)),
		)
	}

	return scopes
}

func parseJWTExpiration(cfg map[string]any, name string) (time.Duration, error) {
	d, err := time.ParseDuration(optionalString(cfg, "jwt_expiration", defaultJWTExpiration.String()))

	switch {
	case err != nil:
		return 0, fmt.Errorf("invalid JWT expiration duration: %w", err)
	case d < minJWTExpiration:
		slog.Warn("clamping JWT expiration to lower bound", slog.String("name", name),
			slog.Duration("below_min", d), slog.Duration("new_value", minJWTExpiration),
		)
		d = minJWTExpiration
	case d > maxJWTExpiration:
		slog.Warn("clamping JWT expiration to upper bound", slog.String("name", name),
			slog.Duration("above_max", d), slog.Duration("new_value", maxJWTExpiration),
		)
		d = maxJWTExpiration
	}

	return d, nil
}

func parseJWTClaims(cfg map[string]any) (map[string]string, error) {
	anyValue, found := cfg["jwt_claims"]
	if !found {
		return nil, nil
	}

	table, ok := anyValue.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q must be a table of key-value pairs, got %T", "jwt_claims", anyValue)
	}
	if len(table) == 0 {
		return nil, nil
	}

	claims := make(map[string]string, len(table))
	for k, v := range table {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("JWT claim %q must be a string, got %T", k, v)
		}
		claims[k] = s
	}

	return claims, nil
}

// parseJWTPrivateKey supports RSA (PKCS #1 or #8), ECDSA (SEC 1 or PKCS #8), and Ed25519 (PKCS #8). It also
// returns the JWT signing algorithm that matches the key, because ECDSA algorithms are bound to specific curves.
func parseJWTPrivateKey(data []byte) (crypto.Signer, string, error) {
	if key, err := jwt.ParseRSAPrivateKeyFromPEM(data); err == nil {
		return key, jwt.SigningMethodRS256.Alg(), nil
	}

	if key, err := jwt.ParseECPrivateKeyFromPEM(data); err == nil {
		switch key.Curve {
		case elliptic.P256():
			return key, jwt.SigningMethodES256.Alg(), nil
		case elliptic.P384():
			return key, jwt.SigningMethodES384.Alg(), nil
		case elliptic.P521():
			return key, jwt.SigningMethodES512.Alg(), nil
		default:
			return nil, "", fmt.Errorf("unsupported ECDSA curve %q in JWT private key", key.Curve.Params().Name)
		}
	}

	if key, err := jwt.ParseEdPrivateKeyFromPEM(data); err == nil {
		if signer, ok := key.(crypto.Signer); ok {
			return signer, jwt.SigningMethodEdDSA.Alg(), nil
		}
	}

	return nil, "", errors.New("unsupported JWT private key type")
}
