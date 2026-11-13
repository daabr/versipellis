package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/daabr/versipellis/pkg/config"
)

type configType string

const (
	configTypeNone   configType = "none"
	configTypeBasic  configType = "basic"
	configTypeStatic configType = "static"
	configTypeOAuth  configType = "oauth"
)

// Config contains all the configuration and state details for enabling
// HTTP collectors and senders to send authenticated client requests.
type Config struct {
	authType configType

	headerName  string
	headerValue string

	grant             grantType
	oauthConfig       *oauth2.Config
	oauthClientCreds  *clientcredentials.Config
	oauthAuthParams   map[string]string
	oauthTokenParams  map[string]string
	oauthRefreshToken string
	oauthPKCE         bool

	jwtTemplate   *jwt.Token
	jwtExpiration time.Duration
	jwtSigningKey any

	oauthState    string
	oauthVerifier string
	oauthTokenSrc *sharedTokenSource
}

// ParseConfig parses the provided "auth" section when initializing a new collector or sender.
func ParseConfig(rawCfg any, name string) (*Config, error) {
	if rawCfg == nil {
		rawCfg = map[string]any{}
	}
	cfg, ok := rawCfg.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("'auth' must be a table of key-value pairs, got %T", rawCfg)
	}

	parsed := &Config{authType: configType(strings.ToLower(optionalString(cfg, "type", string(configTypeNone))))}
	var err error
	switch parsed.authType {
	case "":
		return nil, errors.New("type field cannot be empty")
	case configTypeNone:
		return parsed, nil
	case configTypeBasic:
		parsed.headerName, parsed.headerValue, err = parseBasicHeader(cfg)
	case configTypeStatic, configTypeOAuth:
		parsed.headerName, parsed.headerValue, err = parseStaticHeader(cfg, parsed.authType)
	default:
		return nil, fmt.Errorf("unrecognized type %q", parsed.authType)
	}

	if err != nil {
		return nil, err
	}
	if parsed.authType != configTypeOAuth {
		return parsed, nil
	}
	if err := parsed.parseOAuthConfig(cfg, name); err != nil {
		return nil, err
	}
	return parsed, nil
}

func optionalString(cfg map[string]any, key, defaultValue string) string {
	return strings.TrimSpace(config.Value(cfg, key, defaultValue))
}
