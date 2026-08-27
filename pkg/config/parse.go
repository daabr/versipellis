package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
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
		return nil, errors.New("configuration path should be a TOML file, not a directory: " + path)
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

// ExtractSubmaps extracts all the sub-maps with the given key from the given configuration map.
// The returned map has the full path to the sub-map as the key (without the leaf) and the sub-map
// itself as the value. Note that this function does not recurse within already-matched sub-maps.
func ExtractSubmaps(cfg map[string]any, key string) map[string]map[string]any {
	submaps := make(map[string]map[string]any)
	traverseMap(cfg, key, []string{}, submaps)
	return submaps
}

func traverseMap(cfg map[string]any, key string, path []string, submaps map[string]map[string]any) {
	if cfg == nil {
		return
	}

	for k, v := range cfg {
		if k == key {
			switch t := v.(type) {
			case map[string]any:
				submaps[strings.Join(path, ".")] = t
			case []any:
				for i, e := range t {
					if m, ok := e.(map[string]any); ok {
						submaps[fmt.Sprintf("%s[%d]", strings.Join(path, "."), i+1)] = m
					}
				}
			}
			continue
		}

		switch t := v.(type) {
		case map[string]any:
			traverseMap(t, key, append(path, k), submaps)
		case []any:
			for i, e := range t {
				if m, ok := e.(map[string]any); ok {
					traverseMap(m, key, append(path, fmt.Sprintf("%s[%d]", k, i+1)), submaps)
				}
			}
		}
	}
}

// Value retrieves a generic value with the given key from the given TOML-based configuration map.
// This function intentionally does not return errors, it reports them via logging.
func Value[T any](cfg map[string]any, key string, defaultValue T) T {
	anyValue, found := cfg[key]
	if !found {
		return defaultValue
	}
	if typedValue, ok := anyValue.(T); ok {
		return typedValue
	}
	slog.Warn("unexpected type for TOML config key, using default value",
		slog.String("key", key), slog.Any("default", defaultValue),
		slog.String("expected_type", fmt.Sprintf("%T", defaultValue)),
		slog.String("actual_type", fmt.Sprintf("%T", anyValue)),
	)
	return defaultValue
}
