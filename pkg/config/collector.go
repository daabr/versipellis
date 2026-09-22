package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/daabr/versipellis/pkg/cron"
)

// CollectorType* constants represent all the available types of "collector" configurations in the TOML file.
const (
	CollectorTypeHTTP  = "http"
	CollectorTypeHTTP3 = "http3"
	CollectorTypeSQL   = "sql"
)

var validCollectorTypes = []string{
	CollectorTypeHTTP,
	CollectorTypeHTTP3,
	CollectorTypeSQL,
}

// BaseCollector contains the basic details of any "collector" configuration in the TOML file.
type BaseCollector struct {
	// For now, [BaseCollector] is an extension of [BaseReceiver], but
	// if this changes in the future, it shouldn't break existing code.
	BaseReceiver

	Cronspec    string
	Schedule    *cron.Schedule
	Trigger     string // Not fully implemented yet, but reserved for future use.
	Concurrency int
}

// NewBaseCollector creates a new [BaseCollector] from the given configuration, which was read from a TOML file. It checks
// these details and returns an error if any of them is invalid, but the caller is responsible for providing non-nil input.
func NewBaseCollector(cfg map[string]any, name string, senders map[string]Sender) (*BaseCollector, error) {
	c := &BaseCollector{
		Type:        strings.ToLower(strings.TrimSpace(Value(cfg, "type", ""))),
		Name:        name,
		Cronspec:    Value(cfg, "schedule", ""),
		Trigger:     Value(cfg, "trigger", ""),
		Concurrency: concurrencyLimit(cfg, name),
		Destination: strings.TrimSpace(Value(cfg, "destination", "")), // Attention: case sensitive!
	}
	var senderFound bool
	c.Sender, senderFound = senders[c.Destination]

	switch {
	case c.Type == "":
		return nil, errors.New("type field required but not found")
	case !slices.Contains(validCollectorTypes, c.Type):
		return nil, fmt.Errorf("unrecognized type %q", c.Type)
	case !senderFound:
		return nil, fmt.Errorf("unrecognized destination %q", c.Destination)
	case c.Cronspec != "" && c.Trigger != "":
		return nil, errors.New("configuration cannot have both a schedule and a trigger")
	case c.Trigger != "":
		return c, nil
	case c.Cronspec == "":
		return nil, errors.New("configuration must have either a schedule or a trigger")
	}

	tz, label := LoadLocation(Value(cfg, "timezone", "UTC"))
	if tz == nil {
		return nil, fmt.Errorf("invalid time zone %q", label)
	}
	sched, err := cron.Parse(c.Cronspec, tz)
	if err != nil {
		return nil, fmt.Errorf("invalid expression in schedule: %w", err)
	}
	c.Cronspec = fmt.Sprintf("TZ=%s %s", label, c.Cronspec)
	if !sched.RunsOnlyOnce() && sched.Next(time.Now()).IsZero() {
		return nil, fmt.Errorf("schedule %q will never run", c.Cronspec)
	}

	c.Schedule = sched
	return c, nil
}

// LoadLocation returns the [time.Location] corresponding to the given timezone name. This is
// a thin wrapper over [time.LoadLocation] to support case-insensitivity for "UTC" and "Local".
func LoadLocation(timezone string) (*time.Location, string) {
	timezone = strings.TrimSpace(timezone)
	switch strings.ToLower(timezone) {
	case "", "utc":
		return time.UTC, "UTC"
	case "local":
		return time.Local, time.Local.String() //nolint:gosmopolitan // Intentional configuration option.
	}

	if loc, err := time.LoadLocation(timezone); err == nil {
		return loc, timezone
	}
	return nil, timezone
}
