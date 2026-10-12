package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/flow"
)

func recordSends(t *testing.T) (config.SendFunc, func() []string) {
	t.Helper()

	var mu sync.Mutex
	var sent []string
	send := func(_ context.Context, data flow.Chunk, format config.Format) {
		desc := fmt.Sprintf("%s (%s)", describeChunk(t, data), format)
		mu.Lock()
		sent = append(sent, desc)
		mu.Unlock()
	}

	return send, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(sent)
	}
}

func describeChunk(t *testing.T, data flow.Chunk) string {
	t.Helper()

	switch chunk := data.(type) {
	case flow.Blobs:
		return "blobs: " + string(bytes.Join(chunk, []byte(" | ")))

	case flow.JSON:
		b, err := codec.JSONArray.EncodeJSON(chunk)
		if err != nil {
			t.Errorf("failed to encode JSON chunk: %v", err)
		}
		return "json: " + string(b)

	case flow.HTTPRequests:
		var bodies []string
		for _, req := range chunk {
			if req.Body == nil {
				bodies = append(bodies, "<nil>")
				continue
			}
			b, err := io.ReadAll(req.Body)
			if err != nil {
				t.Errorf("failed to read request body: %v", err)
			}
			bodies = append(bodies, string(b))
		}
		return "requests: " + strings.Join(bodies, " | ")

	case flow.HTTPResponses:
		var bodies []string
		for _, resp := range chunk {
			b, err := io.ReadAll(resp.Body)
			if err != nil {
				b = fmt.Appendf(nil, "<%v>", err) // Unexpected errors still fail tests, because they're compared.
			}
			bodies = append(bodies, string(b))
		}
		return "responses: " + strings.Join(bodies, " | ")

	case flow.Rows:
		var rows []string
		for _, row := range chunk {
			rows = append(rows, fmt.Sprintf("%q=%q", row.Cols, row.Vals))
		}
		return "rows: " + strings.Join(rows, " | ")

	default:
		t.Errorf("unexpected chunk type %T", data)
		return ""
	}
}

