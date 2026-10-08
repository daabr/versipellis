package http

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
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

	"github.com/daabr/versipellis/pkg/config"
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

			_, gotErr := NewSender(tt.cfg, tt.name, tt.baseType)
			if (gotErr != nil) != tt.wantErr {
				t.Errorf("NewSender() error = %v, wantErr %v", gotErr, tt.wantErr)
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
			s, err := NewSender(cfg, tt.name, config.SenderTypeHTTP)
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

	_, err := NewSender(
		map[string]any{"url": "https://example.com"}, "TestNewSenderHTTP3", config.SenderTypeHTTP3,
	)
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

	s, err := NewSender(cfg, t.Name(), config.SenderTypeHTTP)
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
	ndjsonType := http.Header{contentTypeHeader: {flow.FormatNDJSON.ContentType()}}

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
			chunk: flow.Structured{{"key1": "value1"}, {"key2": int64(2)}},
			want: []sentRequest{
				{body: `{"key1":"value1"}` + "\n" + `{"key2":2}` + "\n", header: ndjsonType, query: "p1=good"},
			},
		},
		{
			name:  "structured_with_unencoded_html",
			chunk: flow.Structured{{"html": "& < >"}},
			want:  []sentRequest{{body: `{"html":"& < >"}` + "\n", header: ndjsonType, query: "p1=good"}},
		},
		{
			name:    "structured_with_configured_content_type",
			headers: map[string]any{contentTypeHeader: "application/json"},
			chunk:   flow.Structured{{"key": "value"}},
			want: []sentRequest{
				{body: `{"key":"value"}` + "\n", header: http.Header{contentTypeHeader: {"application/json"}}, query: "p1=good"},
			},
		},
		{
			name:  "structured_encoding_error",
			chunk: flow.Structured{{"key1": "value1"}, {"key2": make(chan struct{})}}, // Channels cannot be encoded.
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

			s.Send(t.Context(), tt.chunk)
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

				s.Send(t.Context(), flow.Structured{{"n": int64(1)}, {"n": int64(2)}, {"n": int64(3)}})
				s.Send(t.Context(), flow.Structured{{"n": int64(4)}})
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

func TestSenderSendDiscard(t *testing.T) {
	t.Parallel()

	s, sent := newRecordingSender(t, map[string]any{"url": "http://example.com"})
	s.Close(t.Context())

	// Rejected data must be discarded, and its resources released.
	body := &closeTracker{Reader: strings.NewReader("test")}
	s.Send(t.Context(), flow.HTTPRequests{{URL: &url.URL{}, Body: body}})
	if !body.closed.Load() {
		t.Error("rejected HTTP request body was not closed")
	}

	s.Send(t.Context(), flow.Structured{{"key": "value"}})

	if got := sent(); len(got) > 0 {
		t.Errorf("sent requests after Close = %+v, want none", got)
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
	s, err := NewSender(map[string]any{"url": "http://example.com"}, "TestSenderClose", config.SenderTypeHTTP)
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

	s.Send(t.Context(), flow.Blobs{[]byte("payload")})
	s.Close(t.Context())

	if got := counter.Load(); got != 1 {
		t.Errorf("server received %d requests, want 1", got)
	}

	// Calling Send after Close should be rejected.
	s.Send(t.Context(), flow.Blobs{[]byte("another payload")})
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
		)
		if err != nil {
			t.Fatalf("NewSender() error: %v", err)
		}

		s.client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		})

		s.Send(t.Context(), flow.Blobs{[]byte("payload")})

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
		}, "TestSenderCloseDuringRetryDelay", config.SenderTypeHTTP)
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

		s.Send(t.Context(), flow.Blobs{[]byte("payload")})

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
			data: flow.Structured{{"x": int64(1)}, {"x": int64(2)}, {"x": int64(3)}, {"x": int64(4)}, {"x": int64(5)}},
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
				}, tt.name, config.SenderTypeHTTP)
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

				s.Send(t.Context(), tt.data) // Must not block, even though there are more payloads than slots.
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
		s, err := NewSender(map[string]any{"url": "http://example.com", "concurrency_limit": int64(1)},
			"TestSenderCloseDropsWaitingRequests", config.SenderTypeHTTP,
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

		s.Send(t.Context(), flow.Blobs{[]byte("1"), []byte("2"), []byte("3")})
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

func TestSenderCloseSendsWithAvailableSlots(t *testing.T) {
	t.Parallel()

	// Requests that aren't blocked by the concurrency limit are sent, even if Close begins before their
	// goroutines start (no [synctest.Wait] before Close). This includes the pending batch that Close flushes.
	s, sent := newRecordingSender(t, map[string]any{
		"url":               "http://example.com",
		"concurrency_limit": int64(3),
		"batch":             map[string]any{"max_items": int64(10), "time_window": "1h"},
	})

	s.Send(t.Context(), flow.Blobs{[]byte("a"), []byte("b")})
	s.Send(t.Context(), flow.Structured{{"n": int64(1)}})
	s.Close(t.Context())

	if got := len(sent()); got != 3 {
		t.Errorf("sent requests = %d, want 3", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
