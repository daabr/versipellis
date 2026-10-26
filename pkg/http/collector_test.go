package http

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
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
			name:    "invalid_url",
			base:    &config.BaseCollector{Type: config.CollectorTypeHTTP},
			cfg:     map[string]any{"http": map[string]any{"url": ""}},
			wantErr: true,
		},
		{
			name:    "invalid_method",
			base:    &config.BaseCollector{Type: config.CollectorTypeHTTP},
			cfg:     map[string]any{"url": "https://example.com", "method": "DELETE"},
			wantErr: true,
		},
		{
			name:    "invalid_query_type",
			base:    &config.BaseCollector{Type: config.CollectorTypeHTTP},
			cfg:     map[string]any{"url": "https://example.com", "query": 123},
			wantErr: true,
		},
		{
			name:    "invalid_format",
			base:    &config.BaseCollector{Type: config.CollectorTypeHTTP},
			cfg:     map[string]any{"url": "https://example.com", "format": "invalid"},
			wantErr: true,
		},
		{
			name:    "headers_not_a_table",
			base:    &config.BaseCollector{Type: config.CollectorTypeHTTP},
			cfg:     map[string]any{"url": "https://example.com", "headers": "Accept: application/json"},
			wantErr: true,
		},
		{
			name:    "body_file_not_found",
			base:    &config.BaseCollector{Type: config.CollectorTypeHTTP},
			cfg:     map[string]any{"url": "https://example.com", "body_file": "/nonexistent/file"},
			wantErr: true,
		},
		{
			name:    "invalid_retries",
			base:    &config.BaseCollector{Type: config.CollectorTypeHTTP},
			cfg:     map[string]any{"url": "https://example.com", "retries": "invalid"},
			wantErr: true,
		},
		{
			name:    "tls_not_a_table",
			base:    &config.BaseCollector{Type: config.CollectorTypeHTTP},
			cfg:     map[string]any{"url": "https://example.com", "tls": "invalid"},
			wantErr: true,
		},
		{
			name:        "http_url_with_tls_config",
			base:        &config.BaseCollector{Type: config.CollectorTypeHTTP},
			cfg:         map[string]any{"url": "http://example.com", "tls": map[string]any{"min_version": "1.3"}},
			wantErr:     false, // Logs a warning, does not abort.
			wantURL:     "http://example.com",
			wantMethod:  http.MethodGet,
			wantHeaders: http.Header{},
			wantTimeout: defaultRequestTimeout,
		},
		{
			name:    "invalid_timeout",
			base:    &config.BaseCollector{Type: config.CollectorTypeHTTP},
			cfg:     map[string]any{"url": "https://example.com", "timeout": "not-a-duration"},
			wantErr: true,
		},
		{
			name:        "minimal_valid_config",
			base:        &config.BaseCollector{Type: config.CollectorTypeHTTP},
			cfg:         map[string]any{"url": "https://example.com"},
			wantURL:     "https://example.com",
			wantMethod:  http.MethodGet,
			wantHeaders: http.Header{},
			wantTimeout: defaultRequestTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			coll, gotErr := NewCollector(tt.base, tt.cfg)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("NewCollector() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			if b := coll.Base(); !reflect.DeepEqual(b, tt.base) {
				t.Errorf("Collector.Base() = %+v, want %+v", b, tt.base)
			}
			if got := coll.url.String(); got != tt.wantURL {
				t.Errorf("Collector.url = %q, want %q", got, tt.wantURL)
			}
			if coll.method != tt.wantMethod {
				t.Errorf("Collector.method = %q, want %q", coll.method, tt.wantMethod)
			}
			if !reflect.DeepEqual(coll.headers, tt.wantHeaders) {
				t.Errorf("Collector.headers = %+v, want %+v", coll.headers, tt.wantHeaders)
			}
			if coll.timeout != tt.wantTimeout {
				t.Errorf("Collector.timeout = %v, want %v", coll.timeout, tt.wantTimeout)
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

func TestCollectorStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		proto string
		tls   bool
	}{
		{
			name:  "http1_success",
			proto: config.CollectorTypeHTTP,
			tls:   false,
		},
		{
			name:  "sender_error",
			proto: config.CollectorTypeHTTP,
			tls:   false,
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
			t.Parallel()

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

			base, err := config.NewBaseCollector(
				map[string]any{"type": tt.proto, "schedule": "@once", "destination": "test"},
				tt.name, map[string]config.Sender{"": nil, "test": dest.Discard},
			)
			if err != nil {
				t.Fatalf("config.NewBaseCollector() error: %v", err)
			}

			coll, err := NewCollector(base, map[string]any{
				"method": http.MethodGet, "url": server.URL, "timeout": "100ms",
				"retries": map[string]any{"type": retryTypeStatic, "max_attempts": int64(2), "interval": "1ms"},
			})
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}

			coll.transportID += tt.name
			fakeClient := server.Client()
			fakeTransport, _ := fakeClient.Transport.(*http.Transport)
			transportH2.Set(coll.transportID, fakeTransport)

			if !coll.Start(t.Context()) {
				t.Fatal("Collector.Start() = false, want true")
			}

			<-coll.Done() // Wait for the collector's goroutine to finish its work.
		})
	}
}

