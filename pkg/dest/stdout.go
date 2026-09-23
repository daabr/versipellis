package dest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
)

// Stdout prints any input data to [os.Stdout]. Simple data types are printed as-is, complex structures
// are encoded as JSON, if possible. Some types (e.g., HTTP requests and responses) have specific logic.
// Because this is intended for demo and testing purposes, it is guaranteed to be concurrency-safe, but
// not necessarily performant. For the same reason, JSON encoding errors are logged, but not exposed.
var Stdout = newStdout(os.Stdout)

type stdoutSender struct {
	w io.Writer
	e *json.Encoder

	// Synchronize all Send calls, to prevent concurrent callers from interleaving their output mid-line.
	// [os.Stdout] is a shared resource, it doesn't have built-in concurrency like other destinations.
	mu sync.Mutex
}

func newStdout(w io.Writer) *stdoutSender {
	s := &stdoutSender{w: w, e: json.NewEncoder(w)}
	s.e.SetEscapeHTML(false) // Passing raw data, not rendering it, so don't alter it.
	return s
}

func (s *stdoutSender) Send(_ context.Context, data any) {
	if data == nil {
		return // Don't log nil data, other senders use it as a sentinel for batches.
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var err error
	switch t := data.(type) {
	case *http.Request:
		if t == nil {
			return
		}
		err = t.Write(s.w)
		if t.Body != nil {
			_ = t.Body.Close()
		}

	case *http.Response:
		if t == nil {
			return
		}
		err = t.Write(s.w)
		if t.Body != nil {
			_ = t.Body.Close()
		}

	case []map[string]any:
		for _, m := range t {
			err = errors.Join(err, s.e.Encode(m))
			if err != nil {
				break
			}
		}

	default:
		err = s.e.Encode(data)
	}

	if err != nil {
		slog.Error("cannot encode or print data", slog.Any("error", err), slog.String("data_type", fmt.Sprintf("%T", data)))
	}
}

func (*stdoutSender) Close(context.Context) {}
