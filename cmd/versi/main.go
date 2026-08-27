// Versipellis - a "data flow shape shifter".
//
// Versipellis is a versatile, scalable tool for transferring and transforming data reliably
// across diverse media, protocols, and formats, without altering the data itself.
//
// It is not a data pipeline, but rather a powerful yet easy-to-use conduit for pipeline inputs and outputs.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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
		fmt.Println("Error: failed to read build info")
		os.Exit(1)
	}

	exit, debugLog, structured, path := flags(info)
	if exit {
		os.Exit(0)
	}

	cfg, err := config.ParseFile(path)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	initLog(debugLog, structured, info)

	ctx, cancel := context.WithCancel(context.Background())
	channels, err := initCollectors(ctx, cfg)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
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

func initCollectors(ctx context.Context, cfg map[string]any) ([]<-chan struct{}, error) {
	var done []<-chan struct{}
	errs := 0

	for namespace, collector := range config.ExtractSubmaps(cfg, "collector") {
		base, err := config.NewBaseCollector(collector, namespace)
		if err != nil {
			slog.Error("failed to create base collector", slog.Any("error", err), slog.String("name", namespace))
			errs++
			continue
		}

		switch base.Type {
		case config.CollectorTypeHTTP, config.CollectorTypeHTTP3:
			slog.Error("HTTP collectors are not implemented yet", slog.String("name", namespace))
			errs++
			continue

		case config.CollectorTypeSQL:
			c, err := sql.NewCollector(base, collector)
			switch {
			case err != nil:
				slog.Error("failed to create SQL collector", slog.Any("error", err), slog.String("name", namespace))
				errs++
			case !c.Start(ctx):
				slog.Error("failed to start SQL collector", slog.String("name", namespace))
				errs++
			default:
				done = append(done, c.Done())
			}
		}
	}

	switch {
	case errs > 0:
		msg := fmt.Sprintf("failed to initialize %d collector(s)", errs)
		return nil, errors.New(msg)
	case len(done) == 0:
		// Until we add receivers, we cannot run without collectors.
		return nil, errors.New("no collectors found in configuration")
	default:
		return done, nil
	}
}

func waitForInterrupt(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(ch)
	sig := <-ch

	slog.Warn("intercepted OS signal", slog.String("type", sig.String()))
	cancel()
}