func TestScheduleNextRequest(t *testing.T) {
	t.Parallel()

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
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				base, err := config.NewBaseCollector(
					map[string]any{"type": config.CollectorTypeHTTP, "schedule": tt.schedule},
					tt.name, map[string]config.Sender{"": dest.Discard},
				)
				if err != nil {
					t.Fatalf("config.NewBaseCollector() error: %v", err)
				}

				coll, err := NewCollector(base, map[string]any{"method": http.MethodGet, "url": "https://example.com"})
				if err != nil {
					t.Fatalf("NewCollector() error: %v", err)
				}

				// A real client would send a real request, which would fail and leave a retry pending when the test ends.
				transport := &fakeBlockingTransport{started: make(chan struct{}, 10), unblock: make(chan struct{})}
				close(transport.unblock) // Respond immediately.
				coll.client = &http.Client{Transport: transport}

				ctx, cancel := context.WithCancel(t.Context())
				schedCtx, execCtx := coll.state.StartWith(ctx)
				if tt.cancel {
					cancel()
				} else {
					t.Cleanup(cancel)
				}

				if !tt.isAsync {
					coll.scheduleNext(schedCtx, execCtx, time.Now())
					return
				}

				go coll.scheduleNext(schedCtx, execCtx, time.Now().Add(-5*time.Second))
				synctest.Wait()
				cancel()
				<-coll.Done()
			})
		})
	}
}

func TestCollectorCloseTimeout(t *testing.T) {
	t.Parallel()

	testTimeout := CloseTimeout + AbortTimeout

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
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				base := &config.BaseCollector{Type: config.CollectorTypeHTTP}
				coll, err := NewCollector(base, map[string]any{
					"method":  http.MethodGet,
					"url":     "https://example.com",
					"timeout": tt.timeout.String(),
				})
				if err != nil {
					t.Fatalf("NewCollector() error: %v", err)
				}

				// Test case 1: Close() before Start() should return immediately and have no effect.
				start := time.Now()
				coll.Close(t.Context())
				if got := time.Since(start); got != 0 {
					t.Fatalf("Collector.Close(1) timeout behaved unexpectedly: got %v, want %v", got, 0)
				}

				// Test case 2: Close() after Start() should block until current request is done / the timeout expires.
				coll.state.StartWith(t.Context())
				if tt.start {
					coll.client = clientH2(&tls.Config{}, 0, coll.timeout, tt.name)
					coll.state.InProgress.Go(func() {
						synctest.Sleep(testTimeout * 2)
					})
				}

				start = time.Now()
				coll.Close(t.Context())
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
				base, err := config.NewBaseCollector(
					map[string]any{"type": config.CollectorTypeHTTP, "schedule": "@every 1s", "concurrency_limit": tt.limit},
					tt.name, map[string]config.Sender{"": dest.Discard},
				)
				if err != nil {
					t.Fatalf("config.NewBaseCollector() error: %v", err)
				}

				coll, err := NewCollector(base, map[string]any{"method": http.MethodGet, "url": "https://example.com"})
				if err != nil {
					t.Fatalf("NewCollector() error: %v", err)
				}

				transport := &fakeBlockingTransport{
					started: make(chan struct{}, 10),
					unblock: make(chan struct{}),
				}
				coll.client = &http.Client{Transport: transport}
				ctx, cancel := context.WithCancel(t.Context())
				t.Cleanup(cancel)
				schedCtx, execCtx := coll.state.StartWith(ctx)

				go coll.scheduleNext(schedCtx, execCtx, time.Now())

				// Advance to 1st tick.
				synctest.Sleep(time.Second)
				<-transport.started

				// Advance to 2nd tick.
				synctest.Sleep(time.Second)
				if tt.limit > 1 {
					<-transport.started
				}

				// Unblock ongoing requests and let them finish.
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

func TestCollectorRequestWithConcurrencyLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		canceled  bool // The scheduling context is canceled, i.e., the collector is shutting down.
		semFull   bool // The concurrency limit was already reached.
		wantCount int32
	}{
		{
			name:      "sends_request",
			wantCount: 1,
		},
		{
			name:    "skips_at_concurrency_limit",
			semFull: true,
		},
		{
			name:     "skips_when_canceled",
			canceled: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base, err := config.NewBaseCollector(
				map[string]any{"type": config.CollectorTypeHTTP, "schedule": "@once"},
				tt.name, map[string]config.Sender{"": dest.Discard},
			)
			if err != nil {
				t.Fatalf("config.NewBaseCollector() error: %v", err)
			}
			coll, err := NewCollector(base, map[string]any{"method": http.MethodGet, "url": "https://example.com"})
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}

			transport := &fakeBlockingTransport{started: make(chan struct{}, 1), unblock: make(chan struct{})}
			close(transport.unblock) // Respond immediately.
			coll.client = &http.Client{Transport: transport}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			schedCtx, execCtx := coll.state.StartWith(ctx)
			if tt.canceled {
				cancel()
			}

			// The semaphore has a free slot unless the test case fills it, so skipping the request
			// in the "canceled" test case is only possible because of the cancellation check.
			sem := make(chan struct{}, 1)
			if tt.semFull {
				sem <- struct{}{}
			}

			coll.requestWithConcurrencyLimit(schedCtx, execCtx, sem, time.Now())
			coll.state.InProgress.Wait()

			if got := transport.count.Load(); got != tt.wantCount {
				t.Errorf("sent requests = %d, want %d", got, tt.wantCount)
			}
			wantLen := 0
			if tt.semFull {
				wantLen = 1
			}
			if got := len(sem); got != wantLen {
				t.Errorf("semaphore slots in use = %d, want %d", got, wantLen)
			}
		})
	}
}
