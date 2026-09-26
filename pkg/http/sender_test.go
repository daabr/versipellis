package http

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/daabr/versipellis/pkg/config"
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

func TestNewSenderHTTP3(t *testing.T) {
	t.Parallel()

	_, err := NewSender(
		map[string]any{"url": "https://example.com"}, "TestNewSenderHTTP3", config.SenderTypeHTTP3,
	)
	if err != nil {
		t.Fatalf("NewSender() error = %v, wantErr %v", err, false)
	}
}

func TestSenderSendBatch(t *testing.T) {
	t.Parallel()

	var counter atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		switch {
		case err != nil:
			t.Errorf("failed to read request body: %v", err)
		case len(body) == 0:
			t.Errorf("received empty request body")
		default:
			counter.Add(1)
		}
	}))
	t.Cleanup(server.Close)

	sender, err := NewSender(
		map[string]any{"url": server.URL, "timeout": "0"},
		"TestSenderSendBatch", config.SenderTypeHTTP,
	)
	if sender == nil {
		t.Fatalf("NewSender() error = %v", err)
	}

	// Aside: cover the logic for not sending an empty byte slice.
	sender.Send(t.Context(), []byte{})

	// Step 1: start batch.
	sender.Send(t.Context(), nil)

	// Reminder: join multiple parts into a single batch.
	sender.Send(t.Context(), []byte("part1"))
	sender.Send(t.Context(), []byte("part2"))
	sender.Send(t.Context(), []byte("part3"))

	// Step 3: finalize & send batch.
	sender.Send(t.Context(), nil)

	sender.Close(t.Context())

	want := int64(3) // Reminder: change to 1 when batches are implemented.
	if got := counter.Load(); got != want {
		t.Errorf("counter = %d, want %d", got, want)
	}
}

func TestSerializeDataHTTPRequest(t *testing.T) {
	t.Parallel()

	u, _ := url.Parse("http://example.com/?p1=good")

	tests := []struct {
		name      string
		req       *http.Request
		hdr       http.Header
		wantBody  bool
		wantErr   bool
		wantHdr   http.Header
		wantQuery string
	}{
		{
			name:     "nil",
			req:      nil,
			hdr:      http.Header{},
			wantBody: false,
			wantErr:  true,
		},
		{
			name: "modify_some_headers_and_params",
			req: &http.Request{
				Header: http.Header{"H1": {"bad"}, "H2": {"good"}},
				URL:    &url.URL{RawQuery: "p1=bad&p2=good"},
			},
			hdr:       http.Header{"H1": {"good"}},
			wantBody:  false,
			wantErr:   false,
			wantHdr:   http.Header{"H1": {"good"}, "H2": {"good"}},
			wantQuery: "p1=good&p2=good",
		},
		{
			name:      "req_with_body",
			req:       &http.Request{URL: &url.URL{}},
			hdr:       http.Header{},
			wantBody:  true,
			wantErr:   false,
			wantHdr:   http.Header{},
			wantQuery: "p1=good",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotURL := u.Clone()
			gotHdr := tt.hdr.Clone()
			if tt.wantBody {
				tt.req.Body = io.NopCloser(strings.NewReader("test"))
			}

			getBodyFn, _, gotErr := serializeData(tt.req, gotURL, gotHdr)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("serializeData() error = %v, wantErr %v", gotErr, tt.wantErr)
			}

			var gotBody1, gotBody2 []byte
			if getBodyFn != nil {
				r, err := getBodyFn()
				if err != nil {
					t.Fatalf("getBodyFunc(1) error = %v", err)
				}
				gotBody1, err = io.ReadAll(r)
				if err != nil {
					t.Fatalf("io.ReadAll(1) error = %v", err)
				}

				r, err = getBodyFn()
				if err != nil {
					t.Fatalf("getBodyFunc(2) error = %v", err)
				}
				gotBody2, err = io.ReadAll(r)
				if err != nil {
					t.Fatalf("io.ReadAll(2) error = %v", err)
				}
			}

			gotQuery := gotURL.Query()

			wantBody := ""
			if tt.wantBody {
				wantBody = "test"
			}
			if tt.wantBody != (string(gotBody1) == "test") {
				t.Errorf("body = %v, want %q", gotBody1, wantBody)
			}
			if !bytes.Equal(gotBody2, gotBody1) {
				t.Errorf("retry body = %v, want %q", gotBody2, gotBody1)
			}
			if tt.req != nil && !reflect.DeepEqual(gotHdr, tt.wantHdr) {
				t.Errorf("hdr = %+v, want %+v", gotHdr, tt.wantHdr)
			}
			if tt.req != nil && gotQuery.Encode() != tt.wantQuery {
				t.Errorf("query = %+v, want %q", gotQuery, tt.wantQuery)
			}
		})
	}
}

