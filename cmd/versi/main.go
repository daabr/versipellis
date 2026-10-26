// Versipellis ("versi") is a versatile, scalable tool for transferring and transforming data
// reliably across services, networks, protocols and formats, without altering the data itself.
//
// It is an easy-to-use adapter and conduit around data pipelines, sources, and sinks. It can move
// and restructure data of any shape or size, with adjustable security in transit and at rest - while
// maintaining strong safeguards against corruption and loss, or latency and crashes under heavy load.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"

	"github.com/lmittmann/tint"

	"github.com/daabr/versipellis/pkg/config"
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
	senders, ok := initSenders(cfg)
	if !ok {
		cancel()
		os.Exit(1)
	}
	collectors, ok := initCollectors(ctx, senders, cfg)
	if !ok {
		cancel()
		shutdown(context.Background(), collectors, nil, senders)
		os.Exit(1)
	}
	receivers, ok := initReceivers(ctx, senders, cfg)
	if !ok {
		cancel()
		shutdown(context.Background(), collectors, nil, senders)
		os.Exit(1)
	}
	if len(collectors)+len(receivers) == 0 {
		slog.Error("no collectors or receivers initialized")
		cancel()
		os.Exit(1)
	}

	waitForInterrupt(cancel)
	shutdown(context.Background(), collectors, receivers, senders)
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

func waitForInterrupt(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(ch)
	sig := <-ch

	slog.Warn("intercepted OS signal, entering lame-duck mode", slog.String("signal", sig.String()))
	cancel()
}

// shutdown gracefully drains and closes all asynchronous components, in this order:
//  1. Collectors and receivers, in parallel (each with its own timeout).
//  2. All senders, except the Dead-Letter Queue (DLQ), in parallel.
//  3. The DLQ (so it can save undelivered data from other senders).
func shutdown(ctx context.Context, collectors []collector, receivers []receiver, senders map[string]config.Sender) {
	var wg sync.WaitGroup
	for _, r := range receivers {
		wg.Go(func() { r.Close(ctx) })
	}
	for _, c := range collectors {
		if ch := c.Done(); ch != nil {
			<-ch
		}
	}
	wg.Wait()

	dlq := senders[config.SenderTypeDLQ]
	delete(senders, config.SenderTypeDLQ)
	for _, sender := range senders {
		wg.Go(func() { sender.Close(ctx) })
	}
	wg.Wait()

	dlq.Close(ctx)
	slog.Info("shutting down")
}
