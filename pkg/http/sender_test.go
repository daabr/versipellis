package http

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/dest"
	"github.com/daabr/versipellis/pkg/flow"
)

func TestNewSender(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      map[string]any
		baseType string
		wantErr  bool
	}{
		{
			name:     "nil_config",
			cfg:      nil,
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "missing_url",
			cfg:      map[string]any{},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "invalid_url",
			cfg:      map[string]any{"url": "not-a-url"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "invalid_method",
			cfg:      map[string]any{"url": "https://example.com", "method": "BLAH"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "get_method_not_allowed",
			cfg:      map[string]any{"url": "https://example.com", "method": "GET"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "invalid_query",
			cfg:      map[string]any{"url": "https://example.com", "query": "invalid"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "invalid_headers",
			cfg:      map[string]any{"url": "https://example.com", "headers": "invalid"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "http_url_with_tls_config",
			cfg:      map[string]any{"url": "http://example.com", "tls": map[string]any{"min_version": "1.3"}},
			baseType: config.SenderTypeHTTP,
			wantErr:  false, // Warning log.
		},
		{
			name:     "invalid_timeout",
			cfg:      map[string]any{"url": "https://example.com", "timeout": "not-a-duration"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "invalid_retries",
			cfg:      map[string]any{"url": "https://example.com", "retries": "invalid"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "invalid_tls",
			cfg:      map[string]any{"url": "https://example.com", "tls": "invalid"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "unexpected_base_type",
			cfg:      map[string]any{"url": "https://example.com/path"},
			baseType: "unexpected_type",
			wantErr:  true,
		},
		{
			name:     "invalid_batch",
			cfg:      map[string]any{"url": "https://example.com", "batch": "invalid"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "unsupported_batch_byte_limit", // No item size function, because byte size batching is out of scope.
			cfg:      map[string]any{"url": "https://example.com", "batch": map[string]any{"max_bytes": int64(1024)}},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "invalid_format",
			cfg:      map[string]any{"url": "https://example.com", "format": "invalid"},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "unsupported_raw_format",
			cfg:      map[string]any{"url": "https://example.com", "format": string(codec.FormatRaw)},
			baseType: config.SenderTypeHTTP,
			wantErr:  true,
		},
		{
			name:     "valid_minimal_config",
			cfg:      map[string]any{"url": "https://example.com/path"},
			baseType: config.SenderTypeHTTP,
			wantErr:  false,
		},
		{
			name: "valid_full_config",
			cfg: map[string]any{
				"url":     "https://example.com/path",
				"method":  http.MethodPatch,
				"timeout": "10s",
				"headers": map[string]any{
					"Authorization": "Bearer token123",
				},
				"retries": map[string]any{
					"type":         retryTypeStatic,
					"max_attempts": int64(2),
					"interval":     "100ms",
				},
				"batch": map[string]any{
					"max_items":   int64(100),
					"time_window": "500ms",
				},
				"concurrency_limit": int64(8),
			},
			baseType: config.SenderTypeHTTP,
			wantErr:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, gotErr := NewSender(tt.cfg, tt.name, tt.baseType, nil)
			if (gotErr != nil) != tt.wantErr {
				t.Errorf("NewSender() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
		})
	}
}

func TestNewSenderFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		dlq          config.Sender
		wantFallback bool
	}{
		{"without_dlq", nil, false},
		{"with_dlq", dest.Discard, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := NewSender(map[string]any{"url": "https://example.com"}, tt.name, config.SenderTypeHTTP, tt.dlq)
			if err != nil {
				t.Fatalf("NewSender() error: %v", err)
			}
			if got := s.fallback == nil; got == tt.wantFallback {
				t.Errorf("NewSender().fallback nil = %v, want %v", got, tt.wantFallback)
			}
		})
	}
}

func TestNewSenderConcurrencyLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		limit any
		want  int
	}{
		{
			name:  "default",
			limit: nil,
			want:  int(defaultConcurrencyLimit),
		},
		{
			name:  "custom",
			limit: int64(5),
			want:  5,
		},
		{
			name:  "one",
			limit: int64(1),
			want:  1,
		},
		{
			name:  "zero",
			limit: int64(0),
			want:  int(maxConcurrencyLimit),
		},
		{
			name:  "negative",
			limit: int64(-3),
			want:  int(maxConcurrencyLimit),
		},
		{
			name:  "above_max",
			limit: int64(1000),
			want:  int(maxConcurrencyLimit),
		},
		{
			name:  "wrong_type",
			limit: "8",
			want:  int(defaultConcurrencyLimit),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := map[string]any{"url": "https://example.com"}
			if tt.limit != nil {
				cfg["concurrency_limit"] = tt.limit
			}
			s, err := NewSender(cfg, tt.name, config.SenderTypeHTTP, nil)
			if err != nil {
				t.Fatalf("NewSender() error: %v", err)
			}
			if got := cap(s.slots); got != tt.want {
				t.Errorf("NewSender() concurrency limit = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNewSenderHTTP3(t *testing.T) {
	t.Parallel()

	_, err := NewSender(map[string]any{"url": "https://example.com"}, "TestNewSenderHTTP3", config.SenderTypeHTTP3, nil)
	if err != nil {
		t.Fatalf("NewSender() error = %v, wantErr %v", err, false)
	}
}

// sentRequest is an outgoing HTTP request, captured by the transport of [newRecordingSender].
type sentRequest struct {
	body   string
	header http.Header
	query  string
}

// newRecordingSender creates an HTTP [Sender] whose transport doesn't send anything over the network, but records
// all the outgoing requests instead. It also verifies that their bodies are reusable for retries and redirects.
func newRecordingSender(t *testing.T, cfg map[string]any) (*Sender, func() []sentRequest) {
	t.Helper()

	s, err := NewSender(cfg, t.Name(), config.SenderTypeHTTP, nil)
	if err != nil {
		t.Fatalf("NewSender() error = %v", err)
	}

	var mu sync.Mutex
	var sent []sentRequest
	s.client.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read request body: %v", err)
		}
		if r.GetBody == nil {
			t.Error("request body is not reusable: GetBody = nil")
		} else if rc, err := r.GetBody(); err != nil {
			t.Errorf("GetBody() error = %v", err)
		} else if retry, err := io.ReadAll(rc); err != nil || !bytes.Equal(retry, body) {
			t.Errorf("retry body = %q, error = %v, want %q", retry, err, body)
		}

		mu.Lock()
		sent = append(sent, sentRequest{body: string(body), header: r.Header, query: r.URL.RawQuery})
		mu.Unlock()

		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: http.NoBody, Header: make(http.Header)}, nil
	})

	return s, func() []sentRequest {
		mu.Lock()
		defer mu.Unlock()

		// The order of concurrent requests is non-deterministic.
		got := slices.Clone(sent)
		slices.SortFunc(got, func(a, b sentRequest) int { return strings.Compare(a.body, b.body) })
		return got
	}
}

func TestSenderSend(t *testing.T) {
	t.Parallel()

	textType := http.Header{contentTypeHeader: {"text/plain; charset=utf-8"}}
	ndjsonType := http.Header{contentTypeHeader: {codec.NDJSON.ContentType()}}

	tests := []struct {
		name    string
		headers map[string]any
		chunk   flow.Chunk
		want    []sentRequest
	}{
		{
			name:  "nil_chunk",
			chunk: nil,
		},
		{
			name:  "empty_chunk",
			chunk: flow.Blobs{},
		},
		{
			name:  "unhandled_chunk_type",
			chunk: flow.FakeDataType{},
		},
		{
			name:  "blobs",
			chunk: flow.Blobs{[]byte("pay"), []byte("load")},
			want: []sentRequest{
				{body: "load", header: textType, query: "p1=good"},
				{body: "pay", header: textType, query: "p1=good"},
			},
		},
		{
			name:  "empty_blob",
			chunk: flow.Blobs{[]byte{}, []byte("x")},
			want:  []sentRequest{{body: "x", header: textType, query: "p1=good"}},
		},
		{
			name:    "blob_with_configured_content_type",
			headers: map[string]any{contentTypeHeader: "application/octet-stream"},
			chunk:   flow.Blobs{[]byte("x")},
			want: []sentRequest{
				{body: "x", header: http.Header{contentTypeHeader: {"application/octet-stream"}}, query: "p1=good"},
			},
		},
		{
			name:  "structured",
			chunk: flow.Rows{{Cols: []string{"key1"}, Vals: []any{"value1"}}, {Cols: []string{"key2"}, Vals: []any{int64(2)}}},
			want: []sentRequest{
				{body: `{"key1":"value1"}` + "\n" + `{"key2":2}` + "\n", header: ndjsonType, query: "p1=good"},
			},
		},
		{
			name:  "structured_with_unencoded_html",
			chunk: testRow("html", "& < >"),
			want:  []sentRequest{{body: `{"html":"& < >"}` + "\n", header: ndjsonType, query: "p1=good"}},
		},
		{
			name:    "structured_with_configured_content_type",
			headers: map[string]any{contentTypeHeader: "application/json"},
			chunk:   testRow("key", "value"),
			want: []sentRequest{
				{body: `{"key":"value"}` + "\n", header: http.Header{contentTypeHeader: {"application/json"}}, query: "p1=good"},
			},
		},
		{
			name:  "structured_encoding_error",
			chunk: testRow("key", "value1", make(chan struct{})), // Channels cannot be encoded.
		},
		{
			name:  "nil_http_request",
			chunk: flow.HTTPRequests{nil},
		},
		{
			name:    "http_request_without_body",
			headers: map[string]any{"H1": "good"},
			chunk: flow.HTTPRequests{{
				Header: http.Header{"H1": {"bad"}, "H2": {"good"}},
				URL:    &url.URL{RawQuery: "p1=bad&p2=good"},
			}},
			want: []sentRequest{
				{body: "", header: http.Header{"H1": {"good"}, "H2": {"good"}}, query: "p1=good&p2=good"},
			},
		},
		{
			name:  "http_request_with_body",
			chunk: flow.HTTPRequests{{URL: &url.URL{}, Body: io.NopCloser(strings.NewReader("test"))}},
			want:  []sentRequest{{body: "test", header: http.Header{}, query: "p1=good"}},
		},
		{
			name: "http_request_with_reusable_body",
			chunk: flow.HTTPRequests{{
				URL:     &url.URL{},
				GetBody: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("test")), nil },
			}},
			want: []sentRequest{{body: "test", header: http.Header{}, query: "p1=good"}},
		},
		{
			name:  "http_request_body_read_error",
			chunk: flow.HTTPRequests{{URL: &url.URL{}, Body: io.NopCloser(iotest.ErrReader(errors.New("read error")))}},
		},
		{
			name:  "nil_http_response",
			chunk: flow.HTTPResponses{nil},
		},
		{
			name:  "http_response_body_read_error",
			chunk: flow.HTTPResponses{{Body: io.NopCloser(iotest.ErrReader(errors.New("read error")))}},
		},
		{
			name:  "http_response_without_body",
			chunk: flow.HTTPResponses{{Header: http.Header{"H1": {"good"}}}},
		},
		{
			name:    "http_response_with_body",
			headers: map[string]any{"H1": "good"},
			chunk: flow.HTTPResponses{{
				Header: http.Header{"H1": {"bad"}, "H2": {"good"}},
				Body:   io.NopCloser(strings.NewReader("test")),
			}},
			want: []sentRequest{
				{body: "test", header: http.Header{"H1": {"good"}, "H2": {"good"}}, query: "p1=good"},
			},
		},
		{
			name: "http_response_with_reusable_body",
			chunk: flow.HTTPResponses{{
				Header: http.Header{"H1": {"good"}},
				Body:   &reusableBody{Reader: bytes.NewReader([]byte("test")), raw: []byte("test")},
			}},
			want: []sentRequest{{body: "test", header: http.Header{"H1": {"good"}}, query: "p1=good"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := map[string]any{"url": "http://example.com/?p1=good", "headers": tt.headers}
			if tt.headers == nil {
				delete(cfg, "headers")
			}
			s, sent := newRecordingSender(t, cfg)

			s.Send(t.Context(), tt.chunk, codec.FormatDefault)
			s.Close(t.Context())

			got := sent()
			if len(got) != len(tt.want) {
				t.Fatalf("sent requests = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if !reflect.DeepEqual(got[i], tt.want[i]) {
					t.Errorf("sent request %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSenderSendBatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		maxItems int64
		window   time.Duration // Waited before closing the sender, if positive.
		want     []string      // Request bodies, sorted.
	}{
		{
			name: "batching_disabled", // Each chunk is sent as-is.
			want: []string{"1\n2\n3\n", "4\n"},
		},
		{
			name:     "full_batches",
			maxItems: 2,
			want:     []string{"1\n2\n", "3\n4\n"},
		},
		{
			name:     "full_and_partial_batches",
			maxItems: 3,
			want:     []string{"1\n2\n3\n", "4\n"},
		},
		{
			name:     "dispatched_on_close",
			maxItems: 10,
			want:     []string{"1\n2\n3\n4\n"},
		},
		{
			name:     "dispatched_after_window",
			maxItems: 10,
			window:   time.Second,
			want:     []string{"1\n2\n3\n4\n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				cfg := map[string]any{"url": "http://example.com", "timeout": "0"}
				if tt.maxItems > 0 {
					cfg["batch"] = map[string]any{"max_items": tt.maxItems, "time_window": tt.window.String()}
				}
				s, sent := newRecordingSender(t, cfg)

				s.Send(t.Context(), testRow("n", int64(1), int64(2), int64(3)), codec.FormatDefault)
				s.Send(t.Context(), testRow("n", int64(4)), codec.FormatDefault)
				if tt.window > 0 {
					time.Sleep(2 * tt.window)
					synctest.Wait()
					if got := len(sent()); got != len(tt.want) {
						t.Errorf("sent batches before Close = %d, want %d", got, len(tt.want))
					}
				}
				s.Close(t.Context())

				var got []string
				for _, req := range sent() {
					got = append(got, strings.NewReplacer(`{"n":`, "", "}", "").Replace(req.body))
				}
				if !slices.Equal(got, tt.want) {
					t.Errorf("sent batches = %q, want %q", got, tt.want)
				}
			})
		})
	}
}

func TestSenderSendFormat(t *testing.T) {
	t.Parallel()

	jsonType := http.Header{contentTypeHeader: {codec.JSON.ContentType()}}
	ndjsonType := http.Header{contentTypeHeader: {codec.NDJSON.ContentType()}}

	type send struct {
		format config.Format
		chunk  flow.Chunk
	}
	tests := []struct {
		name   string
		format config.Format // Sender's configured format.
		batch  bool
		sends  []send
		want   []sentRequest
	}{
		{
			name:  "both_default",
			sends: []send{{codec.FormatDefault, testJSON(`{"a":1}`, `{"b":2}`)}},
			want:  []sentRequest{{body: `{"a":1}` + "\n" + `{"b":2}` + "\n", header: ndjsonType}},
		},
		{
			name:  "source_format",
			sends: []send{{codec.FormatJSON, testJSON(`{"a":1}`, `{"b":2}`)}},
			want:  []sentRequest{{body: `[{"a":1},{"b":2}]`, header: jsonType}},
		},
		{
			name:  "source_raw_format",
			sends: []send{{codec.FormatRaw, testRow("a", int64(1))}},
			want:  []sentRequest{{body: `{"a":1}` + "\n", header: ndjsonType}},
		},
		{
			name:   "sender_format",
			format: codec.FormatJSONArray,
			sends:  []send{{codec.FormatDefault, testRow("a", int64(1))}},
			want:   []sentRequest{{body: `[{"a":1}]`, header: jsonType}},
		},
		{
			name:   "sender_format_overrides_source_format",
			format: codec.FormatNDJSON,
			sends:  []send{{codec.FormatJSON, testJSON(`{"a":1}`, `{"b":2}`)}},
			want:   []sentRequest{{body: `{"a":1}` + "\n" + `{"b":2}` + "\n", header: ndjsonType}},
		},
		{
			name:  "batch_per_source_format",
			batch: true,
			sends: []send{
				{codec.FormatJSON, testRow("a", int64(1))},
				{codec.FormatDefault, testRow("b", int64(2))},
				{codec.FormatJSON, testRow("c", int64(3))},
				{codec.FormatNDJSON, testRow("d", int64(4))},
			},
			want: []sentRequest{
				{body: `[{"a":1},{"c":3}]`, header: jsonType},
				{body: `{"b":2}` + "\n" + `{"d":4}` + "\n", header: ndjsonType},
			},
		},
		{
			name:  "batch_per_data_kind",
			batch: true,
			sends: []send{
				{codec.FormatDefault, testJSON(`{"j":1}`)},
				{codec.FormatDefault, testRow("r", int64(2))},
				{codec.FormatDefault, testJSON("{\n  \"j\": 3\n}")},
			},
			want: []sentRequest{
				{body: `{"j":1}` + "\n" + `{"j":3}` + "\n", header: ndjsonType},
				{body: `{"r":2}` + "\n", header: ndjsonType},
			},
		},
		{
			name:   "batch_with_sender_format",
			format: codec.FormatJSON,
			batch:  true,
			sends: []send{
				{codec.FormatNDJSON, testRow("a", int64(1))},
				{codec.FormatJSONArray, testRow("b", int64(2))},
			},
			want: []sentRequest{{body: `[{"a":1},{"b":2}]`, header: jsonType}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := map[string]any{"url": "http://example.com"}
			if tt.format != "" {
				cfg["format"] = string(tt.format) // TOML strings are untyped.
			}
			if tt.batch {
				cfg["batch"] = map[string]any{"max_items": int64(10), "time_window": "1h"}
			}
			s, sent := newRecordingSender(t, cfg)

			for _, send := range tt.sends {
				s.Send(t.Context(), send.chunk, send.format)
			}
			s.Close(t.Context())

			got := sent()
			if len(got) != len(tt.want) {
				t.Fatalf("sent requests = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if !reflect.DeepEqual(got[i], tt.want[i]) {
					t.Errorf("sent request %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSenderSendDiscard(t *testing.T) {
	t.Parallel()

	s, sent := newRecordingSender(t, map[string]any{"url": "http://example.com"})
	s.Close(t.Context())

	// Rejected data must be discarded, and its resources released.
	body := &closeTracker{Reader: strings.NewReader("test")}
	s.Send(t.Context(), flow.HTTPRequests{{URL: &url.URL{}, Body: body}}, codec.FormatDefault)
	if !body.closed.Load() {
		t.Error("rejected HTTP request body was not closed")
	}

	s.Send(t.Context(), testRow("key", "value"), codec.FormatDefault)
	s.Send(t.Context(), testJSON(`{"key":"value"}`), codec.FormatDefault)

	if got := sent(); len(got) > 0 {
		t.Errorf("sent requests after Close = %+v, want none", got)
	}
}

func TestSenderOutputFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		senderFormat config.Format // Empty for the default format.
		data         flow.Chunk
		upstream     config.Format
		want         config.Format
	}{
		{"json_upstream_format", "", testJSON(`{"a":1}`), codec.FormatJSON, codec.FormatJSON},
		{"json_default_format", "", testJSON(`{"a":1}`), codec.FormatDefault, codec.FormatNDJSON},
		{"json_non_structured_format", "", testJSON(`{"a":1}`), codec.FormatRaw, codec.FormatNDJSON},
		{"json_explicit_sender_format", codec.FormatJSONArray, testJSON(`{"a":1}`), codec.FormatJSON, codec.FormatJSONArray},
		{"rows_explicit_sender_format", codec.FormatJSON, testRow("a", 1), codec.FormatNDJSON, codec.FormatJSON},
		{"blob_default_format", "", flow.Blobs{[]byte("x")}, codec.FormatDefault, codec.FormatRaw},
		{"blob_upstream_format", codec.FormatNDJSON, flow.Blobs{[]byte("x")}, codec.FormatJSON, codec.FormatJSON},
		{"http_request", codec.FormatJSON, flow.HTTPRequests{{}}, codec.FormatDefault, codec.FormatDefault},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := &Sender{format: cmp.Or(tt.senderFormat, codec.FormatDefault)}
			if got := s.outputFormat(tt.data, tt.upstream); got != tt.want {
				t.Errorf("outputFormat(%T, %q) = %q, want %q", tt.data, tt.upstream, got, tt.want)
			}
		})
	}
}

func TestSenderSendFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		senderFormat string // Empty for the default format.
		want         []string
	}{
		{
			name: "default_sender_format", // Structured data uses the upstream format, or NDJSON for the default.
			want: []string{`json: [{"key":"value"}] (json)`, `rows: ["a"]=["x"] (ndjson)`, "blobs: blob (raw)"},
		},
		{
			name:         "explicit_sender_format", // Overrides the upstream format of structured data only.
			senderFormat: string(codec.FormatJSONArray),
			want:         []string{`json: [{"key":"value"}] (json_array)`, `rows: ["a"]=["x"] (json_array)`, "blobs: blob (raw)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := map[string]any{"url": "http://example.com"}
			if tt.senderFormat != "" {
				cfg["format"] = tt.senderFormat
			}
			s, sent := newRecordingSender(t, cfg)
			fallback, fallbackSent := recordSends(t)
			s.fallback = fallback
			s.Close(t.Context())

			// Rejected data must be passed to the fallback sender (the DLQ) instead of being discarded, and structured
			// data in the sender's resolved format, like all other rejections (see TestSenderCloseRejectsWaitingDataToFallback).
			s.Send(t.Context(), testJSON(`{"key":"value"}`), codec.FormatJSON)
			s.Send(t.Context(), testRow("a", "x"), codec.FormatDefault)
			s.Send(t.Context(), flow.Blobs{[]byte("blob")}, codec.FormatRaw)

			if got := fallbackSent(); !slices.Equal(got, tt.want) {
				t.Errorf("sent to fallback after Close = %q, want %q", got, tt.want)
			}
			if got := sent(); len(got) > 0 {
				t.Errorf("sent requests after Close = %+v, want none", got)
			}
		})
	}
}

func TestSenderEncodingFailureFallback(t *testing.T) {
	t.Parallel()

	s, sent := newRecordingSender(t, map[string]any{"url": "http://example.com"})
	fallback, fallbackSent := recordSends(t)
	s.fallback = fallback

	// Strings with invalid UTF-8 can't be encoded as JSON, so the entire batch must go to the
	// fallback sender (the DLQ), in the format that the sender resolved, instead of being dropped.
	s.Send(t.Context(), flow.Rows{
		{Cols: []string{"a"}, Vals: []any{"ok"}},
		{Cols: []string{"a"}, Vals: []any{"\xff"}},
	}, codec.FormatJSON)
	s.Close(t.Context())

	want := []string{`rows: ["a"]=["ok"] | ["a"]=["\xff"] (json)`}
	if got := fallbackSent(); !slices.Equal(got, want) {
		t.Errorf("sent to fallback = %q, want %q", got, want)
	}
	if got := sent(); len(got) > 0 {
		t.Errorf("sent requests = %+v, want none", got)
	}
}

// closeTracker is an [io.ReadCloser] which records whether it was closed.
type closeTracker struct {
	io.Reader

	closed atomic.Bool
}

func (c *closeTracker) Close() error {
	c.closed.Store(true)
	return nil
}

func TestCopyQueryNilGuard(t *testing.T) {
	t.Parallel()

	nilURL := (*url.URL)(nil)
	nonNilURL := &url.URL{}
	copyQuery(nilURL, nonNilURL)
	copyQuery(nonNilURL, nilURL)
	// No panic = success.

	srcURL := &url.URL{RawQuery: ""}
	dstURL := &url.URL{RawQuery: "foo=bar"}
	copyQuery(srcURL, dstURL)
	if srcURL.RawQuery != "" {
		t.Errorf("srcURL.RawQuery = %q, want %q", srcURL.RawQuery, "")
	}
	if dstURL.RawQuery != "foo=bar" {
		t.Errorf("dstURL.RawQuery = %q, want %q", dstURL.RawQuery, "foo=bar")
	}

	srcURL = &url.URL{RawQuery: ""}
	dstURL = &url.URL{RawQuery: "foo=bar"}
	copyQuery(dstURL, srcURL)
	if srcURL.RawQuery != "foo=bar" {
		t.Errorf("srcURL.RawQuery = %q, want %q", srcURL.RawQuery, "foo=bar")
	}
	if dstURL.RawQuery != "foo=bar" {
		t.Errorf("dstURL.RawQuery = %q, want %q", dstURL.RawQuery, "foo=bar")
	}
}

func TestSafeHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		hdr  http.Header
		req  bool
		want []string
	}{
		{
			name: "nil_headers",
			hdr:  nil,
			want: nil,
		},
		{
			name: "empty_headers",
			hdr:  http.Header{},
			want: []string{},
		},
		{
			name: "remove_request_headers",
			hdr: http.Header{
				"Connection":       {"foo"},
				"Content-Encoding": {"gzip"},
				"Foo":              {"bar"}, // Should be deleted (see "Connection" header).
				"Cookie":           {"abc"}, // Should be preserved.
				"Keep-Alive":       {"timeout=5", "max=1000"},
			},
			req:  true,
			want: []string{"Content-Encoding", "Cookie"},
		},
		{
			name: "remove_response_headers",
			hdr: http.Header{
				"Connection":       {"foo"},
				"Content-Encoding": {"gzip"},
				"Foo":              {"bar"}, // Should be deleted (see "Connection" header).
				"Cookie":           {"abc"}, // Should be preserved.
				"Keep-Alive":       {"timeout=5", "max=1000"},
			},
			req:  false,
			want: []string{"Cookie"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := safeHeaders(tt.hdr, tt.req)
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Errorf("safeHeaders() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSenderClose(t *testing.T) {
	t.Parallel()

	var counter atomic.Int64
	s, err := NewSender(map[string]any{"url": "http://example.com"}, "TestSenderClose", config.SenderTypeHTTP, nil)
	if err != nil {
		t.Fatalf("NewSender() error: %v", err)
	}

	s.client.Transport = roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		counter.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})

	s.Send(t.Context(), flow.Blobs{[]byte("payload")}, codec.FormatDefault)
	s.Close(t.Context())

	if got := counter.Load(); got != 1 {
		t.Errorf("server received %d requests, want 1", got)
	}

	// Calling Send after Close should be rejected.
	s.Send(t.Context(), flow.Blobs{[]byte("another payload")}, codec.FormatDefault)
	s.Close(t.Context()) // Idempotent.

	if got := counter.Load(); got != 1 {
		t.Errorf("server received %d requests after Close, want 1", got)
	}
}

func TestSenderCloseTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		s, err := NewSender(
			map[string]any{"url": "http://example.com", "timeout": "100ms"},
			"TestSenderCloseTimeout",
			config.SenderTypeHTTP,
			nil,
		)
		if err != nil {
			t.Fatalf("NewSender() error: %v", err)
		}

		s.client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		})

		s.Send(t.Context(), flow.Blobs{[]byte("payload")}, codec.FormatDefault)

		want := 50 * time.Millisecond
		ctx, cancel := context.WithTimeout(t.Context(), want)
		t.Cleanup(cancel)

		start := time.Now()
		s.Close(ctx)

		if got := time.Since(start); got != want {
			t.Errorf("Sender.Close() timeout took %v, want %v", got, want)
		}
	})
}

func TestSenderCloseDuringRetryDelay(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		s, err := NewSender(map[string]any{
			"url":     "http://example.com",
			"timeout": "5s",
			"retries": map[string]any{
				"type":     retryTypeStatic,
				"interval": "1m",
			},
		}, "TestSenderCloseDuringRetryDelay", config.SenderTypeHTTP, nil)
		if err != nil {
			t.Fatalf("NewSender() error: %v", err)
		}

		s.client.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       http.NoBody,
				Header:     make(http.Header),
			}, nil
		})

		s.Send(t.Context(), flow.Blobs{[]byte("payload")}, codec.FormatDefault)

		// Allow attempt 0 to fail and enter the 1-minute retry wait.
		time.Sleep(10 * time.Millisecond)

		start := time.Now()
		s.Close(t.Context())

		// Close should return immediately without waiting for CloseTimeout (5s) or retry interval (1m).
		if elapsed := time.Since(start); elapsed == CloseTimeout {
			t.Errorf("Sender.Close() took %v, want graceful close well under %v", elapsed, CloseTimeout)
		}
	})
}

