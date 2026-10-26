package dest

import (
	"fmt"
	"log/slog"

	"github.com/daabr/versipellis/pkg/config"
)

const (
	dlqSection = "dead_letter_queue"

	defaultDLQMaxSize  int64 = 0
	defaultDLQWarnSize int64 = 100 << 20 // 100 MiB.
	maxDLQSize         int64 = 1 << 50   // 1 PiB.
)

func parseDLQ(rawCfg any) (maxSize, warnSize int64, err error) {
	if rawCfg == nil {
		rawCfg = map[string]any{}
	}
	cfg, ok := rawCfg.(map[string]any)
	if !ok {
		return 0, 0, fmt.Errorf("%q must be a table of key-value pairs, got %T", dlqSection, rawCfg)
	}

	size := config.Value(cfg, "max_size", defaultDLQMaxSize)
	maxSize = int64(config.BoundedInt(size, 0, maxDLQSize, dlqSection, "max size"))
	size = config.Value(cfg, "warning_size", defaultDLQWarnSize)
	warnSize = int64(config.BoundedInt(size, 0, maxDLQSize, dlqSection, "warning size"))

	for key := range cfg {
		if key != "max_size" && key != "warning_size" {
			slog.Warn("unexpected TOML config field", slog.String("section", dlqSection), slog.String("field", key))
		}
	}

	return maxSize, warnSize, nil
}
