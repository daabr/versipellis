package http

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"

	"github.com/daabr/versipellis/pkg/config"
)

const (
	// CloseTimeout is the maximum amount of time to wait for HTTP requests to finish before forcibly closing
	// the connection. This is used during shutdown, so it's intentionally short and not configurable.
	CloseTimeout = 5 * time.Second

	httpScheme  = "http"
	httpsScheme = "https"

	maxByteSize          int64 = 100 << 20 // 100 MiB (which is already quite large for a single HTTP request).
	defaultMaxBodySize   int64 = 10 << 20  // 10 MiB.
	defaultMaxHeaderSize int64 = 1 << 20   // 1 MiB.

	defaultRequestTimeout = 5 * time.Second
)

func parseURL(rawURL, protoVer string) (*url.URL, error) {
	if rawURL == "" {
		return nil, errors.New("url field required but not found")
	}

	u, err := url.Parse(rawURL)
	switch {
	case err != nil:
		return nil, fmt.Errorf("invalid URL: %w", err)
	case !u.IsAbs():
		return nil, errors.New("URL must be absolute (start with a scheme)")
	}

	u.Scheme = strings.ToLower(u.Scheme)
	switch {
	case u.Scheme != httpsScheme && (protoVer == config.CollectorTypeHTTP3 || protoVer == config.SenderTypeHTTP3):
		return nil, errors.New("HTTP/3 URL must have an HTTPS scheme")
	case u.Scheme != httpsScheme && u.Scheme != httpScheme:
		return nil, errors.New("URL must have an HTTP/S scheme")
	case u.Opaque != "":
		return nil, fmt.Errorf(`URL must have "//" after the "%s:" scheme`, u.Scheme)
	case u.Hostname() == "":
		return nil, errors.New("URL must have a host address")
	case u.Port() != "":
		// [url.Parse] returns an error for negative and non-numeric values, but not out-of-range numbers.
		if port, err := strconv.Atoi(u.Port()); err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("URL has an invalid port number: %q", u.Port())
		}
	}

	return u, nil
}

func parseMethod(rawMethod, action string) (string, error) {
	switch m := strings.ToUpper(rawMethod); m {
	case http.MethodGet, http.MethodPatch, http.MethodPost, http.MethodPut:
		return m, nil
	case http.MethodConnect, http.MethodDelete, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return "", fmt.Errorf("HTTP method %q not supported for data %s", m, action)
	default:
		return "", fmt.Errorf("invalid HTTP method %q", rawMethod)
	}
}

// parseQuery adds "query" key-value pairs (if there are any) to the URL's query.
// It overrides any existing parameters from the original URL with the same name,
// and returns an error if the type of any configured value isn't a string.
func parseQuery(u *url.URL, rawCfg any, role string) error {
	if rawCfg == nil {
		return nil
	}
	cfg, ok := rawCfg.(map[string]any)
	if !ok {
		return fmt.Errorf(`HTTP %s "query" must be a table of string key-value pairs, got %T`, role, rawCfg)
	}
	if len(cfg) == 0 {
		return nil
	}

	values := u.Query()
	for key, rawValue := range cfg {
		if v, ok := rawValue.(string); ok {
			values.Set(key, v)
			continue
		}
		return fmt.Errorf("query parameter %q must be a string, got %T", key, rawValue)
	}

	u.RawQuery = values.Encode()
	return nil
}

func parseHeaders(rawCfg any, role string) (http.Header, error) {
	if rawCfg == nil {
		return make(http.Header), nil
	}
	cfg, ok := rawCfg.(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`HTTP %s "headers" must be a table of string key-value pairs, got %T`, role, rawCfg)
	}

	headers := make(http.Header, len(cfg))
	for key, value := range cfg {
		if !httpguts.ValidHeaderFieldName(key) {
			return nil, fmt.Errorf("invalid HTTP header name %q", key)
		}
		v, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("HTTP header value for %q must be a string, got %T", key, value)
		}
		if !httpguts.ValidHeaderFieldValue(v) {
			return nil, fmt.Errorf("invalid HTTP header value for %q", key)
		}
		headers.Set(key, v)
	}

	return headers, nil
}

func parseByteSize(cfg map[string]any, key, name string, defaultValue int64) int64 {
	value := config.Value(cfg, key, defaultValue)
	if value <= 0 {
		description := strings.ReplaceAll(key, "_", " ")
		slog.Warn(description+" has an invalid (non-positive) value, using default value",
			slog.String("name", name), slog.Int64("actual", value), slog.Int64("default", defaultValue),
		)
		value = defaultValue
	}
	if value > maxByteSize {
		description := strings.ReplaceAll(key, "_", " ")
		slog.Warn("forcing upper bound on "+description, slog.String("name", name),
			slog.Int64("above_max", value), slog.Int64("new_value", maxByteSize),
		)
		value = maxByteSize
	}

	return value
}