func TestSenderConcurrencyLimit(t *testing.T) {
	t.Parallel()

	const limit, total = 2, 5

	tests := []struct {
		name string
		data flow.Chunk
	}{
		{
			name: "blobs",
			data: flow.Blobs{[]byte("1"), []byte("2"), []byte("3"), []byte("4"), []byte("5")},
		},
		{
			name: "structured_batches", // Encoding happens only after acquiring a slot.
			data: testRow("x", int64(1), int64(2), int64(3), int64(4), int64(5)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				s, err := NewSender(map[string]any{
					"url":               "http://example.com",
					"concurrency_limit": int64(limit),
					"batch":             map[string]any{"max_items": int64(1)},
				}, tt.name, config.SenderTypeHTTP, nil)
				if err != nil {
					t.Fatalf("NewSender() error: %v", err)
				}

				release := make(chan struct{})
				var mu sync.Mutex
				var active, maxActive, started int
				s.client.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
					mu.Lock()
					started++
					active++
					maxActive = max(maxActive, active)
					mu.Unlock()

					<-release

					mu.Lock()
					active--
					mu.Unlock()
					return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
				})

				// Must not block, even though there are more payloads than slots.
				s.Send(t.Context(), tt.data, codec.FormatDefault)
				synctest.Wait()
				mu.Lock()
				if started != limit {
					t.Errorf("requests started before any finished = %d, want %d", started, limit)
				}
				mu.Unlock()

				close(release)
				synctest.Wait() // All the waiting requests acquire slots before Close, which would drop them.
				s.Close(t.Context())
				mu.Lock()
				defer mu.Unlock()
				if started != total {
					t.Errorf("requests started = %d, want %d", started, total)
				}
				if maxActive != limit {
					t.Errorf("max concurrent requests = %d, want %d", maxActive, limit)
				}
			})
		})
	}
}

