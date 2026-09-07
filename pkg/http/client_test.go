package http

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daabr/versipellis/pkg/config"
)

func TestClientH3WithoutTLS(t *testing.T) {
	client := clientH3(nil, "test", time.Second)
	if client != nil {
		t.Errorf("Expected nil client for HTTP/3 without TLS, got: %#v", client)
	}
}

func TestRequestWithRetriesNonRetryableError(t *testing.T) {
	_ = clientH2(&tls.Config{}, "test", time.Second)

	tests := []struct {
		name   string
		status int
	}{
		{"404", http.StatusNotFound},
		{"405", http.StatusMethodNotAllowed},
		{"413", http.StatusRequestEntityTooLarge},
		{"431", http.StatusRequestHeaderFieldsTooLarge},
		{"501", http.StatusNotImplemented},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transportH2.Clear()
			t.Cleanup(transportH2.Clear)

			server := httptest.NewServer(fakeHandler(t, false, 0, tt.status, "Should not retry"))
			t.Cleanup(server.Close)

			base, err := config.NewBaseCollector(map[string]any{"type": config.CollectorTypeHTTP, "schedule": "@once"}, "name")
			if err != nil {
				t.Fatalf("config.NewBaseCollector() error: %v", err)
			}

			c, err := NewCollector(base, map[string]any{
				"type": config.CollectorTypeHTTP,
				"http": map[string]any{"method": http.MethodGet, "url": server.URL},
			})
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}
			if !c.Start(t.Context()) {
				t.Fatal("Collector.Start() = false, want true")
			}

			<-c.Done() // Wait for the collector's goroutine to finish its work.
		})
	}
}

func TestRequestOnceEdgeCases(t *testing.T) {
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
			name:    "with_body_and_host_header",
			body:    "test body",
			headers: http.Header{"Host": []string{"example.com"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transportH2.Clear()
			t.Cleanup(transportH2.Clear)

			server := httptest.NewServer(fakeHandler(t, false, 0, http.StatusOK, tt.body))
			t.Cleanup(server.Close)

			base, err := config.NewBaseCollector(map[string]any{"type": config.CollectorTypeHTTP, "schedule": "@once"}, "name")
			if err != nil {
				t.Fatalf("config.NewBaseCollector() error: %v", err)
			}

			c, err := NewCollector(base, map[string]any{
				"type": config.CollectorTypeHTTP,
				"http": map[string]any{"method": http.MethodGet, "url": server.URL},
			})
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}

			c.client = clientH2(&tls.Config{}, "test", 0)
			if tt.methodErr {
				c.method = "???"
			}
			if tt.headers != nil {
				c.headers = tt.headers
			}
			if tt.body != "" {
				c.body = []byte(tt.body)
			}
			ctx, cancel := context.WithCancel(t.Context())
			c.done = ctx.Done()
			c.cancel = cancel
			t.Cleanup(cancel)

			gotResp, gotRetry := c.requestOnce(ctx)
			_ = gotResp.Body.Close()
			if gotRetry {
				t.Error("requestOnce() bool = true, want false")
			}
		})
	}
}

func TestProcessResponseErrors(t *testing.T) {
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
			maxBodyBytes = tt.maxSize // Package variable.
			t.Cleanup(func() { maxBodyBytes = 10 << 20 })

			transportH2.Clear()
			t.Cleanup(transportH2.Clear)

			body := strings.Repeat("A", tt.bodySize)
			server := httptest.NewServer(fakeHandler(t, false, tt.contentLen, http.StatusOK, body))
			t.Cleanup(server.Close)

			base, err := config.NewBaseCollector(map[string]any{"type": config.CollectorTypeHTTP, "schedule": "@once"}, "name")
			if err != nil {
				t.Fatalf("config.NewBaseCollector() error: %v", err)
			}

			c, err := NewCollector(base, map[string]any{
				"type": config.CollectorTypeHTTP,
				"http": map[string]any{"method": http.MethodGet, "url": server.URL},
			})
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}

			c.client = clientH2(&tls.Config{}, "test", 0)
			ctx, cancel := context.WithCancel(t.Context())
			c.done = ctx.Done()
			c.cancel = cancel
			t.Cleanup(cancel)

			resp, _ := c.requestOnce(ctx)
			_ = resp.Body.Close()

			if resp.ContentLength == 10 || resp.StatusCode == http.StatusOK {
				t.Errorf("requestOnce() resp = %v, wantErr = true", resp)
			}
		})
	}
}

func fakeHandler(t *testing.T, tls bool, contentLength, statusCode int, body string) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		if tls && r.TLS == nil {
			t.Errorf("expected TLS connection, got nil")
		}

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
