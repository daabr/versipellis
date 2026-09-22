package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daabr/versipellis/pkg/config"
)

func TestClientH3WithoutTLSReturnsNil(t *testing.T) {
	t.Parallel()

	client := clientH3(nil, 0, time.Second, "TestClientH3WithoutTLS")
	if client != nil {
		t.Errorf("expected nil client for HTTP/3 without TLS, got: %#v", client)
	}
}

func TestCollectorRequestWithRetries(t *testing.T) {
	_ = clientH2(&tls.Config{}, 0, time.Second, "TestCollectorRequestWithRetries")

	tests := []struct {
		name      string
		status    int
		retryable bool
	}{
		{"400", http.StatusBadRequest, false},
		{"404", http.StatusNotFound, false},
		{"405", http.StatusMethodNotAllowed, false},
		{"413", http.StatusRequestEntityTooLarge, false},
		{"431", http.StatusRequestHeaderFieldsTooLarge, false},
		{"501", http.StatusNotImplemented, false},
		{"503_retryable", http.StatusServiceUnavailable, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			handler := fakeHandler(t, 0, tt.status, "response body")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				handler(w, r)
			}))
			t.Cleanup(server.Close)

			base, err := config.NewBaseCollector(
				map[string]any{"type": config.CollectorTypeHTTP, "schedule": "@once"},
				tt.name, map[string]config.Sender{"": nil},
			)
			if err != nil {
				t.Fatalf("config.NewBaseCollector() error: %v", err)
			}

			c, err := NewCollector(base, map[string]any{
				"method": http.MethodGet, "url": server.URL,
				"retries": map[string]any{"type": retryTypeStatic, "interval": "1ms"},
			})
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}

			if !c.Start(t.Context()) {
				t.Fatal("Collector.Start() = false, want true")
			}

			<-c.Done() // Wait for the collector's goroutine to finish its work.

			wantRequests := 1
			if tt.retryable {
				wantRequests = c.retries.MaxAttempts
			}
			if got := int(requests.Load()); got != wantRequests {
				t.Errorf("handler received %d requests, want %d", got, wantRequests)
			}
		})
	}
}

func TestDestinationSendWithRetries(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		retryable bool
	}{
		{"200", http.StatusOK, false},
		{"400", http.StatusBadRequest, false},
		{"404", http.StatusNotFound, false},
		{"405", http.StatusMethodNotAllowed, false},
		{"413", http.StatusRequestEntityTooLarge, false},
		{"431", http.StatusRequestHeaderFieldsTooLarge, false},
		{"501", http.StatusNotImplemented, false},
		{"503_retryable", http.StatusServiceUnavailable, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			handler := fakeHandler(t, 0, tt.status, "response body")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				handler(w, r)
			}))
			t.Cleanup(server.Close)

			d, err := NewDestination(map[string]any{
				"method": http.MethodPost, "url": server.URL,
				"retries": map[string]any{"type": retryTypeStatic, "interval": "1ms"},
			}, tt.name, config.SenderTypeHTTP)
			if err != nil {
				t.Fatalf("NewDestination() error: %v", err)
			}

			d.Send(t.Context(), []byte("payload"))
			d.inProgress.Wait()

			wantRequests := 1
			if tt.retryable {
				wantRequests = d.retries.MaxAttempts
			}
			if got := int(requests.Load()); got != wantRequests {
				t.Errorf("handler received %d requests, want %d", got, wantRequests)
			}
		})
	}
}

func TestCollectorRequestOnceEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		methodErr bool
		headers   http.Header
		body      string
	}{
		{
			name:      "req_construction_error",
			methodErr: true,
		},
		{
			name:    "with_host_header_and_body",
			headers: http.Header{"Host": []string{"example.com"}},
			body:    "test body",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(fakeHandler(t, 0, http.StatusOK, tt.body))
			t.Cleanup(server.Close)

			base := &config.BaseCollector{Type: config.CollectorTypeHTTP, Name: tt.name}
			c, err := NewCollector(base, map[string]any{"method": http.MethodGet, "url": server.URL})
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}

			c.client = clientH2(&tls.Config{}, 0, 0, tt.name)
			if tt.methodErr {
				c.method = "???"
			}
			if tt.headers != nil {
				c.headers = tt.headers
			}
			if tt.body != "" {
				c.body = []byte(tt.body)
			}

			gotResp, gotRetry := c.requestOnce(t.Context())
			_ = gotResp.Body.Close()
			if gotRetry {
				t.Error("Collector.requestOnce() bool = true, want false")
			}
		})
	}
}

func TestDestinationSendOnceEdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		methodErr bool
		headers   http.Header
		body      []byte
	}{
		{
			name:      "req_construction_error",
			methodErr: true,
		},
		{
			name:    "with_host_header_and_body",
			headers: http.Header{"Host": []string{"example.com"}},
			body:    []byte("test body"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(fakeHandler(t, 0, http.StatusOK, string(tt.body)))
			t.Cleanup(server.Close)

			d, err := NewDestination(
				map[string]any{"method": http.MethodPost, "url": server.URL},
				tt.name, config.SenderTypeHTTP,
			)
			if err != nil {
				t.Fatalf("NewDestination() error: %v", err)
			}

			d.client = clientH2(&tls.Config{}, 0, 0, tt.name)
			if tt.methodErr {
				d.method = "???"
			}
			if tt.headers != nil {
				d.headers = tt.headers
			}
			getBody := func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(tt.body)), nil
			}

			gotResp, gotRetry := d.sendOnce(t.Context(), d.url, d.headers, getBody, int64(len(tt.body)))
			_ = gotResp.Body.Close()
			if gotRetry {
				t.Error("Destination.sendOnce() bool = true, want false")
			}
		})
	}
}

func TestCollectorProcessResponseErrors(t *testing.T) {
	tests := []struct {
		name       string
		maxSize    int64
		bodySize   int
		contentLen int
	}{
		{
			name:       "content_size_header_value_too_large",
			maxSize:    9,
			bodySize:   10,
			contentLen: 10,
		},
		{
			name:       "reader_error",
			maxSize:    9,
			bodySize:   10,
			contentLen: 9,
		},
		{
			name:       "reader_limit_exceeded",
			maxSize:    9,
			bodySize:   10,
			contentLen: -1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := strings.Repeat("A", tt.bodySize)
			server := httptest.NewServer(fakeHandler(t, tt.contentLen, http.StatusOK, body))
			t.Cleanup(server.Close)

			base := &config.BaseCollector{Type: config.CollectorTypeHTTP, Name: tt.name}
			c, err := NewCollector(base, map[string]any{
				"method": http.MethodGet, "url": server.URL, "max_body_size": tt.maxSize,
			})
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}
			c.client = clientH2(&tls.Config{}, 0, 0, tt.name)

			resp, _ := c.requestOnce(t.Context())
			_ = resp.Body.Close()

			if resp.ContentLength == 10 || resp.StatusCode == http.StatusOK {
				t.Errorf("Collector.requestOnce() resp = %v, wantErr = true", resp)
			}
		})
	}
}

func fakeHandler(t *testing.T, contentLength, statusCode int, body string) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, _ *http.Request) {
		if contentLength >= 0 {
			w.Header().Set("Content-Length", strconv.Itoa(contentLength))
		}
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte(body))

		if flusher, ok := w.(http.Flusher); ok && contentLength < 0 {
			flusher.Flush()
		}
	}
}

func TestCollectorRequestWithRetriesShutdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		shutdownBefore bool
		shutdownDuring bool
		cancelBefore   bool
	}{
		{
			name:           "shutdown_before_first_request",
			shutdownBefore: true,
		},
		{
			name:           "shutdown_during_retries",
			shutdownDuring: true,
		},
		{
			name:         "cancel_before_first_request",
			cancelBefore: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			schedCtx, cancelSched := context.WithCancel(t.Context())
			t.Cleanup(cancelSched)
			execCtx, cancelExec := context.WithCancel(t.Context())
			t.Cleanup(cancelExec)

			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				if tt.shutdownDuring {
					cancelSched() // Trigger shutdown on the first attempt so retries are aborted.
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			}))
			t.Cleanup(server.Close)

			base := &config.BaseCollector{Type: config.CollectorTypeHTTP, Name: tt.name}
			c, err := NewCollector(base, map[string]any{
				"method": http.MethodGet, "url": server.URL,
				"retries": map[string]any{"type": retryTypeStatic, "interval": "1ms"},
			})
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}
			c.client = clientH2(&tls.Config{}, 0, 0, tt.name)

			if tt.shutdownBefore || tt.cancelBefore {
				cancelExec() // Trigger cancellation of execution context immediately.
			}

			resp := c.requestWithRetries(schedCtx, execCtx)
			_ = resp.Body.Close()

			var wantRequests, wantStatusCode int
			switch {
			case tt.shutdownBefore, tt.cancelBefore:
				wantRequests, wantStatusCode = 0, http.StatusGatewayTimeout
			case tt.shutdownDuring:
				wantRequests, wantStatusCode = 1, http.StatusServiceUnavailable
			}

			if gotRequests := requests.Load(); gotRequests != int32(wantRequests) {
				t.Errorf("Collector.requestWithRetries() sent %d requests, want %d", gotRequests, wantRequests)
			}
			if resp.StatusCode != wantStatusCode {
				t.Errorf("Collector.requestWithRetries() status = %d, want %d", resp.StatusCode, wantStatusCode)
			}
		})
	}
}

func TestDestinationSendWithRetriesShutdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		before bool
		during bool
	}{
		{
			name:   "shutdown_before_first_request",
			before: true,
		},
		{
			name:   "shutdown_during_retries",
			during: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)

			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				if tt.during {
					cancel() // Trigger shutdown on the first attempt so retries are aborted.
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			}))
			t.Cleanup(server.Close)

			d, err := NewDestination(map[string]any{
				"method": http.MethodPost, "url": server.URL,
				"retries": map[string]any{"type": retryTypeStatic, "interval": "1ms"},
			}, tt.name, config.SenderTypeHTTP)
			if err != nil {
				t.Fatalf("NewDestination() error: %v", err)
			}

			if tt.before {
				cancel() // Trigger cancellation of execution context immediately.
			}

			getBody := func() (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("payload")), nil
			}
			d.sendWithRetries(ctx, d.url, d.headers, getBody, int64(len("payload")))

			wantRequests := 0
			if tt.during {
				wantRequests = 1
			}
			if gotRequests := requests.Load(); gotRequests != int32(wantRequests) {
				t.Errorf("Destination.sendWithRetries() sent %d requests, want %d", gotRequests, wantRequests)
			}
		})
	}
}

func TestCollectorRequestOnceNetworkErrors(t *testing.T) {
	t.Parallel()

	t.Run("timeout_returns_504", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
				w.WriteHeader(http.StatusOK)
			}
		}))
		t.Cleanup(server.Close)

		base := &config.BaseCollector{Type: config.CollectorTypeHTTP, Name: "timeout_test"}
		c, err := NewCollector(base, map[string]any{"method": http.MethodGet, "url": server.URL, "timeout": "25ms"})
		if err != nil {
			t.Fatalf("NewCollector() error: %v", err)
		}
		c.client = clientH2(&tls.Config{}, 0, c.timeout, "TestRequestOnceTimeout")

		resp, retry := c.requestOnce(t.Context())
		t.Cleanup(func() { _ = resp.Body.Close() })

		if resp.StatusCode != http.StatusGatewayTimeout {
			t.Errorf("Collector.requestOnce() StatusCode = %d, want %d", resp.StatusCode, http.StatusGatewayTimeout)
		}
		if !retry {
			t.Errorf("Collector.requestOnce() retry = false, want true")
		}
	})

	t.Run("connection_refused_returns_502", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
		serverURL := server.URL
		server.Close()

		base := &config.BaseCollector{Type: config.CollectorTypeHTTP, Name: "refused_test"}
		c, err := NewCollector(base, map[string]any{"method": http.MethodGet, "url": serverURL})
		if err != nil {
			t.Fatalf("NewCollector() error: %v", err)
		}
		c.client = clientH2(&tls.Config{}, 0, time.Second, "TestRequestOnceRefused")

		resp, retry := c.requestOnce(t.Context())
		t.Cleanup(func() { _ = resp.Body.Close() })

		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("Collector.requestOnce() StatusCode = %d, want %d", resp.StatusCode, http.StatusBadGateway)
		}
		if !retry {
			t.Errorf("Collector.requestOnce() retry = false, want true")
		}
	})
}

func TestDestinationSendOnceNetworkError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(server.Close)

	d, err := NewDestination(
		map[string]any{"method": http.MethodPut, "url": server.URL, "timeout": "25ms"},
		"TestDestinationSendOnceNetworkError", config.SenderTypeHTTP,
	)
	if err != nil {
		t.Fatalf("NewDestination() error: %v", err)
	}

	getBody := func() (io.ReadCloser, error) {
		return nil, nil
	}

	resp, retry := d.sendOnce(t.Context(), d.url, d.headers, getBody, 0)
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Errorf("Destination.sendOnce() StatusCode = %d, want %d", resp.StatusCode, http.StatusGatewayTimeout)
	}
	if !retry {
		t.Errorf("Destination.sendOnce() retry = false, want true")
	}
}
