package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

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

	var config map[string]any
	if err := toml.NewDecoder(f).Decode(&config); err != nil {
		return nil, err
	}

	return config, nil
}

func value[T any](config map[string]any, key string, defaultValue T) T {
	anyValue, found := config[key]
	if !found {
		return defaultValue
	}
	if typedValue, ok := anyValue.(T); ok {
		return typedValue
	}
	slog.Warn("unexpected type for TOML config key, using default value",
		slog.String("key", key), slog.Any("default", defaultValue),
		slog.String("expected_type", fmt.Sprintf("%T", defaultValue)),
	)
	return defaultValue
}
