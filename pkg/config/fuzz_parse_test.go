package config_test

import (
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/daabr/versipellis/pkg/config"
)

func FuzzExtractSubmaps(f *testing.F) {
	f.Add([]byte("[collector.sql]\ntype = \"sql\"\n"), "collector")
	f.Add([]byte("[[collector]]\ntype = \"http\"\n[[collector]]\ntype = \"sql\"\n"), "collector")

	f.Fuzz(func(_ *testing.T, tomlBytes []byte, key string) {
		var cfg map[string]any
		if err := toml.Unmarshal(tomlBytes, &cfg); err != nil {
			return
		}
		// Must not panic on any structure or key.
		_ = config.ExtractSubmaps(cfg, key)
	})
}
