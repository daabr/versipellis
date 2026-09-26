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
	"github.com/daabr/versipellis/pkg/dest"
)

func TestClientH3WithoutTLSReturnsNil(t *testing.T) {
	t.Parallel()

	client := clientH3(nil, 0, time.Second, "TestClientH3WithoutTLS")
	if client != nil {
		t.Errorf("expected nil client for HTTP/3 without TLS, got: %#v", client)
	}
}

func TestCollectorRequestWithRetries(t *testing.T) {
	t.Parallel()

	_ = clientH2(&tls.Config{}, 0, time.Second, "TestCollectorRequestWithRetries")

	tests := []struct {
		name      string
		status    int
		retryable bool
	}{
		{"coll_200", http.StatusOK, false},
		{"coll_300", http.StatusMultipleChoices, false},
		{"coll_400", http.StatusBadRequest, false},
		{"coll_404", http.StatusNotFound, false},
		{"coll_405", http.StatusMethodNotAllowed, false},
		{"coll_413", http.StatusRequestEntityTooLarge, false},
		{"coll_431", http.StatusRequestHeaderFieldsTooLarge, false},
		{"coll_501", http.StatusNotImplemented, false},
		{"coll_503_retryable", http.StatusServiceUnavailable, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var requests atomic.Int32
			handler := fakeHandler(t, 0, tt.status, "response body")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				handler(w, r)
			}))
			t.Cleanup(server.Close)

			base, err := config.NewBaseCollector(
				map[string]any{"type": config.CollectorTypeHTTP, "schedule": "@once"},
				tt.name, map[string]config.Sender{"": dest.Discard},
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
			c.transportID += tt.name

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

func TestSenderSendWithRetries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		retryable bool
	}{
		{"send_200", http.StatusOK, false},
		{"send_300", http.StatusMultipleChoices, false},
		{"send_400", http.StatusBadRequest, false},
		{"send_404", http.StatusNotFound, false},
		{"send_405", http.StatusMethodNotAllowed, false},
		{"send_413", http.StatusRequestEntityTooLarge, false},
		{"send_431", http.StatusRequestHeaderFieldsTooLarge, false},
		{"send_501", http.StatusNotImplemented, false},
		{"send_503_retryable", http.StatusServiceUnavailable, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var requests atomic.Int32
			handler := fakeHandler(t, 0, tt.status, "response body")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				handler(w, r)
			}))
			t.Cleanup(server.Close)

			sender, err := NewSender(map[string]any{
				"method": http.MethodPost, "url": server.URL,
				"retries": map[string]any{"type": retryTypeStatic, "interval": "1ms"},
			}, tt.name, config.SenderTypeHTTP)
			if err != nil {
				t.Fatalf("NewSender() error: %v", err)
			}

			sender.Send(t.Context(), []byte("payload"))
			sender.inProgress.Wait()

			wantRequests := 1
			if tt.retryable {
				wantRequests = sender.retries.MaxAttempts
			}
			if got := int(requests.Load()); got != wantRequests {
				t.Errorf("handler received %d requests, want %d", got, wantRequests)
			}
		})
	}
}

func TestCollectorRequestOnceEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		methodErr bool
		headers   http.Header
		body      string
	}{
		{
			name:      "coll_req_construction_error",
			methodErr: true,
		},
		{
			name:    "coll_with_host_header_and_body",
			headers: http.Header{"Host": []string{"example.com"}},
			body:    "test body",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

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

func TestSenderSendOnceEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		methodErr bool
		headers   http.Header
		body      []byte
	}{
		{
			name:      "send_req_construction_error",
			methodErr: true,
		},
		{
			name:    "send_with_host_header_and_body",
			headers: http.Header{"Host": []string{"example.com"}},
			body:    []byte("test body"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(fakeHandler(t, 0, http.StatusOK, string(tt.body)))
			t.Cleanup(server.Close)

			sender, err := NewSender(
				map[string]any{"method": http.MethodPost, "url": server.URL},
				tt.name, config.SenderTypeHTTP,
			)
			if err != nil {
				t.Fatalf("NewSender() error: %v", err)
			}

			sender.client = clientH2(&tls.Config{}, 0, 0, tt.name)
			if tt.methodErr {
				sender.method = "???"
			}
			if tt.headers != nil {
				sender.headers = tt.headers
			}
			getBody := func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(tt.body)), nil
			}

			gotResp, gotRetry := sender.sendOnce(t.Context(), sender.url, sender.headers, getBody, int64(len(tt.body)))
			_ = gotResp.Body.Close()
			if gotRetry {
				t.Error("Sender.sendOnce() bool = true, want false")
			}
		})
	}
}

func TestCollectorProcessResponseErrors(t *testing.T) {
	t.Parallel()

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
			t.Parallel()

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

			schedCtx, cancelSched := context.WithCancel(t.Context())
			t.Cleanup(cancelSched)
			execCtx, cancelExec := context.WithCancel(t.Context())
			t.Cleanup(cancelExec)

			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				if tt.during {
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

			if tt.before {
				cancelExec() // Trigger cancellation of execution context immediately.
			}

			resp := c.requestWithRetries(schedCtx, execCtx)
			_ = resp.Body.Close()

			want := 0
			if tt.during {
				want = 1
			}
			if gotRequests := requests.Load(); gotRequests != int32(want) {
				t.Errorf("Collector.requestWithRetries() sent %d requests, want %d", gotRequests, want)
			}
			want = http.StatusServiceUnavailable
			if resp.StatusCode != want {
				t.Errorf("Collector.requestWithRetries() status = %d, want %d", resp.StatusCode, want)
			}
		})
	}
}

func TestSenderSendWithRetriesShutdown(t *testing.T) {
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

			sender, err := NewSender(map[string]any{
				"method": http.MethodPost, "url": server.URL,
				"retries": map[string]any{"type": retryTypeStatic, "interval": "1ms"},
			}, tt.name, config.SenderTypeHTTP)
			if err != nil {
				t.Fatalf("NewSender() error: %v", err)
			}

			if tt.before {
				cancel() // Trigger cancellation of execution context immediately.
			}

			getBody := func() (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("payload")), nil
			}
			sender.sendWithRetries(ctx, sender.url, sender.headers, getBody, int64(len("payload")))

			want := 0
			if tt.during {
				want = 1
			}
			if gotRequests := requests.Load(); gotRequests != int32(want) {
				t.Errorf("Sender.sendWithRetries() sent %d requests, want %d", gotRequests, want)
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

func TestSenderSendOnceNetworkError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(server.Close)

	sender, err := NewSender(
		map[string]any{"method": http.MethodPut, "url": server.URL, "timeout": "25ms"},
		"TestSenderSendOnceNetworkError", config.SenderTypeHTTP,
	)
	if err != nil {
		t.Fatalf("NewSender() error: %v", err)
	}

	getBody := func() (io.ReadCloser, error) {
		return nil, nil
	}

	resp, retry := sender.sendOnce(t.Context(), sender.url, sender.headers, getBody, 0)
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Errorf("Sender.sendOnce() StatusCode = %d, want %d", resp.StatusCode, http.StatusGatewayTimeout)
	}
	if !retry {
		t.Errorf("Sender.sendOnce() retry = false, want true")
	}
}
