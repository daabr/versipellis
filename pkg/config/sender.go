package config

import (
	"context"
)

// SenderType* constants represent all the available types of "sender" configurations in the TOML file.
const (
	SenderTypeDiscard = "discard"
	SenderTypeNone    = "none"
	SenderTypeStdout  = "stdout"
	SenderTypeDLQ     = "dead_letter_queue"

	SenderTypeHTTP  = "http"
	SenderTypeHTTP3 = "http3"
)

// Sender is a function that sends any type of data to a destination.
// It also closes any resources associated with the data, if applicable.
// It is intentionally asynchronous and does not return a status/result.
// Reminder: convert this into an interface, with a time-bounded Close() in a future PR.
type Sender func(context.Context, any)
