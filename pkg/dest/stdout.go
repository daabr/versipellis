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
	if req, ok := data.(*http.Request); ok && req != nil {
		err = req.Write(writer)
		if req.Body != nil {
			_ = req.Body.Close()
		}
	} else if resp, ok := data.(*http.Response); ok && resp != nil {
		err = resp.Write(writer)
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	} else {
		err = encoder.Encode(data)
	}

	if err != nil {
		slog.Error("cannot encode or print data", slog.Any("error", err), slog.String("data_type", fmt.Sprintf("%T", data)))
		// Log this kind of error, but...
	}

	// ...Never let this specific destination interrupt or abort data flow.
	return nil
}

func lazyInit() {
	encoder = json.NewEncoder(writer)
	encoder.SetEscapeHTML(false) // Passing raw data, not rendering it, so don't alter it.
}
