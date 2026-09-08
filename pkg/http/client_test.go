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
	client := clientH3(nil, 0, time.Second, "TestClientH3WithoutTLS")
	if client != nil {
		t.Errorf("Expected nil client for HTTP/3 without TLS, got: %#v", client)
	}
}

func TestRequestWithRetriesNonRetryableError(t *testing.T) {
	_ = clientH2(&tls.Config{}, 0, time.Second, "TestRequestWithRetriesNonRetryableError")

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
			server := httptest.NewServer(fakeHandler(t, 0, tt.status, "Should not retry"))
			t.Cleanup(server.Close)

			base, err := config.NewBaseCollector(map[string]any{
				"type":     config.CollectorTypeHTTP,
				"schedule": "@once",
			}, tt.name)
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
			server := httptest.NewServer(fakeHandler(t, 0, http.StatusOK, tt.body))
			t.Cleanup(server.Close)

			base := &config.BaseCollector{Type: config.CollectorTypeHTTP, Name: tt.name}
			c, err := NewCollector(base, map[string]any{
				"type": config.CollectorTypeHTTP,
				"http": map[string]any{"method": http.MethodGet, "url": server.URL},
			})
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
			body := strings.Repeat("A", tt.bodySize)
			server := httptest.NewServer(fakeHandler(t, tt.contentLen, http.StatusOK, body))
			t.Cleanup(server.Close)

			base := &config.BaseCollector{Type: config.CollectorTypeHTTP, Name: tt.name}
			c, err := NewCollector(base, map[string]any{
				"type": config.CollectorTypeHTTP,
				"http": map[string]any{"method": http.MethodGet, "url": server.URL, "max_body_size": tt.maxSize},
			})
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}

			c.client = clientH2(&tls.Config{}, 0, 0, tt.name)
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
