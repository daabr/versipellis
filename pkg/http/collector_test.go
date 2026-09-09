package http

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/dest"
)

func TestNewCollector(t *testing.T) {
	t.Parallel()

	httpBase := &config.BaseCollector{Type: config.CollectorTypeHTTP}

	tests := []struct {
		name    string
		base    *config.BaseCollector
		cfg     map[string]any
		wantErr bool

		wantURL     string
		wantMethod  string
		wantHeaders http.Header
		wantTimeout time.Duration
	}{
		{
			name:    "nil_base",
			base:    nil,
			cfg:     map[string]any{},
			wantErr: true,
		},
		{
			name:    "wrong_collector_type",
			base:    &config.BaseCollector{Type: config.CollectorTypeSQL},
			cfg:     map[string]any{},
			wantErr: true,
		},
		{
			name:    "nil_cfg",
			base:    httpBase,
			cfg:     nil,
			wantErr: true,
		},
		{
			name:    "missing_section",
			base:    httpBase,
			cfg:     map[string]any{},
			wantErr: true,
		},
		{
			name: "invalid_url",
			base: httpBase,
			cfg: map[string]any{
				"http": map[string]any{"url": ""},
			},
			wantErr: true,
		},
		{
			name: "invalid_method",
			base: httpBase,
			cfg: map[string]any{
				"http": map[string]any{"url": "https://example.com", "method": "DELETE"},
			},
			wantErr: true,
		},
		{
			name: "invalid_query_type",
			base: httpBase,
			cfg: map[string]any{
				"http": map[string]any{"url": "https://example.com", "query": 123},
			},
			wantErr: true,
		},
		{
			name: "headers_not_a_table",
			base: httpBase,
			cfg: map[string]any{
				"http": map[string]any{"url": "https://example.com", "headers": "Accept: application/json"},
			},
			wantErr: true,
		},
		{
			name: "body_file_not_found",
			base: httpBase,
			cfg: map[string]any{
				"http": map[string]any{"url": "https://example.com", "body_file": "/nonexistent/file"},
			},
			wantErr: true,
		},
		{
			name: "invalid_timeout",
			base: httpBase,
			cfg: map[string]any{
				"http": map[string]any{"url": "https://example.com", "timeout": "not-a-duration"},
			},
			wantErr: true,
		},
		{
			name: "minimal_valid_config",
			base: httpBase,
			cfg: map[string]any{
				"http": map[string]any{"url": "https://example.com"},
			},
			wantURL:     "https://example.com",
			wantMethod:  http.MethodGet,
			wantHeaders: nil,
			wantTimeout: defaultRequestTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c, gotErr := NewCollector(tt.base, tt.cfg)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("NewCollector() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			if b := c.Base(); !reflect.DeepEqual(b, tt.base) {
				t.Errorf("Collector.Base() = %v, want %v", b, tt.base)
			}
			if got := c.url.String(); got != tt.wantURL {
				t.Errorf("Collector.url = %q, want %q", got, tt.wantURL)
			}
			if c.method != tt.wantMethod {
				t.Errorf("Collector.method = %q, want %q", c.method, tt.wantMethod)
			}
			if !reflect.DeepEqual(c.headers, tt.wantHeaders) {
				t.Errorf("Collector.headers = %#v, want %#v", c.headers, tt.wantHeaders)
			}
			if c.timeout != tt.wantTimeout {
				t.Errorf("Collector.timeout = %v, want %v", c.timeout, tt.wantTimeout)
			}
		})
	}
}

func TestParseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rawURL   string
		protoVer string
		want     string
		wantErr  bool
	}{
		{
			name:    "empty_url",
			rawURL:  "",
			wantErr: true,
		},
		{
			name:    "invalid_url",
			rawURL:  "://invalid-url",
			wantErr: true,
		},
		{
			name:    "relative_url",
			rawURL:  "/relative/path",
			wantErr: true,
		},
		{
			name:     "http3_with_http_scheme",
			rawURL:   "http://example.com",
			protoVer: config.CollectorTypeHTTP3,
			wantErr:  true,
		},
		{
			name:     "http3_with_https_scheme",
			rawURL:   "https://example.com",
			protoVer: config.CollectorTypeHTTP3,
			want:     "https://example.com",
			wantErr:  false,
		},
		{
			name:     "http_with_http_scheme",
			rawURL:   "http://example.com",
			protoVer: config.CollectorTypeHTTP,
			want:     "http://example.com",
			wantErr:  false,
		},
		{
			name:     "http_with_https_scheme",
			rawURL:   "https://example.com/",
			protoVer: config.CollectorTypeHTTP,
			want:     "https://example.com/",
			wantErr:  false,
		},
		{
			name:     "invalid_scheme",
			rawURL:   "invalid://example.com/",
			protoVer: config.CollectorTypeHTTP,
			want:     "",
			wantErr:  true,
		},
		{
			name:    "url_with_opaque_part",
			rawURL:  "https:opaque-part",
			wantErr: true,
		},
		{
			name:    "url_without_host",
			rawURL:  "https://",
			wantErr: true,
		},
		{
			name:    "url_with_invalid_port_number_1",
			rawURL:  "https://example.com:99999",
			wantErr: true,
		},
		{
			name:    "url_with_invalid_port_number_2",
			rawURL:  "https://example.com:0",
			wantErr: true,
		},
		{
			name:    "url_with_invalid_port_number_3",
			rawURL:  "https://example.com:-1",
			wantErr: true,
		},
		{
			name:    "url_with_non_numeric_port",
			rawURL:  "https://example.com:port",
			wantErr: true,
		},
		{
			name:    "url_with_query_and_fragment",
			rawURL:  "https://example.com/path?query=1&param2=value2#fragment",
			want:    "https://example.com/path?query=1&param2=value2#fragment",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, gotErr := parseURL(tt.rawURL, tt.protoVer)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("parseURL() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if (got == nil) != tt.wantErr || (got != nil && got.String() != tt.want) {
				t.Errorf("parseURL() = %v, want %q", got, tt.want)
			}
		})
	}
}

func TestParseMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		method  string
		wantErr bool
	}{
		{"empty", "", true},
		{"get_upper_case", "GET", false},
		{"get_lower_case", "get", false},
		{"post_mixed_case_1", "Post", false},
		{"post_lower_case_2", "posT", false},
		{"patch_upper_case", http.MethodPatch, false},
		{"put_upper_case", http.MethodPut, false},
		{"trace_upper_case", http.MethodTrace, true},
		{"invalid", "BlAh", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, gotErr := parseMethod(tt.method); (gotErr != nil) != tt.wantErr {
				t.Errorf("parseMethod() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
		})
	}
}

func TestParseQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		url     string
		cfg     any
		want    string
		wantErr bool
	}{
		{
			name: "nil_raw_leaves_url_untouched",
			url:  "https://example.com?z=9&a=1",
			cfg:  nil,
			want: "z=9&a=1", // Not re-encoded/sorted, since mergeQuery returns early.
		},
		{
			name: "empty_table_re-encodes_existing_query",
			url:  "https://example.com?z=9&a=1",
			cfg:  map[string]any{},
			want: "a=1&z=9", // Re-encoded via url.Values.Encode(), which sorts by key.
		},
		{
			name: "adds_new_params_to_existing_query",
			url:  "https://example.com?a=1",
			cfg:  map[string]any{"b": "2"},
			want: "a=1&b=2",
		},
		{
			name: "overrides_same-name_param",
			url:  "https://example.com?a=1",
			cfg:  map[string]any{"a": "2"},
			want: "a=2",
		},
		{
			name: "no_existing_query",
			url:  "https://example.com",
			cfg:  map[string]any{"a": "1"},
			want: "a=1",
		},
		{
			name:    "raw_not_a_table",
			url:     "https://example.com",
			cfg:     "a=1",
			wantErr: true,
		},
		{
			name:    "value_not_a_string",
			url:     "https://example.com",
			cfg:     map[string]any{"a": 1},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			u, err := url.Parse(tt.url)
			if err != nil {
				t.Fatalf("url.Parse(%q) error: %v", tt.url, err)
			}

			gotErr := parseQuery(u, tt.cfg)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("parseQuery() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if !tt.wantErr && u.RawQuery != tt.want {
				t.Errorf("parseQuery() RawQuery = %q, want %q", u.RawQuery, tt.want)
			}
		})
	}
}

func TestParseHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     any
		want    http.Header
		wantErr bool
	}{
		{
			name: "no_headers",
			cfg:  nil,
			want: nil,
		},
		{
			name: "empty_table",
			cfg:  map[string]any{},
			want: http.Header{},
		},
		{
			name: "single_header",
			cfg:  map[string]any{"Accept": "application/json"},
			want: http.Header{"Accept": {"application/json"}},
		},
		{
			name: "lowercase_key_is_canonicalized",
			cfg:  map[string]any{"accept": "text/plain"},
			want: http.Header{"Accept": {"text/plain"}},
		},
		{
			name: "comma-separated_value_is_kept_as_a_single_value",
			cfg:  map[string]any{"Accept": "text/plain, application/json, application/xml"},
			want: http.Header{"Accept": {"text/plain, application/json, application/xml"}},
		},
		{
			name: "multiple_headers",
			cfg: map[string]any{
				"Accept":        "application/json",
				"Authorization": "Bearer token",
			},
			want: http.Header{
				"Accept":        {"application/json"},
				"Authorization": {"Bearer token"},
			},
		},
		{
			name:    "headers_not_a_table",
			cfg:     "Accept: application/json",
			wantErr: true,
		},
		{
			name:    "invalid_key",
			cfg:     map[string]any{" ": "value"},
			wantErr: true,
		},
		{
			name:    "value_not_a_string",
			cfg:     map[string]any{"Key": 1},
			wantErr: true,
		},
		{
			name:    "invalid_value",
			cfg:     map[string]any{"Key": "text/plain\napplication/json"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, gotErr := parseHeaders(tt.cfg)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("parseHeaders() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseHeaders() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestLoadBody(t *testing.T) {
	t.Parallel()

	bodyWithSpaces := "\n text  \n\n"
	tempDir := t.TempDir()
	err := os.WriteFile(filepath.Join(tempDir, "empty.txt"), []byte{}, 0o600) //gosec:disable G304 // Unit test.
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(filepath.Join(tempDir, "body.txt"), []byte(bodyWithSpaces), 0o600) //gosec:disable G304 // Unit test.
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		body    string
		path    string
		method  string
		want    string
		wantErr bool
	}{
		{
			name:    "no_body_or_file",
			wantErr: false,
		},
		{
			name:    "both_body_and_file",
			body:    bodyWithSpaces,
			path:    filepath.Join(tempDir, "body.txt"),
			wantErr: true,
		},
		{
			name:    "valid_inline_body_with_get_method",
			body:    bodyWithSpaces,
			method:  http.MethodGet,
			wantErr: true,
		},
		{
			name: "valid_inline_body",
			body: bodyWithSpaces,
			want: bodyWithSpaces,
		},
		{
			name:    "body_file_not_found",
			path:    filepath.Join(tempDir, "nonexistent"),
			wantErr: true,
		},
		{
			name:    "body_file_is_directory",
			path:    tempDir,
			wantErr: true,
		},
		{
			name:    "empty_body_file",
			path:    filepath.Join(tempDir, "empty.txt"),
			wantErr: true,
		},
		{
			name:    "valid_body_file_with_get_method",
			path:    filepath.Join(tempDir, "body.txt"),
			method:  http.MethodGet,
			wantErr: true,
		},
		{
			name: "valid_body_file",
			path: filepath.Join(tempDir, "body.txt"),
			want: bodyWithSpaces,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := map[string]any{"body": tt.body, "body_file": tt.path}
			gotBytes, gotErr := loadBody(cfg, tt.method)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("loadBody() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if gotStr := string(gotBytes); gotStr != tt.want {
				t.Fatalf("loadBody() got = %q, want %q", gotStr, tt.want)
			}
		})
	}
}

func TestParseByteSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  map[string]any
		want int64
	}{
		{
			name: "valid_positive_number",
			cfg:  map[string]any{"size": int64(123)},
			want: 123,
		},
		{
			name: "invalid_zero",
			cfg:  map[string]any{"size": int64(0)},
			want: 456,
		},
		{
			name: "invalid_negative_number",
			cfg:  map[string]any{"size": int64(-123)},
			want: 456,
		},
		{
			name: "invalid_non_number",
			cfg:  map[string]any{"size": "not-a-number"},
			want: 456,
		},
		{
			name: "missing_key",
			cfg:  map[string]any{},
			want: 456,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := parseByteSize(tt.cfg, "size", int64(456)); got != tt.want {
				t.Errorf("parseByteSize() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCollectorStartNilGuard(t *testing.T) {
	var nilCollector *Collector
	if ok := nilCollector.Start(t.Context()); ok {
		t.Error("nil Collector.Start() = true, want false")
	}
}

func TestCollectorStart(t *testing.T) {
	tests := []struct {
		name   string
		proto  string
		tls    bool
		sender dest.Sender
	}{
		{
			name:   "http1_success",
			proto:  config.CollectorTypeHTTP,
			tls:    false,
			sender: fakeSender(nil),
		},
		{
			name:   "sender_error",
			proto:  config.CollectorTypeHTTP,
			tls:    false,
			sender: fakeSender(errors.New("fake sender error")),
		},
		{
			name:  "http2_success",
			proto: config.CollectorTypeHTTP,
			tls:   true,
		},
		{
			name:  "http3_failed_retries",
			proto: config.CollectorTypeHTTP3,
			tls:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.tls && r.TLS == nil {
					t.Errorf("expected TLS connection, got nil")
				}
				w.WriteHeader(http.StatusOK)
			}))
			if tt.tls {
				server.EnableHTTP2 = true
				server.StartTLS()
			} else {
				server.Start()
			}
			t.Cleanup(server.Close)

			fakeClient := server.Client()
			fakeTransport, _ := fakeClient.Transport.(*http.Transport)
			transportH2.Set("", fakeTransport)

			base, err := config.NewBaseCollector(map[string]any{"type": tt.proto, "schedule": "@once"}, tt.name)
			if err != nil {
				t.Fatalf("config.NewBaseCollector() error: %v", err)
			}
			if !tt.tls {
				base.Sender = tt.sender // For extra coverage.
			}

			c, err := NewCollector(base, map[string]any{
				"type":   tt.proto,
				tt.proto: map[string]any{"method": http.MethodGet, "url": server.URL},
			})
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}
			c.retries = 1
			c.timeout = time.Second / 4

			if !c.Start(t.Context()) {
				t.Fatal("Collector.Start(1) = false, want true")
			}
			if !c.Start(t.Context()) {
				t.Fatal("second Collector.Start(2) failed (should be idempotent)")
			}

			<-c.Done() // Wait for the collector's goroutine to finish its work.
		})
	}
}

