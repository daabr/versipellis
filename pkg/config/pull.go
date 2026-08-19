package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/daabr/versipellis/internal/cron"
)

// PullType* constants represent all the different types of "pull" configurations in the TOML file.
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

// GenericPuller contains the generic details of any "pull" configuration in the TOML file.
type GenericPuller struct {
	Type     string
	Cronspec string
	Trigger  string
	Schedule *cron.Schedule
}

// NewGenericPuller creates a new [GenericPuller] from the given configuration, which was read
// from a TOML file. It checks the details and returns an error if any of them is invalid.
func NewGenericPuller(config map[string]any) (*GenericPuller, error) {
	p := &GenericPuller{
		Type:     strings.ToLower(strings.TrimSpace(value(config, "type", PullTypeNone))),
		Cronspec: value(config, "schedule", ""),
		Trigger:  value(config, "trigger", ""),
	}

	switch {
	case !slices.Contains(validPullTypes, p.Type):
		return nil, errors.New("unrecognized pull type: " + p.Type)
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

	tz, name := LoadLocation(value(config, "timezone", "UTC"))
	if tz == nil {
		return nil, errors.New("invalid time zone: " + name)
	}
	sched, err := cron.Parse(p.Cronspec, tz)
	if err != nil {
		return nil, fmt.Errorf("invalid expression in pull schedule: %w", err)
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
		return time.Local, time.Local.String() //nolint:gosmopolitan // Intentional option.
	}

	if loc, err := time.LoadLocation(timezone); err == nil {
		return loc, timezone
	}
	return nil, timezone
}
