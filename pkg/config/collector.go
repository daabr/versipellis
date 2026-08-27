package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/daabr/versipellis/pkg/cron"
	"github.com/daabr/versipellis/pkg/dest"
)

// CollectorType* constants represent all the available types of "collector" configurations in the TOML file.
const (
	CollectorTypeNone  = "none"
	CollectorTypeHTTP  = "http"
	CollectorTypeHTTP3 = "http/3"
	CollectorTypeSQL   = "sql"
)

var validCollectorTypes = []string{
	CollectorTypeNone,
	CollectorTypeHTTP,
	CollectorTypeHTTP3,
	CollectorTypeSQL,
}

// BaseCollector contains the basic details of any "collector" configuration in the TOML file.
type BaseCollector struct {
	Type string

	Cronspec string
	Schedule *cron.Schedule
	Trigger  string // Not fully implemented yet, but reserved for future use.

	Destination string
	Sender      dest.Sender
}

// NewBaseCollector creates a new [BaseCollector] from the given configuration, which was read
// from a TOML file. It checks the details and returns an error if any of them is invalid.
func NewBaseCollector(cfg map[string]any) (*BaseCollector, error) {
	c := &BaseCollector{
		Type:        strings.ToLower(strings.TrimSpace(Value(cfg, "type", CollectorTypeNone))),
		Cronspec:    Value(cfg, "schedule", ""),
		Trigger:     Value(cfg, "trigger", ""),
		Destination: strings.ToLower(strings.TrimSpace(Value(cfg, "destination", ""))),
	}
	var senderFound bool
	c.Sender, senderFound = dest.Senders[c.Destination]

	switch {
	case !slices.Contains(validCollectorTypes, c.Type):
		return nil, fmt.Errorf("unrecognized collector type %q", c.Type)
	case !senderFound:
		return nil, fmt.Errorf("unrecognized destination %q", c.Destination)
	case c.Type == CollectorTypeNone && c.Cronspec == "" && c.Trigger == "" && c.Sender == nil:
		return c, nil
	case c.Type == CollectorTypeNone: // Cronspec != "" || Trigger != "" || Sender != nil.
		return nil, errors.New("collector configuration of type 'none' cannot have a schedule, a trigger, or a destination")
	case c.Cronspec != "" && c.Trigger != "":
		return nil, errors.New("collector configuration cannot have both a schedule and a trigger")
	case c.Trigger != "":
		return c, nil
	case c.Cronspec == "":
		return nil, errors.New("collector configuration must have either a schedule or a trigger")
	}

	tz, name := LoadLocation(Value(cfg, "timezone", "UTC"))
	if tz == nil {
		return nil, fmt.Errorf("invalid time zone %q", name)
	}
	sched, err := cron.Parse(c.Cronspec, tz)
	if err != nil {
		return nil, fmt.Errorf("invalid expression in collector schedule: %w", err)
	}
	c.Cronspec = fmt.Sprintf("TZ=%s %s", name, c.Cronspec)
	if !sched.RunsOnlyOnce() && sched.Next(time.Now()).IsZero() {
		return nil, fmt.Errorf("collector schedule %q will never run", c.Cronspec)
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
