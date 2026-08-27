// Package dest provides trivial destinations to send output data to, for demo and testing purposes.
// Other I/O protocol-specific senders are provided in the packages implementing those protocols.
package dest

import (
	"context"
)

// Sender is a function that sends any type of data to a destination,
// and returns an error if the sending is considered to have failed.
type Sender func(context.Context, any) error

// Senders is a mapping of recognized destination names to their [Sender] functions.
// Nil is a valid value in this map, indicating a no-op (i.e., the data is discarded).
var Senders = map[string]Sender{
	"":        nil,
	"discard": nil,
	"none":    nil,

	"stdout": Stdout,
}
