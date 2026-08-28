// Versipellis - a "data flow shape shifter".
//
// Versipellis is a versatile, scalable tool for transferring and transforming data reliably
// across diverse media, protocols, and formats, without altering the data itself.
//
// It is not a data pipeline, but rather a powerful yet easy-to-use conduit for pipeline inputs and outputs.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/lmittmann/tint"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/sql"
)

func main() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Fprintln(os.Stderr, "Error: failed to read build info")
		os.Exit(1)
	}

	exit, debugLog, structured, path := flags(info)
	if exit {
		os.Exit(0)
	}

	cfg, err := config.ParseFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}

	initLog(debugLog, structured, info)

	ctx, cancel := context.WithCancel(context.Background())
	channels, ok := initCollectors(ctx, cfg)
	if !ok {
		cancel()
		os.Exit(1)
	}

	waitForInterrupt(cancel)
	for _, done := range channels {
		<-done
	}
	slog.Info("shutting down")
}

func initLog(debugLog, structured bool, info *debug.BuildInfo) {
	l := slog.LevelInfo
	if debugLog {
		l = slog.LevelDebug
	}

	var h slog.Handler
	if structured {
		h = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{AddSource: true, Level: l})
	} else {
		h = tint.NewTextHandler(os.Stdout, &tint.Options{AddSource: true, Level: l, TimeFormat: "15:04:05.000"})
	}

	slog.SetDefault(slog.New(h))
	slog.Info("build versions", slog.String("go", info.GoVersion), slog.String("versipellis", info.Main.Version))
	slog.Debug("build settings", buildAttrs(info)...)
}

func buildAttrs(info *debug.BuildInfo) []any {
	attrs := []any{}
	for _, s := range info.Settings {
		switch {
		case strings.HasPrefix(s.Key, "CGO") && s.Value != "":
			attrs = append(attrs, slog.String(strings.ToLower(s.Key), s.Value))
		case strings.HasPrefix(s.Key, "GO") && s.Value != "":
			attrs = append(attrs, slog.String(strings.ToLower(s.Key), s.Value))
		case strings.HasPrefix(s.Key, "vcs.") && s.Value != "":
			attrs = append(attrs, slog.String(strings.ReplaceAll(s.Key, ".", "_"), s.Value))
		case s.Key == "-tags" && s.Value != "":
			attrs = append(attrs, slog.String("tags", s.Value))
		}
	}
	return attrs
}

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

func waitForInterrupt(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(ch)
	sig := <-ch

	slog.Warn("intercepted OS signal", slog.String("type", sig.String()))
	cancel()
}
