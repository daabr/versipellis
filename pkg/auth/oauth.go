package auth

import (
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/daabr/versipellis/pkg/config"
)

type grantType string

const (
	grantTypeAuthCode     grantType = "authorization_code"
	grantTypeClientCreds  grantType = "client_credentials"
	grantTypeRefreshToken grantType = "refresh_token"
	grantTypeJWTAlias     grantType = "jwt_bearer"
	grantTypeJWTBearer    grantType = "urn:ietf:params:oauth:grant-type:jwt-bearer" //gosec:disable G101 // ID, not cred.
)

var validGrantTypes = []grantType{
	"", // Set dynamic default value based on other OAuth settings.
	grantTypeAuthCode,
	grantTypeClientCreds,
	grantTypeRefreshToken,
	grantTypeJWTBearer,
	// GrantTypeJWTAlias, // Already normalized to [grantTypeJWTBearer].
}

var (
	// Parameters that "oauth_auth_params" cannot override, because they're set by
	// other config fields, or auto-generated (e.g., "state" for CSRF protection).
	reservedAuthParams = []string{
		"response_type", "client_id", "redirect_uri", "scope",
		"state", "code_challenge", "code_challenge_method",
	}

	// Parameters that "oauth_token_params" cannot override, because they're set by other config fields.
	// Note that "grant_type" isn't reserved, for interoperability with non-compliant servers.
	reservedTokenParams = []string{"client_id", "client_secret", "scope"}
)

func (c *Config) parseOAuthConfig(cfg map[string]any, name string) error {
	c.grant = grantType(strings.ToLower(optionalString(cfg, "oauth_grant_type", "")))

	c.oauthConfig = &oauth2.Config{
		ClientID:     optionalString(cfg, "oauth_client_id", ""),
		ClientSecret: config.Value(cfg, "oauth_client_secret", ""),
		Endpoint: oauth2.Endpoint{
			AuthURL:  optionalString(cfg, "oauth_auth_url", ""),
			TokenURL: optionalString(cfg, "oauth_token_url", ""),
		},
		RedirectURL: optionalString(cfg, "oauth_callback_url", ""),
	}
	c.oauthPKCE = config.Value(cfg, "oauth_pkce", false)
	c.oauthRefreshToken = config.Value(cfg, "oauth_refresh_token", "")

	switch {
	case c.grant == grantTypeJWTAlias:
		c.grant = grantTypeJWTBearer // Normalize value for simplicity.
	case !slices.Contains(validGrantTypes, c.grant):
		return fmt.Errorf("unrecognized OAuth grant type %q", c.grant)
	case c.grant != "":
		// No need to determine the grant type, it's already set.
	case c.oauthConfig.Endpoint.AuthURL != "" || c.oauthConfig.RedirectURL != "":
		c.grant = grantTypeAuthCode
	case c.oauthRefreshToken != "":
		c.grant = grantTypeRefreshToken
	case c.oauthConfig.ClientID != "":
		c.grant = grantTypeClientCreds
	default:
		c.grant = grantTypeJWTBearer
	}

	var err error
	if c.oauthConfig.Scopes, err = parseOAuthScopes(cfg, name); err != nil {
		return err
	}
	if c.oauthAuthParams, err = parseOAuthParams(cfg, "oauth_auth_params", reservedAuthParams); err != nil {
		return err
	}
	if c.oauthTokenParams, err = parseOAuthParams(cfg, "oauth_token_params", reservedTokenParams); err != nil {
		return err
	}
	if err := c.checkOAuthConfig(name); err != nil {
		return err
	}

	switch c.grant {
	case grantTypeAuthCode, grantTypeRefreshToken:
		// No additional configuration needed.

	case grantTypeClientCreds:
		params := make(url.Values, len(c.oauthTokenParams))
		for k, v := range c.oauthTokenParams {
			params.Set(k, v)
		}
		c.oauthClientCreds = &clientcredentials.Config{
			ClientID:       c.oauthConfig.ClientID,
			ClientSecret:   c.oauthConfig.ClientSecret,
			TokenURL:       c.oauthConfig.Endpoint.TokenURL,
			Scopes:         c.oauthConfig.Scopes,
			EndpointParams: params,
		}
		c.oauthConfig = nil

	case grantTypeJWTBearer:
		return c.parseJWTConfig(cfg, name)
	}

	return nil
}

