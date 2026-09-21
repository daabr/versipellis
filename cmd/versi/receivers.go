package main

import (
	"context"
	"log/slog"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/http"
)

type receiver interface {
	Base() *config.BaseReceiver
	Start(context.Context) bool
	Close(context.Context)
}

// initReceivers initializes, starts, and returns all the receivers that are defined in the TOML
// configuration file. It does not fail fast; it attempts to initialize all of them before aborting if
// any of them failed. This provides a better experience for first-time users with multiple configuration
// mistakes, as they get feedback on all issues at once rather than encountering them one by one.
func initReceivers(ctx context.Context, senders map[string]config.Sender, entireCfg map[string]any) ([]receiver, bool) {
	var receivers []receiver
	ok := true

	for name, cfg := range config.ExtractSubmaps(entireCfg, "receiver") {
		if len(cfg) == 0 {
			continue // Ignore empty receiver configuration sections (not an error, just useless).
		}

		base, err := config.NewBaseReceiver(cfg, name, senders)
		if err != nil {
			slog.Error("receiver creation error", slog.Any("error", err), slog.String("name", name))
			ok = false
			continue
		}

		innerCfg, valid := cfg[base.Type].(map[string]any)
		if !valid || len(innerCfg) == 0 {
			slog.Error("missing/invalid/empty receiver configuration sub-section",
				slog.String("name", base.Name), slog.String("type", base.Type),
			)
			ok = false
			continue
		}

		var r receiver
		switch base.Type {
		case config.ReceiverTypeHTTP, config.ReceiverTypeHTTP3:
			r, err = http.NewReceiver(base, innerCfg)
		default:
			slog.Error("unhandled receiver type", slog.String("name", base.Name), slog.String("type", base.Type))
			ok = false
			continue
		}

		if err != nil {
			slog.Error("receiver initialization error", slog.Any("error", err),
				slog.String("name", base.Name), slog.String("type", base.Type),
			)
			ok = false
			continue
		}
		receivers = append(receivers, r)
	}
	if !ok {
		return nil, false
	}

	// Receivers are started synchronously, unlike collectors, to ensure deterministic startup order.
	// For example: consistent errors when multiple receivers are configured to use the same port.
	for _, r := range receivers {
		if !r.Start(ctx) {
			b := r.Base()
			slog.Error("failed to start receiver", slog.String("name", b.Name), slog.String("type", b.Type))
			ok = false
		}
	}
	if !ok {
		for _, r := range receivers {
			r.Close(ctx)
		}
		return nil, false
	}

	return receivers, ok
}
