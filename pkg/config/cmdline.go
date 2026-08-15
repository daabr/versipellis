package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// DefaultFilePath is the default path to the TOML configuration file.
var DefaultFilePath = filepath.Join("config", "versi.toml")

// ParseFile parses the TOML configuration file at the given path and returns its contents.
func ParseFile(path string) (map[string]any, error) {
	info, err := os.Stat(path)
	switch {
	case path == DefaultFilePath && errors.Is(err, fs.ErrNotExist):
		return nil, nil // Rely on default values if the default file does not exist.
	case err != nil:
		return nil, err
	case info.IsDir():
		return nil, fmt.Errorf("configuration path should be a TOML file, not a directory: %s", path)
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
