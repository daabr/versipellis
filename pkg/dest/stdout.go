package dest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/daabr/versipellis/pkg/flow"
)

var (
	// Stdout prints any input data to [os.Stdout]. Simple data types are printed as-is, complex structures
	// are encoded as JSON, if possible. Some types (e.g., HTTP requests and responses) have specific logic.
	// Because this is intended for demo and testing purposes, it is guaranteed to be concurrency-safe, but
	// not necessarily performant. For the same reason, JSON encoding errors are logged, but not exposed.
	Stdout = new(stdoutSender{writer: os.Stdout})

	newline = []byte{'\n'}
)

type stdoutSender struct {
	writer io.Writer

	// Synchronize all Send calls, to prevent concurrent callers from interleaving their output mid-line.
	// [os.Stdout] is a shared resource, it doesn't have built-in concurrency like other destinations.
	mu sync.Mutex

	lameDuck  atomic.Bool
	closeOnce sync.Once
}

func (s *stdoutSender) Send(ctx context.Context, data flow.Chunk) {
	if flow.IsEmpty(data) {
		return
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
	switch chunk := data.(type) {
	case flow.Blobs:
		for _, b := range chunk {
			if len(b) > 0 {
				_, werr := s.writer.Write(b)
				if werr == nil {
					_, werr = s.writer.Write(newline)
				}
				err = errors.Join(err, werr)
			}
		}
	case flow.Structured:
		err = flow.FormatNDJSON.Print(s.writer, chunk) // More readable than [flow.Format.Encode].

	case flow.HTTPRequests:
		for _, req := range chunk {
			if req == nil {
				continue
			}
			err = errors.Join(err, req.Write(s.writer))
			if req.Body != nil {
				_ = req.Body.Close()
			}
		}
	case flow.HTTPResponses:
		for _, resp := range chunk {
			if resp == nil {
				continue
			}
			err = errors.Join(err, resp.Write(s.writer))
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
		}
	default:
		err = errors.New("unhandled data type")
	}

	if err != nil {
		slog.Error("cannot encode or print data", slog.Any("error", err),
			slog.String("data_type", fmt.Sprintf("%T", data)),
		)
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
		case <-shutdownCtx.Done(): // Shouldn't happen.
			slog.Error("closing stdout sender forcefully", slog.Any("error", shutdownCtx.Err()))
		}
	})
}
