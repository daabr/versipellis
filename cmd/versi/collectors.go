package main

import (
	"context"
	"log/slog"
	"runtime"
	"runtime/debug"

	"github.com/daabr/versipellis/pkg/config"
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

func initCollectors(ctx context.Context, entireCfg map[string]any) ([]<-chan struct{}, bool) {
	var collectors []collector
	for namespace, cfg := range config.ExtractSubmaps(entireCfg, "collector") {
		base, err := config.NewBaseCollector(cfg, namespace)
		if err != nil {
			slog.Error("failed to create base collector", slog.Any("error", err), slog.String("name", namespace))
			continue
		}

		var c collector
		switch base.Type {
		case config.CollectorTypeHTTP, config.CollectorTypeHTTP3:
			slog.Error("HTTP collector not yet implemented", slog.String("name", base.Name), slog.String("type", base.Type))
			continue
		case config.CollectorTypeSQL:
			c, err = sql.NewCollector(base, cfg)
		default:
			slog.Error("unhandled collector type", slog.String("name", base.Name), slog.String("type", base.Type))
			continue
		}

		if err != nil {
			slog.Error("failed to create collector", slog.Any("error", err),
				slog.String("name", base.Name), slog.String("type", base.Type),
			)
			continue
		}
		collectors = append(collectors, c)
	}

	results := make(chan collectorInitResult, len(collectors))
	initCollectorsAsync(ctx, collectors, results)
	var done []<-chan struct{}
	for range collectors {
		if res := <-results; res.ok {
			done = append(done, res.done)
		}
	}
	close(results)

	// Temporary: until we add receivers, we require at least one collector in order to run.
	if len(done) == 0 {
		slog.Error("no collectors were initialized successfully")
		return nil, false
	}

	return done, true
}

// Start all the collectors concurrently, without overwhelming the system or the data sources.
func initCollectorsAsync(ctx context.Context, collectors []collector, results chan<- collectorInitResult) {
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
