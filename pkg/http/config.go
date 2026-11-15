package http

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/http/httpguts"

	"github.com/daabr/versipellis/pkg/config"
)

const (
	// CloseTimeout is the maximum amount of time to wait for HTTP requests to finish before forcefully closing
	// the connection. This is used during shutdown, so it's intentionally short and not configurable.
	CloseTimeout = 5 * time.Second

	// AbortTimeout is the maximum amount of time to wait for an HTTP request to be aborted after [CloseTimeout].
	AbortTimeout = time.Second

	maxByteSize          int64 = 100 << 20 // 100 MiB (which is already quite large for a single HTTP request).
	defaultMaxBodySize   int64 = 10 << 20  // 10 MiB.
	defaultMaxHeaderSize int64 = 1 << 20   // 1 MiB.

	defaultRequestTimeout = 5 * time.Second
)

func parseMethod(rawMethod, action string) (string, error) {
	switch m := strings.ToUpper(rawMethod); m {
	case http.MethodGet, http.MethodPatch, http.MethodPost, http.MethodPut:
		return m, nil
	case http.MethodConnect, http.MethodDelete, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return "", fmt.Errorf("HTTP method %q is not supported for data %s", m, action)
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
		return fmt.Errorf("HTTP %s 'query' must be a table of string key-value pairs, got %T", role, rawCfg)
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
		return nil, fmt.Errorf("HTTP %s 'headers' must be a table of string key-value pairs, got %T", role, rawCfg)
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
		slog.Warn(fmt.Sprintf("clamping %s to upper bound", description), slog.String("name", name),
			slog.Int64("above_max", value), slog.Int64("new_value", maxByteSize),
		)
		value = maxByteSize
	}

	return value
}