func fakeSender(err error) dest.Sender {
	return func(_ context.Context, _ any) error {
		return err
	}
}

func TestScheduleNextRequest(t *testing.T) {
	tests := []struct {
		name     string
		schedule string
		cancel   bool
		isAsync  bool
	}{
		{
			name:     "context_cancellation",
			schedule: "@daily",
			cancel:   true,
		},
		{
			name:     "behind_schedule_skip",
			schedule: "@every 1s",
			isAsync:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				base, err := config.NewBaseCollector(map[string]any{
					"type":     config.CollectorTypeHTTP,
					"schedule": tt.schedule,
				}, tt.name)
				if err != nil {
					t.Fatalf("config.NewBaseCollector() error: %v", err)
				}

				c, err := NewCollector(base, map[string]any{
					"type": config.CollectorTypeHTTP,
					"http": map[string]any{"method": http.MethodGet, "url": "https://example.com"},
				})
				if err != nil {
					t.Fatalf("NewCollector() error: %v", err)
				}

				c.client = clientH2(&tls.Config{}, 0, c.timeout, tt.name)
				ctx, cancel := context.WithCancel(t.Context())
				c.cancelSched = cancel
				if tt.cancel {
					cancel()
				} else {
					t.Cleanup(cancel)
				}

				if !tt.isAsync {
					c.scheduleNext(ctx, ctx, time.Now())
					return
				}

				go c.scheduleNext(ctx, ctx, time.Now().Add(-5*time.Second))
				synctest.Wait()
				cancel()
				synctest.Wait()
			})
		})
	}
}

func TestFixHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		headers http.Header
		want    http.Header
	}{
		{
			name:    "nil_headers",
			headers: nil,
			want:    nil,
		},
		{
			name:    "empty_headers",
			headers: http.Header{},
			want:    http.Header{},
		},
		{
			name: "remove_headers",
			headers: http.Header{
				"Connection":       {"foo"},
				"Content-Encoding": {"gzip"},
				"Foo":              {"bar"},
				"Keep-Alive":       {"timeout=5", "max=1000"},
			},
			want: http.Header{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := fixHeaders(tt.headers)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("fixHeaders() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCollectorCloseTimeout(t *testing.T) {
	testTimeout := 5 * time.Second
	tests := []struct {
		name    string
		start   bool
		timeout time.Duration
		want    time.Duration
	}{
		{
			name:    "with_client",
			start:   true,
			timeout: testTimeout,
			want:    testTimeout,
		},
		{
			name:    "negative_timeout",
			start:   true,
			timeout: -1 * time.Second,
			want:    testTimeout,
		},
		{
			name:    "without_client",
			timeout: testTimeout,
			start:   false,
			want:    0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				base := &config.BaseCollector{Type: config.CollectorTypeHTTP}
				c, err := NewCollector(base, map[string]any{
					"type": config.CollectorTypeHTTP,
					"http": map[string]any{
						"method":  http.MethodGet,
						"url":     "https://example.com",
						"timeout": tt.timeout.String(),
					},
				})
				if err != nil {
					t.Fatalf("NewCollector() error: %v", err)
				}

				// Test case 1: Close() before Start() should return immediately and have no effect.
				start := time.Now()
				c.Close()
				if got := time.Since(start); got != 0 {
					t.Fatalf("Collector.Close(1) timeout behaved unexpectedly: got %v, want %v", got, 0)
				}

				// Test case 2: Close() after Start() should block until current request is done / the timeout expires.
				_, c.cancelSched = context.WithCancel(t.Context())
				if tt.start {
					c.client = clientH2(&tls.Config{}, 0, c.timeout, tt.name)
					c.inProgress.Go(func() {
						synctest.Sleep(testTimeout * 2)
					})
				}

				start = time.Now()
				c.Close()
				if got := time.Since(start); got != tt.want {
					t.Errorf("Collector.Close(2) timeout behaved unexpectedly: got %v, want %v", got, tt.want)
				}

				synctest.Sleep(testTimeout * 3)
			})
		})
	}
}

type fakeBlockingTransport struct {
	started chan struct{}
	unblock chan struct{}
	count   atomic.Int32
}

func (f *fakeBlockingTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	f.count.Add(1)

	select {
	case f.started <- struct{}{}:
	default:
	}

	<-f.unblock

	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
}

func TestCollectorConcurrencyLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		limit     int64
		wantCount int32
	}{
		{
			name:      "0_no_concurrency",
			limit:     0,
			wantCount: 1,
		},
		{
			name:      "1_no_concurrency",
			limit:     1,
			wantCount: 1,
		},
		{
			name:      "2_bounded_concurrency",
			limit:     2,
			wantCount: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				base, err := config.NewBaseCollector(map[string]any{
					"type":              config.CollectorTypeHTTP,
					"schedule":          "@every 1s",
					"concurrency_limit": tt.limit,
				}, tt.name)
				if err != nil {
					t.Fatalf("config.NewBaseCollector() error: %v", err)
				}

				c, err := NewCollector(base, map[string]any{
					"type": config.CollectorTypeHTTP,
					"http": map[string]any{"method": http.MethodGet, "url": "https://example.com"},
				})
				if err != nil {
					t.Fatalf("NewCollector() error: %v", err)
				}

				transport := &fakeBlockingTransport{
					started: make(chan struct{}, 10),
					unblock: make(chan struct{}),
				}
				c.client = &http.Client{Transport: transport}
				ctx, cancel := context.WithCancel(t.Context())
				c.cancelSched = cancel

				go c.scheduleNext(ctx, ctx, time.Now())

				// Advance to 1st tick.
				synctest.Sleep(time.Second)
				<-transport.started

				// Advance to 2nd tick.
				synctest.Sleep(time.Second)
				if tt.limit > 1 {
					<-transport.started
				}

				// Unblock in-flight requests and let them finish.
				close(transport.unblock)
				synctest.Sleep(10 * time.Millisecond)

				cancel()
				synctest.Wait()

				if got := transport.count.Load(); got != tt.wantCount {
					t.Errorf("executed requests = %d, want %d", got, tt.wantCount)
				}
			})
		})
	}
}

func TestCollectorCheckConcurrencyCanceled(t *testing.T) {
	t.Parallel()

	c := &Collector{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	c.checkConcurrency(ctx, t.Context(), sem, time.Now())

	if len(sem) != 1 {
		t.Errorf("len(sem) = %d, want 1", len(sem))
	}
}
