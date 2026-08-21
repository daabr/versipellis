package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/daabr/versipellis/internal/cron"
)

// PullType* constants represent all the available types of "pull" configurations in the TOML file.
const (
	PullTypeNone  = "none"
	PullTypeHTTP  = "http"
	PullTypeHTTP3 = "http/3"
	PullTypeSQL   = "sql"
)

var validPullTypes = []string{
	PullTypeNone,
	PullTypeHTTP,
	PullTypeHTTP3,
	PullTypeSQL,
}

// BasePuller contains the basic details of any "pull" configuration in the TOML file.
type BasePuller struct {
	Type string

	Cronspec string
	Schedule *cron.Schedule

	Trigger string
}

// NewBasePuller creates a new [BasePuller] from the given configuration, which was read
// from a TOML file. It checks the details and returns an error if any of them is invalid.
func NewBasePuller(cfg map[string]any) (*BasePuller, error) {
	p := &BasePuller{
		Type:     strings.ToLower(strings.TrimSpace(Value(cfg, "type", PullTypeNone))),
		Cronspec: Value(cfg, "schedule", ""),
		Trigger:  Value(cfg, "trigger", ""),
	}

	switch {
	case !slices.Contains(validPullTypes, p.Type):
		return nil, fmt.Errorf("unrecognized pull type %q", p.Type)
	case p.Type == PullTypeNone && p.Cronspec == "" && p.Trigger == "":
		return p, nil
	case p.Type == PullTypeNone: // Cronspec != "" || Trigger != "".
		return nil, errors.New("pull configuration of type 'none' cannot have a schedule or a trigger")
	case p.Cronspec != "" && p.Trigger != "":
		return nil, errors.New("pull configuration cannot have both a schedule and a trigger")
	case p.Trigger != "":
		return p, nil
	case p.Cronspec == "":
		return nil, errors.New("pull configuration must have either a schedule or a trigger")
	}

	tz, name := LoadLocation(Value(cfg, "timezone", "UTC"))
	if tz == nil {
		return nil, fmt.Errorf("invalid time zone %q", name)
	}
	sched, err := cron.Parse(p.Cronspec, tz)
	if err != nil {
		return nil, fmt.Errorf("invalid expression in pull schedule: %w", err)
	}
	p.Cronspec = fmt.Sprintf("TZ=%s %s", name, p.Cronspec)
	if !sched.RunsOnlyOnce() && sched.Next(time.Now()).IsZero() {
		return nil, fmt.Errorf("pull schedule %q will never run", p.Cronspec)
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