func TestBodyChunk(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		format  config.Format
		want    string // Described chunk, empty if nil.
		wantErr bool
	}{
		{
			name:   "empty_raw",
			format: codec.FormatRaw,
		},
		{
			name:   "empty_json",
			format: codec.FormatJSON,
		},
		{
			name:   "raw",
			body:   "not JSON",
			format: codec.FormatRaw,
			want:   "blobs: not JSON",
		},
		{
			name:   "json_object",
			body:   `{"a":1}`,
			format: codec.FormatJSON,
			want:   `json: [{"a":1}]`,
		},
		{
			name:   "ndjson",
			body:   `{"a":1}` + "\n" + `{"b":2}`,
			format: codec.FormatNDJSON,
			want:   `json: [{"a":1},{"b":2}]`,
		},
		{
			name:   "empty_array",
			body:   `[]`,
			format: codec.FormatJSONArray,
		},
		{
			name:    "invalid_json",
			body:    `{"a":`,
			format:  codec.FormatJSON,
			wantErr: true,
		},
		{
			name:    "unexpected_default_format",
			body:    `{"a":1}`,
			format:  codec.FormatDefault,
			wantErr: true,
		},
		{
			name:   "json_objects_kept_verbatim",
			body:   `[{"z":1, "a":2}]`,
			format: codec.FormatJSON,
			want:   `json: [{"z":1,"a":2}]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := bodyChunk([]byte(tt.body), tt.format)
			if (err != nil) != tt.wantErr {
				t.Errorf("bodyChunk() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got == nil {
				if tt.want != "" {
					t.Errorf("bodyChunk() = nil, want %s", tt.want)
				}
				return
			}
			if desc := describeChunk(t, got); desc != tt.want {
				t.Errorf("bodyChunk() = %s, want %s", desc, tt.want)
			}
		})
	}
}

func TestReceiverSendRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		format   config.Format
		body     string
		noDLQ    bool
		wantSent []string
		wantDLQ  []string
		wantFail bool
	}{
		{
			name:     "default_format",
			format:   codec.FormatDefault,
			body:     "payload",
			wantSent: []string{"requests: payload (default)"},
		},
		{
			name:     "raw_format",
			format:   codec.FormatRaw,
			body:     "payload",
			wantSent: []string{"blobs: payload (raw)"},
		},
		{
			name:     "json_format",
			format:   codec.FormatJSON,
			body:     `[{"a":1},{"b":2}]`,
			wantSent: []string{`json: [{"a":1},{"b":2}] (json)`},
		},
		{
			name:   "empty_body",
			format: codec.FormatNDJSON,
		},
		{
			name:     "invalid_json",
			format:   codec.FormatNDJSON,
			body:     "not JSON",
			wantDLQ:  []string{"requests: not JSON (default)"},
			wantFail: true,
		},
		{
			name:     "invalid_json_without_dlq",
			format:   codec.FormatNDJSON,
			body:     "not JSON",
			noDLQ:    true,
			wantFail: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			send, sent := recordSends(t)
			dlq, dlqSent := recordSends(t)
			if tt.noDLQ {
				dlq = nil
			}
			r := &Receiver{Name: tt.name, Send: send, Fallback: dlq, format: tt.format}

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(tt.body))
			if ok := r.sendRequest(req, []byte(tt.body)); ok == tt.wantFail {
				t.Errorf("sendRequest() = %v, want %v", ok, !tt.wantFail)
			}

			if got := sent(); !slices.Equal(got, tt.wantSent) {
				t.Errorf("sent = %q, want %q", got, tt.wantSent)
			}
			if got := dlqSent(); !slices.Equal(got, tt.wantDLQ) {
				t.Errorf("sent to DLQ = %q, want %q", got, tt.wantDLQ)
			}
		})
	}
}

func TestCollectorSendResponse(t *testing.T) {
	t.Parallel()

	reusable := func(s string) io.ReadCloser {
		return &reusableBody{Reader: bytes.NewReader([]byte(s)), raw: []byte(s)}
	}

	tests := []struct {
		name     string
		format   config.Format
		body     io.ReadCloser
		wantSent []string
		wantDLQ  []string
	}{
		{
			name:     "default_format",
			format:   codec.FormatDefault,
			body:     reusable("payload"),
			wantSent: []string{"responses: payload (default)"},
		},
		{
			name:     "raw_format",
			format:   codec.FormatRaw,
			body:     reusable("payload"),
			wantSent: []string{"blobs: payload (raw)"},
		},
		{
			name:     "json_array_format",
			format:   codec.FormatJSONArray,
			body:     reusable(`{"a":1}`),
			wantSent: []string{`json: [{"a":1}] (json_array)`},
		},
		{
			name:     "non_reusable_body",
			format:   codec.FormatJSON,
			body:     io.NopCloser(strings.NewReader(`{"a":1}`)),
			wantSent: []string{`json: [{"a":1}] (json)`},
		},
		{
			name:   "nil_body",
			format: codec.FormatJSON,
		},
		{
			name:    "invalid_json",
			format:  codec.FormatNDJSON,
			body:    reusable("not JSON"),
			wantDLQ: []string{"responses: not JSON (default)"},
		},
		{
			name:    "invalid_json_in_non_reusable_body",
			format:  codec.FormatNDJSON,
			body:    io.NopCloser(strings.NewReader("not JSON")),
			wantDLQ: []string{"responses: not JSON (default)"},
		},
		{
			name:    "body_read_error",
			format:  codec.FormatJSON,
			body:    io.NopCloser(iotest.ErrReader(errors.New("read error"))),
			wantDLQ: []string{"responses: <read error> (default)"}, // The original body is lost, see [responseBody].
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			send, sent := recordSends(t)
			dlq, dlqSent := recordSends(t)
			base := config.BaseReceiver{Name: tt.name, Send: send, Fallback: dlq}
			c := &Collector{BaseReceiver: base, format: tt.format}

			c.sendResponse(t.Context(), &http.Response{StatusCode: http.StatusOK, Body: tt.body})

			if got := sent(); !slices.Equal(got, tt.wantSent) {
				t.Errorf("sent = %q, want %q", got, tt.wantSent)
			}
			if got := dlqSent(); !slices.Equal(got, tt.wantDLQ) {
				t.Errorf("sent to DLQ = %q, want %q", got, tt.wantDLQ)
			}
		})
	}
}
