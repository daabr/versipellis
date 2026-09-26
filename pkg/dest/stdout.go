package dest

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Stdout prints any input data to [os.Stdout]. Simple data types are printed as-is, complex structures
// are encoded as JSON, if possible. Some types (e.g., HTTP requests and responses) have specific logic.
// Because this is intended for demo and testing purposes, it is guaranteed to be concurrency-safe, but
// not necessarily performant. For the same reason, JSON encoding errors are logged, but not exposed.
var Stdout = new(stdoutSender{writer: os.Stdout})

type stdoutSender struct {
	writer io.Writer

	// Synchronize all Send calls, to prevent concurrent callers from interleaving their output mid-line.
	// [os.Stdout] is a shared resource, it doesn't have built-in concurrency like other destinations.
	mu sync.Mutex

	lameDuck  atomic.Bool
	closeOnce sync.Once
}

func (s *stdoutSender) Send(ctx context.Context, data any) {
	if data == nil {
		return // Don't log nil data, other senders use it as a sentinel for batches.
	}

	if s.lameDuck.Load() {
		Discard.Send(ctx, data)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lameDuck.Load() {
		Discard.Send(ctx, data)
		return
	}

	var err error
	switch t := data.(type) {
	case *http.Request:
		if t == nil {
			return
		}
		err = t.Write(s.writer)
		if t.Body != nil {
			_ = t.Body.Close()
		}

	case *http.Response:
		if t == nil {
			return
		}
		err = t.Write(s.writer)
		if t.Body != nil {
			_ = t.Body.Close()
		}

	case []map[string]any:
		var buf []byte
		for _, m := range t {
			buf, err = json.Marshal(m, jsonOpts)
			if err == nil {
				_, err = s.writer.Write(append(buf, '\n'))
			}
			if err != nil {
				break
			}
		}

	default:
		var buf []byte
		buf, err = json.Marshal(data, jsonOpts)
		if err == nil {
			_, err = s.writer.Write(append(buf, '\n'))
		}
	}

	if err != nil {
		slog.Error("cannot encode or print data", slog.Any("error", err), slog.String("data_type", fmt.Sprintf("%T", data)))
	}
}

// Close waits (up to 1 second) for data to be printed to [os.Stdout], and prevents new data from being printed.
func (s *stdoutSender) Close(ctx context.Context) {
	s.closeOnce.Do(func() {
		s.lameDuck.Store(true)

		shutdownCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()

		done := make(chan struct{})
		go func() {
			s.mu.Lock()
			close(done)
			s.mu.Unlock()
		}()

		select {
		case <-done:
			// All done.
		case <-shutdownCtx.Done():
			slog.Error("closing stdout sender forcefully", slog.Any("error", shutdownCtx.Err()))
		}
	})
}
