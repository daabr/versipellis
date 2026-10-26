package dest

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/flow"
)

var (
	// Stdout prints any input data to [os.Stdout]. Simple data types are printed as-is, complex
	// structures are encoded as readable NDJSON, if possible, regardless of the requested format.
	// Some types (e.g., HTTP requests and responses) have specific logic.
	//
	// Because this is intended primarily for demos and testing, it is concurrency-safe, but not necessarily
	// robust or performant. For the same reason, JSON encoding errors are logged, but not exposed. Unlike other
	// [config.Sender]s, Send() writes inline, so a stalled pipe can block its callers, including batcher guards
	// and collector shutdowns. That's acceptable here, and logs that share stdout would block them anyway.
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

func (s *stdoutSender) Send(ctx context.Context, data flow.Chunk, format config.Format) {
	if flow.IsEmpty(data) {
		return
	}

	if s.lameDuck.Load() {
		Discard.Send(ctx, data, format)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lameDuck.Load() {
		Discard.Send(ctx, data, format)
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
	case flow.JSON:
		err = printJSON(s.writer, chunk)
	case flow.Rows:
		err = printRows(s.writer, chunk)
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
		case <-shutdownCtx.Done():
			slog.Error("closing stdout sender forcefully", slog.Any("error", shutdownCtx.Err()))
		}
	})
}

// printJSON prints one or more JSON objects as NDJSON, i.e. separated by newlines. In addition
// to that, it prioritizes readability over fidelity, which means that each object is reformatted,
// and strings use their shortest encoding (e.g., "\u00e9" becomes "é"). Invalid objects are skipped
// (but they shouldn't exist, because [flow.JSON] objects are created from valid data).
// For all these reasons, we don't use the encoders of the "codec" package here.
func printJSON(w io.Writer, objects flow.JSON) error {
	buf := new(bytes.Buffer)
	enc := jsontext.NewEncoder(buf, readableJSONOpts)
	for i, obj := range objects {
		// If the object is invalid, the encoder's state remains unchanged, so there's no partial output.
		if err := enc.WriteValue(obj); err != nil {
			slog.Warn("skipping invalid JSON object", slog.Any("error", err), slog.Int("index", i))
		}
	}

	if buf.Len() > 0 {
		if _, err := w.Write(buf.Bytes()); err != nil {
			return fmt.Errorf("write error: %w", err)
		}
	}
	return nil
}

// printRows prints tabular rows (e.g., SQL query results) as NDJSON. It prioritizes readability
// over fidelity like [printJSON], and in addition to that it allows duplicate column names and
// invalid UTF-8 characters, and byte values are represented as strings if they're printable.
func printRows(w io.Writer, rows flow.Rows) error {
	buf := new(bytes.Buffer)
	for i, row := range rows {
		// Marshal each row separately instead of using a shared encoder,
		// so failures don't leave partial output in the buffer.
		b, err := marshalRow(row.Cols, row.Vals)
		if err != nil {
			slog.Warn("skipping invalid row", slog.Any("error", err), slog.Int("index", i))
			continue
		}
		buf.Write(b)
	}

	if buf.Len() > 0 {
		if _, err := w.Write(buf.Bytes()); err != nil {
			return fmt.Errorf("write error: %w", err)
		}
	}
	return nil
}

// readableJSONOpts combines [jsontext.Options], which format all the output (raw JSON objects and rows alike),
// with [json.Options], which affect only the marshaling of Go values (i.e. row values), so they're ignored when
// raw JSON objects are written. Both packages share the same options type, so [json.JoinOptions] works for all.
var readableJSONOpts = json.JoinOptions(
	jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true),
	jsontext.SpaceAfterColon(true), jsontext.SpaceAfterComma(true),
	json.Deterministic(true), json.WithMarshalers(json.JoinMarshalers(
		json.MarshalToFunc(marshalPrintableBytesAsString), codec.NonFiniteFloats,
	)),
)

func marshalRow(names []string, vals []any) ([]byte, error) {
	if len(names) != len(vals) {
		return nil, fmt.Errorf("row has %d columns but %d values", len(names), len(vals))
	}

	buf := new(bytes.Buffer)
	enc := jsontext.NewEncoder(buf, readableJSONOpts)
	_ = enc.WriteToken(jsontext.BeginObject)
	for i, name := range names {
		_ = enc.WriteToken(jsontext.String(name))
		if err := json.MarshalEncode(enc, vals[i]); err != nil {
			return nil, fmt.Errorf("JSON value encoding error in column %q: %w", name, err)
		}
	}

	_ = enc.WriteToken(jsontext.EndObject)
	return buf.Bytes(), nil
}

func marshalPrintableBytesAsString(enc *jsontext.Encoder, data []byte) error {
	// Failure: fall back to the default encoding (Base64).
	if !utf8.Valid(data) {
		return errors.ErrUnsupported
	}
	s := string(data)
	for _, r := range s {
		if !unicode.IsPrint(r) && !unicode.IsSpace(r) {
			return errors.ErrUnsupported
		}
	}

	// Success: printable UTF-8 bytes can be written as a regular string.
	_ = enc.WriteToken(jsontext.String(s))
	return nil
}
