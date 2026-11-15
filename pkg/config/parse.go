package config

import (
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/daabr/versipellis/pkg/flow"
)

// DefaultFilePath is the default path to the TOML configuration file.
var DefaultFilePath = filepath.Join("config", "versi.toml")

// ParseFile parses the TOML configuration file at the given path and returns its contents.
func ParseFile(path string) (map[string]any, error) {
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	switch {
	case path == filepath.Clean(DefaultFilePath) && errors.Is(err, os.ErrNotExist):
		return nil, nil // Rely on default values if the default file does not exist.
	case err != nil:
		return nil, err
	case info.IsDir():
		return nil, errors.New("config path should be a TOML file, not a directory: " + path)
	}

	f, err := os.Open(path) //gosec:disable G304 // Modifiable by design.
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cfg map[string]any
	if err := toml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// ExtractSubmaps extracts all the sub-maps with the given key from the given TOML-based configuration
// map. The returned map has the full path to the sub-map as the key and the sub-map itself as
// the value. Note that this function does not recurse within already-matched sub-maps.
func ExtractSubmaps(cfg map[string]any, key string) map[string]map[string]any {
	submaps := make(map[string]map[string]any)
	traverseMap(cfg, key, "root", submaps)
	return submaps
}

func traverseMap(cfg map[string]any, key, path string, submaps map[string]map[string]any) {
	if cfg == nil {
		return
	}

	for k, v := range cfg {
		if k == key {
			switch t := v.(type) {
			case map[string]any:
				submaps[strings.TrimPrefix(fmt.Sprintf("%s.%s", path, k), "root.")] = t
			case []any:
				for i, e := range t {
					if m, ok := e.(map[string]any); ok {
						submaps[strings.TrimPrefix(fmt.Sprintf("%s.%s[%d]", path, k, i+1), "root.")] = m
					}
				}
			}
			continue
		}

		switch t := v.(type) {
		case map[string]any:
			traverseMap(t, key, fmt.Sprintf("%s.%s", path, k), submaps)
		case []any:
			for i, e := range t {
				if m, ok := e.(map[string]any); ok {
					traverseMap(m, key, fmt.Sprintf("%s.%s[%d]", path, k, i+1), submaps)
				}
			}
		}
	}
}

// ExtractSubSubmaps extracts all the sub-maps with the given subkeys from the results of [ExtractSubmaps].
func ExtractSubSubmaps(cfg map[string]any, key string, expectedSubkeys []string) map[string]map[string]any {
	submaps := make(map[string]map[string]any)
	for path, submap := range ExtractSubmaps(cfg, key) {
		for _, k := range expectedSubkeys {
			if v, found := submap[k]; found {
				switch t := v.(type) {
				case map[string]any:
					submaps[fmt.Sprintf("%s.%s", path, k)] = t
				case []any:
					for i, e := range t {
						if m, ok := e.(map[string]any); ok {
							submaps[fmt.Sprintf("%s.%s[%d]", path, k, i+1)] = m
						}
					}
				}
			}
		}
		for k, v := range submap {
			_, isMap := v.(map[string]any)
			_, isSlice := v.([]any)
			// Even with huge config files, O(n) key checks are fine.
			if (isMap || isSlice) && !slices.Contains(expectedSubkeys, k) {
				// Unlike collectors, it's not necessarily an error if a sender type is unrecognized,
				// but we still want to inform the user about it in case it's an unintentional typo.
				slog.Warn(fmt.Sprintf("unrecognized %s type", key), slog.String("path", path), slog.String("type", k))
			}
		}
	}
	return submaps
}

// Value retrieves a generic value with the given key from the given TOML-based configuration map.
// This function intentionally does not return errors, it reports them via logging.
// Attention: TOML returns all integers as int64, and all floats as float64!
func Value[T any](cfg map[string]any, key string, defaultValue T) T {
	anyValue, found := cfg[key]
	if !found {
		return defaultValue
	}
	if typedValue, ok := anyValue.(T); ok {
		return typedValue
	}
	slog.Warn("TOML config field has an unexpected type, using default value",
		slog.String("field", key), slog.Any("default", defaultValue),
		slog.String("expected_type", fmt.Sprintf("%T", defaultValue)),
		slog.String("actual_type", fmt.Sprintf("%T", anyValue)),
	)
	return defaultValue
}

// BoundedInt ensures that the given integer value falls within the specified bounds.
func BoundedInt(value, minValue, maxValue int64, name, description string) int {
	if value < minValue {
		slog.Warn(fmt.Sprintf("clamping %s to lower bound", description), slog.String("name", name),
			slog.Int64("below_min", value), slog.Int64("new_value", minValue),
		)
		value = minValue
	}
	if value > maxValue {
		slog.Warn(fmt.Sprintf("clamping %s to upper bound", description), slog.String("name", name),
			slog.Int64("above_max", value), slog.Int64("new_value", maxValue),
		)
		value = maxValue
	}

	return int(value)
}

const (
	defaultConcurrencyLimit int64 = 1

	maxConcurrency = 100
)

func concurrencyLimit(cfg map[string]any, name string) int {
	n := Value(cfg, "concurrency_limit", defaultConcurrencyLimit)
	return BoundedInt(n, defaultConcurrencyLimit, maxConcurrency, name, "collector concurrency limit")
}

const (
	noBatching = 0

	maxBatchItems = 1_000_000
	maxBatchBytes = 1 << 30 // 1 GiB.
)

// BatchLimits parses the batch configuration within the given TOML-based configuration map of a single
// collector or sender, merged with its provided defaults, and returns the corresponding [flow.Limits].
func BatchLimits(rawCfg any, name string, defaults flow.Limits) (flow.Limits, error) {
	if rawCfg == nil {
		return defaults, nil
	}
	cfg, ok := rawCfg.(map[string]any)
	if !ok {
		return flow.Limits{}, fmt.Errorf("'batch' must be a table of key-value pairs, got %T", rawCfg)
	}

	n := Value(cfg, "max_items", int64(defaults.MaxItems))
	b := Value(cfg, "max_bytes", int64(defaults.MaxBytes))
	d, err := time.ParseDuration(Value(cfg, "time_window", defaults.Window.String()))
	if err != nil {
		return flow.Limits{}, fmt.Errorf("invalid time window duration: %w", err)
	}

	return flow.Limits{
		MaxItems: BoundedInt(n, noBatching, maxBatchItems, name, "maximum batch items"),
		MaxBytes: BoundedInt(b, noBatching, maxBatchBytes, name, "maximum batch byte size"),
		Window:   max(d, noBatching),
	}, nil
}

// Convenience constants for the HTTP and HTTPS URL schemes.
const (
	URLSchemeHTTP  = "http"
	URLSchemeHTTPS = "https"
)

// ParseURL parses, normalizes, and validates the given raw URL string. When the HTTP version is "http3", the URL
// must have an HTTPS scheme. When the URL isn't required, it's allowed to be empty (in which case we return nil).
func ParseURL(rawURL, httpVersion string, required bool) (*url.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	switch {
	case rawURL == "" && !required:
		return nil, nil
	case rawURL == "" && required:
		return nil, errors.New("url field is required but not found")
	}
	u, err := url.Parse(rawURL)
	switch {
	case err != nil:
		return nil, errors.New("invalid URL") // Don't expose the underlying error for security reasons.
	case !u.IsAbs():
		return nil, errors.New("URL must be absolute (start with a scheme)")
	}

	u.Scheme = strings.ToLower(u.Scheme)
	switch {
	case u.Scheme != URLSchemeHTTPS && (httpVersion == CollectorTypeHTTP3 || httpVersion == SenderTypeHTTP3):
		return nil, errors.New("HTTP/3 URL must have an HTTPS scheme")
	case u.Scheme != URLSchemeHTTPS && u.Scheme != URLSchemeHTTP:
		return nil, errors.New("URL must have an HTTP/S scheme")
	case u.Opaque != "":
		return nil, fmt.Errorf("URL must have '//' after the '%s:' scheme", u.Scheme)
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

// LoadPEMFile reads and parses a PEM-encoded file from the given path. If
// the file is optional and a path is not provided, this function returns nil.
func LoadPEMFile(path, description string, required bool) ([]byte, error) {
	path = strings.TrimSpace(path)
	if required && path == "" {
		return nil, errors.New(description + " file is required but not provided")
	}
	if path == "" {
		return nil, nil
	}

	data, err := os.ReadFile(path) //gosec:disable G304 // Path is configurable by design.
	if err != nil {
		return nil, fmt.Errorf("failed to read %s file: %w", description, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%s file is empty: %s", description, path)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to parse PEM block for %s from: %s", description, path)
	}

	return data, nil
}
