package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ReceiverType* constants represent all the available types of "receiver" configurations in the TOML file.
const (
	ReceiverTypeHTTP  = "http"
	ReceiverTypeHTTP3 = "http3"
)

var validReceiverTypes = []string{
	ReceiverTypeHTTP,
	ReceiverTypeHTTP3,
}

// BaseReceiver contains the basic details of any "receiver" configuration in the TOML file.
type BaseReceiver struct {
	Type string
	Name string

	Destination string
	Sender      Sender
}

// NewBaseReceiver creates a new [BaseReceiver] from the given configuration, which was read from a TOML file. It checks
// these details and returns an error if any of them is invalid, but the caller is responsible for providing non-nil input.
func NewBaseReceiver(cfg map[string]any, name string, senders map[string]Sender) (*BaseReceiver, error) {
	r := &BaseReceiver{
		Type:        strings.ToLower(strings.TrimSpace(Value(cfg, "type", ""))),
		Name:        name,
		Destination: strings.TrimSpace(Value(cfg, "destination", "")), // Attention: case sensitive!
	}
	var senderFound bool
	r.Sender, senderFound = senders[r.Destination]

	switch {
	case r.Type == "":
		return nil, errors.New("type field required but not found")
	case !slices.Contains(validReceiverTypes, r.Type):
		return nil, fmt.Errorf("unrecognized type %q", r.Type)
	case !senderFound:
		return nil, fmt.Errorf("unrecognized destination %q", r.Destination)
	}

	return r, nil
}