func TestSenderCloseDropsWaitingRequests(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		s, err := NewSender(
			map[string]any{"url": "http://example.com", "concurrency_limit": int64(1)},
			"TestSenderCloseDropsWaitingRequests", config.SenderTypeHTTP, nil,
		)
		if err != nil {
			t.Fatalf("NewSender() error: %v", err)
		}

		release := make(chan struct{})
		var started atomic.Int64
		s.client.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
			started.Add(1)
			<-release
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
		})

		s.Send(t.Context(), flow.Blobs{[]byte("1"), []byte("2"), []byte("3")}, codec.FormatDefault)
		synctest.Wait() // The first request holds the only slot, the others are waiting for it.

		closed := make(chan struct{})
		go func() {
			defer close(closed)
			s.Close(t.Context())
		}()
		synctest.Wait() // Close began: waiting requests are dropped, and Close waits only for the active one.

		close(release)
		<-closed
		if got := started.Load(); got != 1 {
			t.Errorf("requests started = %d, want 1", got)
		}
	})
}

func TestSenderCloseRejectsWaitingDataToFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		senderFormat string        // Empty for the default format.
		upstream     config.Format // Empty for the default format.
		data         func() flow.Chunk
		want         string
	}{
		{
			name: "blob", // Sent in the default format, which means raw bytes for blobs.
			data: func() flow.Chunk { return flow.Blobs{[]byte("blob")} },
			want: "blobs: blob (raw)",
		},
		{
			name:         "blob_with_upstream_format", // E.g., an encoded payload, which keeps its format.
			senderFormat: string(codec.FormatNDJSON),  // Irrelevant for blobs.
			upstream:     codec.FormatJSON,
			data:         func() flow.Chunk { return flow.Blobs{[]byte(`{"a":1}`)} },
			want:         `blobs: {"a":1} (json)`,
		},
		{
			name: "rows", // Dispatched by the batcher when Close flushes it.
			data: func() flow.Chunk { return testRow("a", "x") },
			want: `rows: ["a"]=["x"] (ndjson)`,
		},
		{
			name:         "rows_explicit_sender_format", // Overrides the upstream format.
			senderFormat: string(codec.FormatJSONArray),
			data:         func() flow.Chunk { return testRow("a", "x") },
			want:         `rows: ["a"]=["x"] (json_array)`,
		},
		{
			name: "request_without_body",
			data: func() flow.Chunk { return flow.HTTPRequests{{URL: &url.URL{}}} },
			want: "requests: <nil> (default)",
		},
		{
			name: "request_with_get_body", // Like requests from receivers.
			data: func() flow.Chunk {
				req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
				return flow.HTTPRequests{outgoingRequest(req, []byte("req"))}
			},
			want: "requests: req (default)",
		},
		{
			name: "request_without_get_body",
			data: func() flow.Chunk {
				return flow.HTTPRequests{{URL: &url.URL{}, Body: io.NopCloser(strings.NewReader("req"))}}
			},
			want: "requests: req (default)",
		},
		{
			name: "response_with_reusable_body", // Like responses from collectors.
			data: func() flow.Chunk {
				body := &reusableBody{Reader: bytes.NewReader([]byte("resp")), raw: []byte("resp")}
				return flow.HTTPResponses{{StatusCode: http.StatusOK, Body: body}}
			},
			want: "responses: resp (default)",
		},
		{
			name: "response_without_reusable_body",
			data: func() flow.Chunk {
				return flow.HTTPResponses{{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("resp"))}}
			},
			want: "responses: resp (default)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				cfg := map[string]any{"url": "http://example.com", "concurrency_limit": int64(1)}
				if tt.senderFormat != "" {
					cfg["format"] = tt.senderFormat
				}
				s, err := NewSender(cfg, tt.name, config.SenderTypeHTTP, nil)
				if err != nil {
					t.Fatalf("NewSender() error: %v", err)
				}
				fallback, fallbackSent := recordSends(t)
				s.fallback = fallback

				release := make(chan struct{})
				var started atomic.Int64
				s.client.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
					started.Add(1)
					<-release
					return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
				})

				s.Send(t.Context(), flow.Blobs{[]byte("active")}, codec.FormatDefault) // Holds the only slot.
				synctest.Wait()
				s.Send(t.Context(), tt.data(), cmp.Or(tt.upstream, codec.FormatDefault)) // Waits for a slot, or in the batcher.
				synctest.Wait()

				closed := make(chan struct{})
				go func() {
					defer close(closed)
					s.Close(t.Context())
				}()
				synctest.Wait() // Close began: waiting data is rejected, and Close waits only for the active request.

				close(release)
				<-closed
				if got := started.Load(); got != 1 {
					t.Errorf("requests started = %d, want 1", got)
				}
				if got, want := fallbackSent(), []string{tt.want}; !slices.Equal(got, want) {
					t.Errorf("sent to fallback = %q, want %q", got, want)
				}
			})
		})
	}
}