func (c *Config) checkOAuthConfig(name string) error {
	// Required fields for different OAuth grant types.
	switch {
	case c.grant != grantTypeJWTBearer && c.oauthConfig.ClientID == "":
		return fmt.Errorf("OAuth client ID is required for grant type %q", c.grant)
	case c.grant == grantTypeAuthCode && !c.oauthPKCE && c.oauthConfig.ClientSecret == "":
		return fmt.Errorf("OAuth client secret is required for non-PKCE %q grant type", c.grant)
	case c.grant == grantTypeClientCreds && c.oauthConfig.ClientSecret == "":
		return fmt.Errorf("OAuth client secret is required for grant type %q", c.grant)
	case c.grant == grantTypeAuthCode && c.oauthConfig.Endpoint.AuthURL == "":
		return fmt.Errorf("OAuth auth URL is required for grant type %q", c.grant)
	case c.grant == grantTypeAuthCode && c.oauthConfig.RedirectURL == "":
		return fmt.Errorf("OAuth callback URL is required for grant type %q", c.grant)
	case c.oauthConfig.Endpoint.TokenURL == "":
		return fmt.Errorf("OAuth token URL is required for grant type %q", c.grant)
	case c.grant == grantTypeRefreshToken && c.oauthRefreshToken == "":
		return fmt.Errorf("OAuth refresh token is required for grant type %q", c.grant)
	case c.grant != grantTypeAuthCode && c.oauthPKCE:
		slog.Warn("PKCE is only supported by the OAuth grant type 'authorization_code'", slog.String("name", name))
	}
	if c.grant != grantTypeAuthCode && len(c.oauthAuthParams) > 0 {
		slog.Warn("'oauth_auth_params' is only supported by the OAuth grant type 'authorization_code'",
			slog.String("name", name),
		)
	}
	if c.grant != grantTypeClientCreds && len(c.oauthTokenParams) > 0 {
		slog.Warn("'oauth_token_params' is only supported by the OAuth grant type 'client_credentials'",
			slog.String("name", name),
		)
	}

	// OAuth URLs format validation (already checked above for required fields).
	if err := checkOAuthURL(c.oauthConfig.Endpoint.AuthURL, "auth"); err != nil {
		return err
	}
	if err := checkOAuthURL(c.oauthConfig.RedirectURL, "callback"); err != nil {
		return err
	}
	return checkOAuthURL(c.oauthConfig.Endpoint.TokenURL, "token")
}

// checkOAuthURL validates an optional OAuth URL, which must use TLS (RFC 6749, sections 3.1, 3.1.2.1, and 3.2),
// because OAuth requests and responses contain credentials and tokens. Loopback hosts are exempt, because their
// traffic never leaves the host (e.g., callbacks to native apps, per RFC 8252, section 7.3, and local test servers).
func checkOAuthURL(rawURL, desc string) error {
	u, err := config.ParseURL(rawURL, "", false)
	switch {
	case err != nil:
		return fmt.Errorf("invalid OAuth %s URL: %w", desc, err)
	case u == nil || u.Scheme == config.URLSchemeHTTPS || isLoopback(u.Hostname()):
		return nil
	default:
		return fmt.Errorf("invalid OAuth %s URL: must have an HTTPS scheme, unless the host is loopback", desc)
	}
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func parseOAuthScopes(cfg map[string]any, name string) ([]string, error) {
	anyValue, found := cfg["oauth_scopes"]
	if !found {
		return nil, nil
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
		return nil, fmt.Errorf("'oauth_scopes' must be a string or an array of strings, got %T", anyValue)
	}

	return scopes, nil
}

// parseOAuthParams parses an optional TOML table of additional OAuth request parameters, whose values must be strings.
// Parameter names are case sensitive, and reserved names are rejected. Names must not contain whitespace or control
// characters. Values must not contain control characters either, but they may contain spaces, because some standard
// parameters are space-delimited lists (e.g., "prompt", "acr_values", and "ui_locales" in OpenID Connect).
func parseOAuthParams(cfg map[string]any, key string, reserved []string) (map[string]string, error) {
	anyValue, found := cfg[key]
	if !found {
		return nil, nil
	}
	table, ok := anyValue.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q must be a table of key-value pairs, got %T", key, anyValue)
	}

	params := make(map[string]string, len(table))
	for k, v := range table {
		s, ok := v.(string)
		switch {
		case k == "":
			return nil, fmt.Errorf("%q must not contain empty parameter names", key)
		case strings.ContainsFunc(k, isSpaceOrControl):
			return nil, fmt.Errorf("%q parameter name %q must not contain whitespace or control characters", key, k)
		case slices.Contains(reserved, k):
			return nil, fmt.Errorf("%q must not contain the reserved parameter %q", key, k)
		case !ok:
			return nil, fmt.Errorf("%q parameter %q must be a string, got %T", key, k, v)
		case strings.ContainsFunc(s, unicode.IsControl):
			return nil, fmt.Errorf("%q parameter %q must not contain control characters", key, k)
		}
		params[k] = s
	}

	return params, nil
}

func isSpaceOrControl(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r)
}
