package auth

import (
	"crypto/ed25519"
	"crypto/elliptic"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pelletier/go-toml/v2"

	"github.com/daabr/versipellis/pkg/config"
)

const (
	defaultJWTExpiration = 5 * time.Minute
	minJWTExpiration     = time.Minute
	maxJWTExpiration     = time.Hour
)

var (
	// Registered JWT claims whose values must be [jwt.NumericDate]s.
	// See: https://datatracker.ietf.org/doc/html/rfc7519#section-4.1
	numericDateClaims = []string{"exp", "nbf", "iat"}

	dateTimeLayouts = []string{
		time.RFC3339,
		"2006-01-02T15:04:05",       // [time.RFC3339] without timezone offset.
		"2006-01-02 15:04:05Z07:00", // [time.RFC3339] with a space instead of a 'T'.
		time.DateTime,
		time.DateOnly,
	}
)

func (c *Config) parseJWTConfig(cfg map[string]any, name string) error {
	claims, err := parseJWTClaims(cfg)
	if err != nil {
		return err
	}
	c.jwtExpiration, err = parseJWTExpiration(cfg, name)
	if err != nil {
		return err
	}

	data, err := config.LoadPEMFile(config.Value(cfg, "jwt_private_key_file", ""), "JWT private key", true)
	if err != nil {
		return err //nolint:wrapcheck // No need to contextualize a sibling error.
	}
	var method jwt.SigningMethod
	c.jwtSigningKey, method, err = parseJWTPrivateKey(data)
	if err != nil {
		return err
	}

	c.jwtTemplate = jwt.NewWithClaims(method, claims)
	return c.checkJWTClaims(name)
}

// ParseJWTClaims parses the "jwt_claims" TOML table, if found, into a [jwt.MapClaims] object.
// [jwt.MapClaims] recognizes only float64 and [json.Number] values as numeric dates, so:
//   - TOML integers are decoded as int64 and converted losslessly to [json.Number].
//   - TOML floats are decoded as float64, but non-finite values are rejected.
//   - TOML offset date-times, local date-times, and local dates are decoded as [time.Time],
//     [toml.LocalDateTime], and [toml.LocalDate], respectively, and converted to Unix timestamps.
//   - TOML strings are left as-is, unless they're in numeric date claims: then they must contain
//     a decimal number or a date-time, which are converted in the same way as described above.
func parseJWTClaims(cfg map[string]any) (jwt.MapClaims, error) {
	anyValue, found := cfg["jwt_claims"]
	if !found {
		return nil, nil
	}
	table, ok := anyValue.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("'jwt_claims' must be a table of key-value pairs, got %T", anyValue)
	}

	table = maps.Clone(table)
	for k, v := range table {
		switch n := v.(type) {
		case int64:
			table[k] = json.Number(strconv.FormatInt(n, 10))
		case float64:
			if math.IsNaN(n) || math.IsInf(n, 0) {
				return nil, fmt.Errorf("JWT claim %q must be a finite number, got %v", k, n)
			}
		case time.Time:
			table[k] = numericDate(n)
		case toml.LocalDateTime:
			table[k] = numericDate(n.AsTime(hostTimeZone()))
		case toml.LocalDate:
			table[k] = numericDate(n.AsTime(hostTimeZone()))
		case string:
			if !slices.Contains(numericDateClaims, k) {
				continue
			}
			d, err := parseNumericDate(n)
			if err != nil {
				return nil, fmt.Errorf("JWT claim %q must be a number or a date-time: %w", k, err)
			}
			table[k] = d
		}

		// Anything else (e.g., TOML local times without a date) can't be a numeric date.
		if slices.Contains(numericDateClaims, k) {
			switch table[k].(type) {
			case float64, json.Number:
			default:
				return nil, fmt.Errorf("JWT claim %q must be a number or a date-time, got %T", k, v)
			}
		}
	}

	return table, nil
}

// hostTimeZone is the time zone of TOML local date-times and dates, which have no time zone offset of their own.
// The TOML spec leaves their conversion to an instant up to the application, and the host's (configurable with
// the TZ environment variable) is the least surprising choice for a config file that's written for that host.
func hostTimeZone() *time.Location {
	return time.Local //nolint:gosmopolitan // Intentional, see above.
}

// See: https://datatracker.ietf.org/doc/html/rfc7519#section-2
func numericDate(t time.Time) json.Number {
	return json.Number(strconv.FormatInt(t.Unix(), 10))
}

// parseNumericDate parses a quoted numeric date claim, which is either a decimal number of seconds
// since the Unix epoch (an integer, converted losslessly to [json.Number], or a finite float),
// or a date-time in one of the formats in [dateTimeLayouts] (converted to a Unix timestamp).
func parseNumericDate(s string) (any, error) {
	s = strings.TrimSpace(s)
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return json.Number(strconv.FormatInt(i, 10)), nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("non-finite number %q", s)
		}
		return f, nil
	}

	for _, layout := range dateTimeLayouts {
		if t, err := time.ParseInLocation(layout, s, hostTimeZone()); err == nil {
			return numericDate(t), nil
		}
	}
	return nil, fmt.Errorf("unrecognized date-time format %q", s)
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

func (c *Config) checkJWTClaims(name string) error {
	mc := c.jwtTemplate.Claims.(jwt.MapClaims) //nolint:errcheck // OK by definition.
	if _, ok := mc["exp"]; ok {
		slog.Warn("JWT bearer claim 'exp' (expiration time) is configured but ignored", slog.String("name", name))
		delete(mc, "exp")
	}
	if _, ok := mc["iat"]; ok {
		slog.Warn("JWT bearer claim 'iat' (issued at) is configured but ignored", slog.String("name", name))
		delete(mc, "iat")
	}
	if len(mc) == 0 {
		return fmt.Errorf("JWT claims are required for OAuth grant type %q", c.grant)
	}
	if _, ok := mc["scope"]; !ok && len(c.oauthConfig.Scopes) > 0 {
		mc["scope"] = strings.Join(c.oauthConfig.Scopes, " ")
	}

	c.jwtTemplate.Claims = mc
	return nil
}

// parseJWTPrivateKey supports RSA (PKCS #1 or #8), ECDSA (SEC 1 or PKCS #8), and Ed25519 (PKCS #8). It also
// returns the JWT signing algorithm that matches the key, because ECDSA algorithms are bound to specific
// curves. See: https://golang-jwt.github.io/jwt/usage/signing_methods/#signing-methods-and-key-types
func parseJWTPrivateKey(data []byte) (any, jwt.SigningMethod, error) {
	if key, err := jwt.ParseRSAPrivateKeyFromPEM(data); err == nil {
		return key, jwt.SigningMethodRS256, nil
	}

	if key, err := jwt.ParseECPrivateKeyFromPEM(data); err == nil {
		switch key.Curve {
		case elliptic.P256():
			return key, jwt.SigningMethodES256, nil
		case elliptic.P384():
			return key, jwt.SigningMethodES384, nil
		case elliptic.P521():
			return key, jwt.SigningMethodES512, nil
		default:
			return nil, nil, fmt.Errorf("unsupported ECDSA curve %q in JWT private key", key.Curve.Params().Name)
		}
	}

	if key, err := jwt.ParseEdPrivateKeyFromPEM(data); err == nil {
		if edpk, ok := key.(ed25519.PrivateKey); ok {
			return edpk, jwt.SigningMethodEdDSA, nil
		}
	}

	return nil, nil, errors.New("unsupported JWT private key type")
}
