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

	senderIndexSuffix = regexp.MustCompile(`\[\d+\]$`)
)

func initSenders(entireCfg map[string]any) (map[string]config.Sender, bool) {
	senders := map[string]config.Sender{
		"":                       dest.Discard,
		config.SenderTypeDiscard: dest.Discard,
		config.SenderTypeNone:    dest.Discard,

		config.SenderTypeStdout: dest.Stdout,
		config.SenderTypeDLQ:    dest.DeadLetterQueue,
	}
	if entireCfg == nil {
		return senders, true
	}

	ok := true
	for name, cfg := range config.ExtractSubSubmaps(entireCfg, "sender", validSenderTypes) {
		if len(cfg) == 0 {
			continue // Ignore empty sender configuration sections (not an error, just useless).
		}

		baseType := senderIndexSuffix.ReplaceAllString(name, "")
		if _, leaf, found := strings.CutLast(baseType, "."); found {
			baseType = leaf
		}

		switch baseType {
		case config.SenderTypeHTTP, config.SenderTypeHTTP3:
			d, err := http.NewDestination(cfg, name, baseType)
			if err == nil {
				senders[name] = d.Send
				break // Exit the switch statement.
			}
			slog.Error("failed to initialize sender", slog.Any("error", err),
				slog.String("name", name), slog.String("type", baseType),
			)
			ok = false
		default:
			slog.Error("unexpected sender type", slog.String("name", name), slog.String("type", baseType))
			ok = false
		}
	}

	return senders, ok
}
