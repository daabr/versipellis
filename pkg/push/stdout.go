// Package push provides trivial targets to push output data to, for demo and testing purposes.
// Other I/O protocol-specific pushers are provided in the packages implementing those protocols.
package push

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
)

var (
	// Synchronize all [Stdout] calls, unlike other pushers, to prevent concurrent callers
	// from interleaving their output mid-line (because [os.Stdout] is a shared resource).
	mu sync.Mutex

	once    sync.Once             // Irrelevant in tests.
	writer  io.Writer = os.Stdout // For testing purposes only.
	encoder *json.Encoder
)

// Stdout prints any input data to [os.Stdout]. Simple data types are printed as-is, while
// complex structures are encoded as JSON, if possible. Because this pusher is intended for
// demo and testing purposes, it is guaranteed to be concurrency-safe but not necessarily
// performant. For the same reason, JSON encoding errors are logged, but not exposed.
func Stdout(data any) error {
	mu.Lock()
	defer mu.Unlock()

	once.Do(lazyInit)

	if err := encoder.Encode(data); err != nil {
		slog.Error("cannot encode data", slog.Any("error", err), slog.String("data_type", fmt.Sprintf("%T", data)))
		// Log this error, but...
	}

	// ...Never let specific pusher interrupt or abort data flow.
	return nil
}

func lazyInit() {
	encoder = json.NewEncoder(writer)
	encoder.SetEscapeHTML(false) // Passing raw data, not rendering it, so don't alter it.
}
