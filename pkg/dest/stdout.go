package dest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
)

var (
	// Synchronize all [Stdout] calls, unlike other destinations which support concurrency, to prevent
	// concurrent callers from interleaving their output mid-line (because [os.Stdout] is a shared resource).
	mu sync.Mutex

	once    sync.Once             // Irrelevant in tests.
	writer  io.Writer = os.Stdout // For testing purposes only.
	encoder *json.Encoder
)

// Stdout prints any input data to [os.Stdout]. Simple data types are printed as-is, while complex structures
// are encoded as JSON, if possible. Some types (e.g., HTTP requests and responses) have special handling.
// Because this destination is intended for demo and testing purposes, it is guaranteed to be concurrency-safe
// but not necessarily performant. For the same reason, JSON encoding errors are logged, but not exposed.
func Stdout(_ context.Context, data any) error {
	if data == nil {
		return nil // Don't log nil data, other senders may use it as a sentinel marking end-of-batch.
	}

	mu.Lock()
	defer mu.Unlock()

	once.Do(lazyInit)

	var err error
	switch v := data.(type) {
	case *http.Request:
		err = v.Write(writer)
		if v.Body != nil {
			_ = v.Body.Close()
		}
	case *http.Response:
		err = v.Write(writer)
		if v.Body != nil { // Never nil - see Collector.processResponse() in pkg/http/client.go - but just in case.
			_ = v.Body.Close()
		}
	default:
		err = encoder.Encode(data)
	}

	if err != nil {
		slog.Error("cannot encode data", slog.Any("error", err), slog.String("data_type", fmt.Sprintf("%T", data)))
		// Log this kind of error, but...
	}

	// ...Never let this specific destination interrupt or abort data flow.
	return nil
}

func lazyInit() {
	encoder = json.NewEncoder(writer)
	encoder.SetEscapeHTML(false) // Passing raw data, not rendering it, so don't alter it.
}