func TestSenderCloseSendsWithAvailableSlots(t *testing.T) {
	t.Parallel()

	// Requests that aren't blocked by the concurrency limit are sent, even if Close begins before their
	// goroutines start (no [synctest.Wait] before Close). This includes the pending batch that Close flushes.
	s, sent := newRecordingSender(t, map[string]any{
		"url":               "http://example.com",
		"concurrency_limit": int64(3),
		"batch":             map[string]any{"max_items": int64(10), "time_window": "1h"},
	})

	s.Send(t.Context(), flow.Blobs{[]byte("a"), []byte("b")}, codec.FormatDefault)
	s.Send(t.Context(), testRow("n", int64(1)), codec.FormatDefault)
	s.Close(t.Context())

	if got := len(sent()); got != 3 {
		t.Errorf("sent requests = %d, want 3", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func testRow(col string, vals ...any) flow.Rows {
	cols := []string{col}
	rows := make(flow.Rows, len(vals))
	for i, v := range vals {
		rows[i] = flow.Row{Cols: cols, Vals: []any{v}}
	}
	return rows
}

func testJSON(objs ...string) flow.JSON {
	chunk := make(flow.JSON, len(objs))
	for i, obj := range objs {
		chunk[i] = jsontext.Value(obj)
	}
	return chunk
}
