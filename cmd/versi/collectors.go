package main

import (
	"context"
	"log/slog"
	"runtime"
	"runtime/debug"
	"sync"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/http"
	"github.com/daabr/versipellis/pkg/sql"
)

type collector interface {
	Base() *config.BaseCollector
	Start(context.Context) bool
	Done() <-chan struct{}
}

// initCollectors initializes, starts, and returns all the collectors that are defined in the
// TOML configuration file. It does not fail fast; it attempts to initialize all of them before
// aborting if any of them failed. This provides a better experience for first-time users with multiple
// configuration mistakes, as they get feedback on all issues at once rather than encountering them one by one.
func initCollectors(ctx context.Context, senders map[string]config.Sender, cfg map[string]any) ([]collector, bool) {
	var collectors []collector
	ok := true

	for name, cfg := range config.ExtractSubmaps(cfg, "collector") {
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
		}
		if err != nil {
			slog.Error("collector initialization error", slog.Any("error", err),
				slog.String("name", base.Name), slog.String("type", base.Type),
			)
			ok = false
			continue
		}
		if c == nil {
			slog.Error("unhandled collector type", slog.String("name", base.Name), slog.String("type", base.Type))
			ok = false
			continue
		}

		collectors = append(collectors, c)
	}

	if !ok {
		return nil, false
	}
	return startCollectorsAsync(ctx, collectors)
}

// startCollectorsAsync starts all the collectors concurrently, but without overwhelming the system or
// the data sources. It returns the collectors that started successfully, and whether all of them did.
func startCollectorsAsync(ctx context.Context, collectors []collector) ([]collector, bool) {
	limit := min(runtime.GOMAXPROCS(0), len(collectors))
	workers := make(chan collector, limit)
	var started []collector
	var mu sync.Mutex

	// Initialize worker pool.
	var wg sync.WaitGroup
	for range limit {
		wg.Go(func() {
			for c := range workers {
				if startCollectorSafely(ctx, c) {
					mu.Lock()
					started = append(started, c)
					mu.Unlock()
				}
			}
		})
	}

	// Assign work (collector initialization) to the available workers.
	for _, c := range collectors {
		workers <- c // The buffered channel enforces reasonable throttling.
	}

	// Wait for all the worker goroutines to terminate when their initialization work is done.
	close(workers)
	wg.Wait()

	return started, len(started) == len(collectors)
}

func startCollectorSafely(ctx context.Context, c collector) bool {
	defer func() {
		if r := recover(); r != nil {
			b := c.Base()
			slog.Error("panic during collector initialization", slog.Any("details", r),
				slog.String("stack", string(debug.Stack())), slog.String("name", b.Name), slog.String("type", b.Type),
			)
		}
	}()

	if c.Start(ctx) {
		return true
	}

	b := c.Base()
	slog.Error("failed to start collector", slog.String("name", b.Name), slog.String("type", b.Type))
	return false
}
