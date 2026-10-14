package config

import (
	"context"

	"github.com/daabr/versipellis/pkg/flow"
)

// SenderType* constants represent all the available types of "sender" sections in the TOML file.
const (
	SenderTypeDiscard = "discard"
	SenderTypeNone    = "none"
	SenderTypeStdout  = "stdout"
	SenderTypeDLQ     = "dead_letter_queue"

	SenderTypeHTTP  = "http"
	SenderTypeHTTP3 = "http3"
)

// Format is the common Go type of all "Format*" constants in the "codec" package.
type Format string

// Sender is an interface to send arbitrary data to some kind of destination.
// Implementations are responsible for managing state, resources, and cleanup.
// It is supposed to be asynchronous and does not return a status/result, to decouple
// the sending process from callers, but it does support graceful, time-bounded shutdown.
type Sender interface {
	Send(ctx context.Context, data flow.Chunk, format Format)
	Close(ctx context.Context)
}

// SendFunc is a Send() function belonging to any implementation of the [Sender] interface.
type SendFunc func(ctx context.Context, data flow.Chunk, format Format)