func TestSerializeDataHTTPResponse(t *testing.T) {
	t.Parallel()

	u, _ := url.Parse("http://example.com/")

	tests := []struct {
		name     string
		resp     *http.Response
		hdr      http.Header
		wantBody bool
		wantErr  bool
		wantHdr  http.Header
	}{
		{
			name:     "nil",
			resp:     nil,
			hdr:      http.Header{},
			wantBody: false,
			wantErr:  true,
		},
		{
			name: "modify_some_headers",
			resp: &http.Response{
				Header: http.Header{"H1": {"bad"}, "H2": {"good"}},
			},
			hdr:      http.Header{"H1": {"good"}},
			wantBody: false,
			wantErr:  false,
			wantHdr:  http.Header{"H1": {"good"}, "H2": {"good"}},
		},
		{
			name:     "resp_with_body",
			resp:     &http.Response{},
			hdr:      http.Header{},
			wantBody: true,
			wantErr:  false,
			wantHdr:  http.Header{},
		},
		{
			name: "resp_with_reusable_body",
			resp: &http.Response{
				Header: http.Header{"H1": {"good"}},
				Body:   &reusableBody{Reader: bytes.NewReader([]byte("test")), raw: []byte("test")},
			},
			hdr:      http.Header{"H1": {"good"}},
			wantBody: true,
			wantErr:  false,
			wantHdr:  http.Header{"H1": {"good"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotURL := u.Clone()
			gotHdr := tt.hdr.Clone()
			if tt.wantBody {
				tt.resp.Body = io.NopCloser(strings.NewReader("test"))
			}

			getBodyFn, _, gotErr := serializeData(tt.resp, gotURL, gotHdr)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("serializeData() error = %v, wantErr %v", gotErr, tt.wantErr)
			}

			var gotBody1, gotBody2 []byte
			if getBodyFn != nil {
				r, err := getBodyFn()
				if err != nil {
					t.Fatalf("getBodyFunc(1) error = %v", err)
				}
				gotBody1, err = io.ReadAll(r)
				if err != nil {
					t.Fatalf("io.ReadAll(1) error = %v", err)
				}

				r, err = getBodyFn()
				if err != nil {
					t.Fatalf("getBodyFunc(2) error = %v", err)
				}
				gotBody2, err = io.ReadAll(r)
				if err != nil {
					t.Fatalf("io.ReadAll(2) error = %v", err)
				}
			}

			wantBody := ""
			if tt.wantBody {
				wantBody = "test"
			}
			if tt.wantBody != (string(gotBody1) == "test") {
				t.Errorf("body = %v, want %q", gotBody1, wantBody)
			}
			if !bytes.Equal(gotBody2, gotBody1) {
				t.Errorf("retry body = %v, want %q", gotBody2, gotBody1)
			}
			if tt.resp != nil && !reflect.DeepEqual(gotHdr, tt.wantHdr) {
				t.Errorf("headers = %+v, want %+v", gotHdr, tt.wantHdr)
			}
		})
	}
}

func TestSerializeDataJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		data     any
		wantBody string
		wantErr  bool
	}{
		{
			name:     "nil",
			data:     nil,
			wantErr:  false,
			wantBody: "null\n",
		},
		{
			name:     "map",
			data:     map[string]any{"key": "value"},
			wantBody: `{"key":"value"}` + "\n",
			wantErr:  false,
		},
		{
			name:     "json_with_unencoded_html",
			data:     map[string]any{"html": "& < >"},
			wantBody: `{"html":"& < >"}` + "\n",
			wantErr:  false,
		},
		{
			name:    "json_error",
			data:    map[string]any{"channel": make(chan struct{})}, // Go channels cannot be encoded as JSON.
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hdr := http.Header{}
			getBodyFn, _, gotErr := serializeData(tt.data, nil, hdr)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("serializeData() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			r, _ := getBodyFn()
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("io.ReadAll() error = %v", err)
			}

			if gotBody := string(got); gotBody != tt.wantBody {
				t.Errorf("serializeData() body = %q, want %q", gotBody, tt.wantBody)
			}
			wantHdr := http.Header{"Content-Type": {"application/json"}}
			if !reflect.DeepEqual(hdr, wantHdr) {
				t.Errorf("headers = %+v, want %+v", hdr, wantHdr)
			}
		})
	}
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

	s.Send(t.Context(), []byte("payload"))
	s.Close(t.Context())

	if got := counter.Load(); got != 1 {
		t.Errorf("server received %d requests, want 1", got)
	}

	// Calling Send after Close should be rejected.
	s.Send(t.Context(), []byte("another payload"))
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

		s.Send(t.Context(), []byte("payload"))

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

		s.Send(t.Context(), []byte("payload"))

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

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
