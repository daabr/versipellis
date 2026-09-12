package http

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daabr/versipellis/pkg/config"
)

func TestClientH3WithoutTLSReturnsNil(t *testing.T) {
	client := clientH3(nil, 0, time.Second, "TestClientH3WithoutTLS")
	if client != nil {
		t.Errorf("Expected nil client for HTTP/3 without TLS, got: %#v", client)
	}
}

func TestRequestWithRetries(t *testing.T) {
	_ = clientH2(&tls.Config{}, 0, time.Second, "TestRequestWithRetries")

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

			base, err := config.NewBaseCollector(map[string]any{"type": config.CollectorTypeHTTP, "schedule": "@once"}, tt.name)
			if err != nil {
				t.Fatalf("config.NewBaseCollector() error: %v", err)
			}

			c, err := NewCollector(base, map[string]any{
				"type": config.CollectorTypeHTTP,
				"http": map[string]any{
					"method": http.MethodGet, "url": server.URL,
					"retries": map[string]any{"type": retryTypeStatic, "interval": "1ms"},
				},
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

			gotResp, gotRetry := c.requestOnce(t.Context())
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

			resp, _ := c.requestOnce(t.Context())
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

func TestRequestWithRetriesShutdown(t *testing.T) {
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
				"type": config.CollectorTypeHTTP,
				"http": map[string]any{
					"method": http.MethodGet, "url": server.URL,
					"retries": map[string]any{"type": retryTypeStatic, "interval": "1ms"},
				},
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
				t.Errorf("requestWithRetries() sent %d requests, want %d", gotRequests, wantRequests)
			}
			if resp.StatusCode != wantStatusCode {
				t.Errorf("requestWithRetries() status = %d, want %d", resp.StatusCode, wantStatusCode)
			}
		})
	}
}

func TestRequestOnceNetworkErrors(t *testing.T) {
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
		c, err := NewCollector(base, map[string]any{
			"type": config.CollectorTypeHTTP,
			"http": map[string]any{
				"method":  http.MethodGet,
				"url":     server.URL,
				"timeout": "25ms",
			},
		})
		if err != nil {
			t.Fatalf("NewCollector() error: %v", err)
		}
		c.client = clientH2(&tls.Config{}, 0, c.timeout, "TestRequestOnceTimeout")

		resp, retry := c.requestOnce(t.Context())
		t.Cleanup(func() { _ = resp.Body.Close() })

		if resp.StatusCode != http.StatusGatewayTimeout {
			t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusGatewayTimeout)
		}
		if !retry {
			t.Errorf("retry = false, want true")
		}
	})

	t.Run("connection_refused_returns_502", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
		serverURL := server.URL
		server.Close()

		base := &config.BaseCollector{Type: config.CollectorTypeHTTP, Name: "refused_test"}
		c, err := NewCollector(base, map[string]any{
			"type": config.CollectorTypeHTTP,
			"http": map[string]any{"method": http.MethodGet, "url": serverURL},
		})
		if err != nil {
			t.Fatalf("NewCollector() error: %v", err)
		}
		c.client = clientH2(&tls.Config{}, 0, time.Second, "TestRequestOnceRefused")

		resp, retry := c.requestOnce(t.Context())
		t.Cleanup(func() { _ = resp.Body.Close() })

		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusBadGateway)
		}
		if !retry {
			t.Errorf("retry = false, want true")
		}
	})
}

func TestTLSClient(t *testing.T) {
	t.Parallel()

	var count atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "server_cert.pem")
	pemBlock := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(path, pemBlock, 0o600); err != nil {
		t.Fatalf("failed to write CA certificate: %v", err)
	}

	base, err := config.NewBaseCollector(map[string]any{"type": config.CollectorTypeHTTP, "schedule": "@once"}, "TestTLSClient")
	if err != nil {
		t.Fatalf("config.NewBaseCollector() error: %v", err)
	}

	c, err := NewCollector(base, map[string]any{
		"type": config.CollectorTypeHTTP,
		"http": map[string]any{
			"method": http.MethodGet,
			"url":    server.URL,
			"tls": map[string]any{
				"server_ca_cert_file": path,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewCollector() error: %v", err)
	}

	if !c.Start(t.Context()) {
		t.Fatalf("failed to start collector")
	}

	<-c.Done()

	if n := count.Load(); n != 1 {
		t.Errorf("expected server to be called exactly once, got %d", n)
	}

	resp, retry := c.requestOnce(t.Context())
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if retry {
		t.Errorf("retry = true, want false")
	}
	if n := count.Load(); n != 2 {
		t.Errorf("expected server to be called exactly twice, got %d", n)
	}
}

func TestMTLSClientAndServer(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	caPEM, _, caCert, caKey := generateTestCert(t, true, nil, nil)
	clientPEM, clientKeyPEM, _, _ := generateTestCert(t, false, caCert, caKey)
	serverPEM, serverKeyPEM, _, _ := generateTestCert(t, false, caCert, caKey)

	serverCert, err := tls.X509KeyPair(serverPEM, serverKeyPEM)
	if err != nil {
		t.Fatalf("tls.X509KeyPair() error: %v", err)
	}
	clientPool := x509.NewCertPool()
	clientPool.AppendCertsFromPEM(caPEM)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientPool,
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	caFile := filepath.Join(tempDir, "ca.pem")
	certFile := filepath.Join(tempDir, "client.pem")
	keyFile := filepath.Join(tempDir, "client.key")
	writeTestFile(t, caFile, caPEM)
	writeTestFile(t, certFile, clientPEM)
	writeTestFile(t, keyFile, clientKeyPEM)

	cfg := map[string]any{"type": config.CollectorTypeHTTP, "schedule": "@once"}
	base, err := config.NewBaseCollector(cfg, "TestMTLSClientAndServer")
	if err != nil {
		t.Fatalf("config.NewBaseCollector() error: %v", err)
	}

	c, err := NewCollector(base, map[string]any{
		"type": config.CollectorTypeHTTP,
		"http": map[string]any{
			"method": http.MethodGet,
			"url":    server.URL,
			"tls": map[string]any{
				"server_ca_cert_file":   caFile,
				"mtls_client_cert_file": certFile,
				"mtls_client_key_file":  keyFile,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewCollector error: %v", err)
	}

	if !c.Start(t.Context()) {
		t.Fatalf("failed to start collector")
	}
	<-c.Done()

	resp, retry := c.requestOnce(t.Context())
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK || retry {
		t.Fatalf("mTLS request failed with status %d, retry=%v", resp.StatusCode, retry)
	}
}
