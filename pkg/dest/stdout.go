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

func lazyInit() {
	encoder = json.NewEncoder(writer)
	encoder.SetEscapeHTML(false) // Passing raw data, not rendering it, so don't alter it.
}

// Stdout prints any input data to [os.Stdout]. Simple data types are printed as-is, while complex structures
// are encoded as JSON, if possible. Some types (e.g., HTTP requests and responses) have special handling.
// Because this destination is intended for demo and testing purposes, it is guaranteed to be concurrency-safe
// but not necessarily performant. For the same reason, JSON encoding errors are logged, but not exposed.
func Stdout(_ context.Context, data any) {
	if data == nil {
		return // Don't log nil data, other senders use it as a sentinel for batches.
	}

	mu.Lock()
	defer mu.Unlock()

	once.Do(lazyInit)

	var err error
	switch r := data.(type) {
	case *http.Request:
		if r == nil {
			return
		}
		err = r.Write(writer)
		if r.Body != nil {
			_ = r.Body.Close()
		}

	case *http.Response:
		if r == nil {
			return
		}
		err = r.Write(writer)
		if r.Body != nil {
			_ = r.Body.Close()
		}

	default:
		err = encoder.Encode(data)
	}

	if err != nil {
		slog.Error("cannot encode or print data", slog.Any("error", err), slog.String("data_type", fmt.Sprintf("%T", data)))
	}
}
