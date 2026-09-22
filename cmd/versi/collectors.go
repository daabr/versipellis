package main

import (
	"context"
	"log/slog"
	"runtime"
	"runtime/debug"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/http"
	"github.com/daabr/versipellis/pkg/sql"
)

type collector interface {
	Base() *config.BaseCollector
	Start(context.Context) bool
	Done() <-chan struct{}
}

type collectorInitResult struct {
	done <-chan struct{}
	ok   bool
}

// initCollectors initializes and starts all the collectors that are defined in the TOML configuration
// file, and returns their done channels. It does not fail fast; it attempts to initialize all of them
// before aborting if any of them failed. This provides a better experience for first-time users with multiple
// configuration mistakes, as they get feedback on all issues at once rather than encountering them one by one.
func initCollectors(ctx context.Context, senders map[string]config.Sender, entireCfg map[string]any) ([]<-chan struct{}, bool) {
	var collectors []collector
	ok := true

	for name, cfg := range config.ExtractSubmaps(entireCfg, "collector") {
		if len(cfg) == 0 {
			continue // Ignore empty collector configuration sections (not an error, just useless).
		}

		base, err := config.NewBaseCollector(cfg, name, senders)
		if err != nil {
			slog.Error("collector creation error", slog.Any("error", err), slog.String("name", name))
			ok = false
			continue
		}

		innerCfg, valid := cfg[base.Type].(map[string]any)
		if !valid || len(innerCfg) == 0 {
			slog.Error("missing/invalid/empty collector configuration sub-section",
				slog.String("name", base.Name), slog.String("type", base.Type),
			)
			ok = false
			continue
		}

		var c collector
		switch base.Type {
		case config.CollectorTypeHTTP, config.CollectorTypeHTTP3:
			c, err = http.NewCollector(base, innerCfg)
		case config.CollectorTypeSQL:
			c, err = sql.NewCollector(base, innerCfg)
		default:
			slog.Error("unhandled collector type", slog.String("name", base.Name), slog.String("type", base.Type))
			ok = false
			continue
		}

		if err != nil {
			slog.Error("collector initialization error", slog.Any("error", err),
				slog.String("name", base.Name), slog.String("type", base.Type),
			)
			ok = false
			continue
		}
		collectors = append(collectors, c)
	}
	if !ok {
		return nil, false
	}

	results := make(chan collectorInitResult, len(collectors))
	startCollectorsAsync(ctx, collectors, results)
	var done []<-chan struct{}
	for range collectors {
		if res := <-results; res.ok {
			done = append(done, res.done)
		} else {
			ok = false
		}
	}

	close(results)
	if !ok {
		return nil, false
	}

	return done, true
}

// startCollectorsAsync starts all the collectors concurrently, but without overwhelming the system or the data sources.
func startCollectorsAsync(ctx context.Context, collectors []collector, results chan<- collectorInitResult) {
	limit := min(runtime.GOMAXPROCS(0), len(collectors))
	workers := make(chan collector, limit)

	for range limit {
		go func() {
			for c := range workers {
				startCollectorSafely(ctx, c, results)
			}
		}()
	}

	for _, c := range collectors {
		workers <- c // The buffered channel enforces reasonable throttling.
	}
	close(workers) // Signal all the goroutines above to terminate when they're done.
}

func startCollectorSafely(ctx context.Context, c collector, results chan<- collectorInitResult) {
	defer func() {
		if r := recover(); r != nil {
			b := c.Base()
			slog.Error("panic during collector initialization",
				slog.Any("details", r), slog.String("stack", string(debug.Stack())),
				slog.String("name", b.Name), slog.String("type", b.Type),
			)
			results <- collectorInitResult{}
		}
	}()

	if c.Start(ctx) {
		results <- collectorInitResult{done: c.Done(), ok: true}
		return
	}

	b := c.Base()
	slog.Error("failed to start collector", slog.String("name", b.Name), slog.String("type", b.Type))
	results <- collectorInitResult{}
}
