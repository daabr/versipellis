package main

import (
	"log/slog"
	"regexp"
	"strings"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/dest"
	"github.com/daabr/versipellis/pkg/http"
)

var (
	validSenderTypes = []string{
		config.SenderTypeHTTP,
		config.SenderTypeHTTP3,
	}

	cfgIndexSuffix = regexp.MustCompile(`\[\d+\]$`)
)

// initSenders initializes all the senders that are defined in the TOML configuration file, and returns a
// map of their instances. It does not fail fast; it attempts to initialize all of them before aborting if
// any of them failed. This provides a better experience for first-time users with multiple configuration
// mistakes, as they get feedback on all issues at once rather than encountering them one by one.
func initSenders(cfg map[string]any) (map[string]config.Sender, bool) {
	dlq := dest.InitDeadLetterQueue("data")
	ok := dlq != nil

	senders := map[string]config.Sender{
		"":                       dest.Discard,
		config.SenderTypeDiscard: dest.Discard,
		config.SenderTypeNone:    dest.Discard,

		config.SenderTypeStdout: dest.Stdout,
		config.SenderTypeDLQ:    dlq,
	}

	for name, cfg := range config.ExtractSubSubmaps(cfg, "sender", validSenderTypes) {
		if len(cfg) == 0 {
			continue // Ignore empty sender configuration sections (not an error, just useless).
		}

		baseType := cfgIndexSuffix.ReplaceAllString(name, "")
		if _, leaf, found := strings.CutLast(baseType, "."); found {
			baseType = leaf
		}

		switch baseType {
		case config.SenderTypeHTTP, config.SenderTypeHTTP3:
			s, err := http.NewSender(cfg, name, baseType)
			if err != nil {
				slog.Error("sender initialization error", slog.Any("error", err),
					slog.String("name", name), slog.String("type", baseType),
				)
				ok = false
			}
			senders[name] = s // No problem even if s == nil, because we abort afterwards if !ok.
		default:
			slog.Error("unrecognized sender type", slog.String("name", name), slog.String("type", baseType))
			ok = false
		}
	}

	return senders, ok
}
