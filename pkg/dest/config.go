package dest

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/daabr/versipellis/pkg/config"
)

const (
	dlqSection = "dead_letter_queue"

	defaultDLQMaxSize  int64 = 0
	defaultDLQWarnSize int64 = 100 << 20 // 100 MiB.
	maxDLQSize         int64 = 1 << 50   // 1 PiB.

	// Minimal delay between measurements of the DLQ directory's total size, to notice external changes
	// (e.g., files that were deleted after replaying them). It's a prime number of seconds (7 minutes + 1).
	remeasureMinDelay = 421 * time.Second

	// As long as there's no DLQ activity, the delay between measurements doubles, but not beyond this limit.
	remeasureMaxDelay = time.Hour
)

// nextRemeasureDelay returns the delay until the next measurement of the DLQ directory's total size: the
// minimal delay after a change, otherwise double the given delay, unless that exceeds the maximal delay.
func nextRemeasureDelay(delay time.Duration, changed bool) time.Duration {
	switch {
	case changed:
		delay = remeasureMinDelay
	case delay*2 <= remeasureMaxDelay:
		delay *= 2
	}
	return delay
}

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
