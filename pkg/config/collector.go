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

	Trigger string // Not fully implemented yet, but reserved for future use.
}

// NewBaseCollector creates a new [BaseCollector] from the given configuration, which was read
// from a TOML file. It checks the details and returns an error if any of them is invalid.
func NewBaseCollector(cfg map[string]any) (*BaseCollector, error) {
	p := &BaseCollector{
		Type:     strings.ToLower(strings.TrimSpace(Value(cfg, "type", CollectorTypeNone))),
		Cronspec: Value(cfg, "schedule", ""),
		Trigger:  Value(cfg, "trigger", ""),
	}

	switch {
	case !slices.Contains(validCollectorTypes, p.Type):
		return nil, fmt.Errorf("unrecognized collector type %q", p.Type)
	case p.Type == CollectorTypeNone && p.Cronspec == "" && p.Trigger == "":
		return p, nil
	case p.Type == CollectorTypeNone: // Cronspec != "" || Trigger != "".
		return nil, errors.New("collector configuration of type 'none' cannot have a schedule or a trigger")
	case p.Cronspec != "" && p.Trigger != "":
		return nil, errors.New("collector configuration cannot have both a schedule and a trigger")
	case p.Trigger != "":
		return p, nil
	case p.Cronspec == "":
		return nil, errors.New("collector configuration must have either a schedule or a trigger")
	}

	tz, name := LoadLocation(Value(cfg, "timezone", "UTC"))
	if tz == nil {
		return nil, fmt.Errorf("invalid time zone %q", name)
	}
	sched, err := cron.Parse(p.Cronspec, tz)
	if err != nil {
		return nil, fmt.Errorf("invalid expression in collector schedule: %w", err)
	}
	p.Cronspec = fmt.Sprintf("TZ=%s %s", name, p.Cronspec)
	if !sched.RunsOnlyOnce() && sched.Next(time.Now()).IsZero() {
		return nil, fmt.Errorf("collector schedule %q will never run", p.Cronspec)
	}

	p.Schedule = sched
	return p, nil
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
