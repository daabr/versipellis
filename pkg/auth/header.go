package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/textproto"
	"strings"

	"golang.org/x/net/http/httpguts"

	"github.com/daabr/versipellis/pkg/config"
)

const (
	defaultHeaderName   = "Authorization"
	defaultStaticPrefix = "Bearer"
)

func parseBasicHeader(cfg map[string]any) (name, value string, err error) {
	username := optionalString(cfg, "basic_username", "")
	password := config.Value(cfg, "basic_password", "")
	if username == "" || password == "" {
		return "", "", errors.New("both username and password are required in basic mode")
	}
	if strings.Contains(username, ":") {
		return "", "", errors.New("basic-mode username must not contain a ':' character")
	}

	// Effectively the same logic as in [http.Request.SetBasicAuth].
	value = "Basic " + base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%s:%s", username, password))
	return defaultHeaderName, value, nil
}

func parseStaticHeader(cfg map[string]any, t configType) (name, value string, err error) {
	name = textproto.CanonicalMIMEHeaderKey(optionalString(cfg, "static_header", defaultHeaderName))
	value = optionalString(cfg, "static_prefix", defaultStaticPrefix)
	switch {
	case !httpguts.ValidHeaderFieldName(name):
		return "", "", fmt.Errorf("invalid HTTP header name %q in 'static_header'", name)
	case !httpguts.ValidHeaderFieldValue(value):
		return "", "", errors.New("'static_prefix' must not contain control characters")
	case value != "":
		value += " "
	}
	if t != configTypeStatic {
		return name, value, nil
	}

	secret := config.Value(cfg, "static_secret", "")
	if secret == "" {
		return "", "", errors.New("secret key/token is required in static mode")
	}
	if !httpguts.ValidHeaderFieldValue(secret) {
		return "", "", errors.New("static-mode secret must not contain control characters")
	}
	value += secret
	return name, value, nil
}

// HeaderName returns the name of the HTTP header that [Config.SetHeader] sets, or an empty string if it sets none.
func (c *Config) HeaderName() string {
	if c == nil {
		return ""
	}
	return c.headerName
}

// SetHeader clones the provided HTTP headers and injects the appropriate (static or dynamic)
// authentication header before sending an HTTP request. The context is the request's: if it's
// canceled while waiting for a new OAuth token, this function stops waiting (but the token request
// continues in the background). Callers should retry the request when this function returns an error.
func (c *Config) SetHeader(ctx context.Context, h http.Header, name string) (http.Header, error) {
	if h = h.Clone(); h == nil {
		h = http.Header{}
	}

	switch {
	case c == nil, c.authType == configTypeNone:
		// Do nothing.
	case c.authType == configTypeBasic || c.authType == configTypeStatic:
		h.Set(c.headerName, c.headerValue)
	case c.oauthTokenSrc == nil:
		// Log this but don't return a (retryable) error in this case, let the (unauthenticated) request trigger a
		// 401 response, because this is a critical internal error that should not happen under normal circumstances.
		slog.Error("missing OAuth token source", slog.String("name", name))
	default:
		t, err := c.oauthTokenSrc.Token(ctx)
		if err != nil && ctx.Err() != nil {
			// The request timed out or was aborted, but not necessarily because of the token request.
			slog.Warn("stopped waiting for OAuth token", slog.Any("error", ctx.Err()), slog.String("name", name))
			return nil, fmt.Errorf("stopped waiting for OAuth token: %w", ctx.Err())
		}
		if err != nil {
			// This is a sporadic (but critical) IdP error that can happen only when a request to an OAuth token
			// URL fails, so we should log every instance of it, and signal the caller to retry the request.
			slog.Error("failed to obtain OAuth token", slog.Any("error", err), slog.String("name", name))
			return nil, fmt.Errorf("failed to obtain OAuth token: %w", err)
		}
		h.Set(c.headerName, c.headerValue+t.AccessToken)
	}

	return h, nil
}
