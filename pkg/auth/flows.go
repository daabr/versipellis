package auth

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

// DoInitialOAuthFlow performs the initial OAuth flow based on the configured grant type.
// Afterwards, it clears parsed configuration details and leaves only runtime state management.
// The provided context must be long-running and include an [http.Client] value under the
// [oauth2.HTTPClient] key. Calling this function has no effect for non-OAuth configs.
func (c *Config) DoInitialOAuthFlow(ctx context.Context) error {
	if c == nil || c.authType != configTypeOAuth {
		return nil
	}

	defer func() {
		c.oauthConfig = nil
		c.oauthClientCreds = nil
		c.oauthAuthParams = nil
		c.oauthTokenParams = nil
		c.oauthRefreshToken = ""
		c.jwtTemplate = nil
		c.jwtSigningKey = nil
	}()

	switch c.grant {
	case grantTypeAuthCode:
		return c.oauthAuthCodeFlow(ctx)
	case grantTypeClientCreds:
		c.oauthTokenSrc = newSharedTokenSource(c.oauthClientCreds.TokenSource(ctx), nil)
	case grantTypeRefreshToken:
		src := c.oauthConfig.TokenSource(ctx, &oauth2.Token{RefreshToken: c.oauthRefreshToken})
		c.oauthTokenSrc = newSharedTokenSource(src, nil)
	case grantTypeJWTBearer:
		return c.oauthJWTBearerFlow(ctx)
	default:
		return fmt.Errorf("unhandled OAuth grant type %q", c.grant)
	}

	if _, err := c.oauthTokenSrc.Token(ctx); err != nil {
		c.oauthTokenSrc = nil
		return fmt.Errorf("OAuth %q flow failed: %w", c.grant, err)
	}
	return nil
}

func (c *Config) oauthAuthCodeURL() string {
	if c.oauthConfig == nil {
		return ""
	}
	c.oauthState = oauth2.GenerateVerifier()
	return c.oauthConfig.AuthCodeURL(c.oauthState, c.oauthAuthCodeOpts()...)
}

// May include [oauth2.AccessTypeOnline] or [oauth2.AccessTypeOffline],
// [oauth2.ApprovalForce], [oauth2.S256ChallengeOption], etc.
func (c *Config) oauthAuthCodeOpts() []oauth2.AuthCodeOption {
	var opts []oauth2.AuthCodeOption
	for k, v := range c.oauthAuthParams {
		opts = append(opts, oauth2.SetAuthURLParam(k, v))
	}
	if c.oauthPKCE {
		c.oauthVerifier = oauth2.GenerateVerifier()
		opts = append(opts, oauth2.S256ChallengeOption(c.oauthVerifier))
	}
	return opts
}

// OAuthExchangeValues returns the current OAuth state parameter and PKCE
// verifier, which are checked during the OAuth authorization code exchange.
func (c *Config) OAuthExchangeValues() (state, verifier string) {
	return c.oauthState, c.oauthVerifier
}

func (c *Config) oauthAuthCodeFlow(_ context.Context) error {
	// This will be implemented in a separate PR, to simplify the review.
	return errors.New("OAuth authorization code flow not yet implemented")
}

func (c *Config) oauthJWTBearerFlow(ctx context.Context) error {
	src := &jwtTokenSource{
		ctx:      ctx,
		tokenURL: c.oauthConfig.Endpoint.TokenURL,
		claims:   maps.Clone(map[string]any(c.jwtTemplate.Claims.(jwt.MapClaims))), //nolint:errcheck // OK by definition.
		method:   c.jwtTemplate.Method,
		exp:      c.jwtExpiration,
		key:      c.jwtSigningKey,
	}
	token, err := src.Token()
	if err != nil {
		return err
	}

	c.oauthTokenSrc = newSharedTokenSource(oauth2.ReuseTokenSource(token, src), token)
	return nil
}
