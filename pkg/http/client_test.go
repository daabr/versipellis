package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/daabr/versipellis/pkg/codec"
	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/dest"
	"github.com/daabr/versipellis/pkg/flow"
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
		{"200", http.StatusOK, false},
		{"300", http.StatusMultipleChoices, false},
		{"400", http.StatusBadRequest, false},
		{"404", http.StatusNotFound, false},
		{"405", http.StatusMethodNotAllowed, false},
		{"413", http.StatusRequestEntityTooLarge, false},
		{"422", http.StatusUnprocessableEntity, false},
		{"431", http.StatusRequestHeaderFieldsTooLarge, false},
		{"501", http.StatusNotImplemented, false},
		{"503_retryable", http.StatusServiceUnavailable, true},
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

func TestSenderSendWithRetries(t *testing.T) { //nolint:paralleltest // Don't share transports between tests.
	tests := []struct {
		name      string
		status    int
		retryable bool
	}{
		{"200", http.StatusOK, false},
		{"300", http.StatusMultipleChoices, false},
		{"400", http.StatusBadRequest, false},
		{"404", http.StatusNotFound, false},
		{"405", http.StatusMethodNotAllowed, false},
		{"413", http.StatusRequestEntityTooLarge, false},
		{"422", http.StatusUnprocessableEntity, false},
		{"431", http.StatusRequestHeaderFieldsTooLarge, false},
		{"501", http.StatusNotImplemented, false},
		{"503_retryable", http.StatusServiceUnavailable, true},
	}
	for _, tt := range tests { //nolint:paralleltest // Don't share transports between tests.
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			handler := fakeHandler(t, 0, tt.status, "response body")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				handler(w, r)
			}))
			t.Cleanup(server.Close)

			sender, err := NewSender(t.Context(), map[string]any{
				"method": http.MethodPost, "url": server.URL,
				"retries": map[string]any{"type": retryTypeStatic, "interval": "1ms"},
			}, tt.name, config.SenderTypeHTTP, nil)
			if err != nil {
				t.Fatalf("NewSender() error: %v", err)
			}
			fallback, fallbackSent := recordSends(t)
			sender.fallback = fallback

			sender.Send(t.Context(), flow.Blobs{[]byte("payload")}, codec.FormatDefault)
			sender.state.InProgress.Wait()

			wantRequests := 1
			if tt.retryable {
				wantRequests = sender.retries.MaxAttempts
			}
			if got := int(requests.Load()); got != wantRequests {
				t.Errorf("handler received %d requests, want %d", got, wantRequests)
			}

			// Non-retryable errors and too many retries must pass the data to the fallback sender (the DLQ).
			var wantFallback []string
			if tt.status > MaxSuccessfulStatusCode {
				wantFallback = []string{"blobs: payload (raw)"}
			}
			if got := fallbackSent(); !slices.Equal(got, wantFallback) {
				t.Errorf("sent to fallback = %q, want %q", got, wantFallback)
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
		badBody   bool
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
		{
			name:    "send_with_bad_body",
			headers: http.Header{"Host": []string{"example.com"}},
			badBody: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(fakeHandler(t, 0, http.StatusOK, string(tt.body)))
			t.Cleanup(server.Close)

			sender, err := NewSender(t.Context(),
				map[string]any{"method": http.MethodPost, "url": server.URL},
				tt.name, config.SenderTypeHTTP, nil,
			)
			if err != nil {
				t.Fatalf("NewSender() error: %v", err)
			}

			sender.timeout = 0
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
			if tt.badBody {
				getBody = func() (io.ReadCloser, error) {
					return nil, errors.New("bad body")
				}
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
			coll, err := NewCollector(base, map[string]any{
				"method": http.MethodGet, "url": server.URL,
				"retries": map[string]any{"type": retryTypeStatic, "interval": "1ms"},
			})
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}
			coll.client = clientH2(&tls.Config{}, 0, 0, tt.name)

			if tt.before {
				cancelExec() // Trigger cancellation of execution context immediately.
			}

			resp := coll.requestWithRetries(schedCtx, execCtx)
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

			sender, err := NewSender(t.Context(), map[string]any{
				"method": http.MethodPost, "url": server.URL,
				"retries": map[string]any{"type": retryTypeStatic, "interval": "1ms"},
			}, tt.name, config.SenderTypeHTTP, nil)
			if err != nil {
				t.Fatalf("NewSender() error: %v", err)
			}

			if tt.before {
				cancel() // Trigger cancellation of execution context immediately.
			}

			getBody := func() (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("payload")), nil
			}
			var rejected int
			reject := func() string {
				rejected++
				return config.SenderTypeDLQ
			}
			sender.sendWithRetries(ctx, sender.url, sender.headers, getBody, int64(len("payload")), reject)

			want := 0
			if tt.during {
				want = 1
			}
			if gotRequests := requests.Load(); gotRequests != int32(want) {
				t.Errorf("Sender.sendWithRetries() sent %d requests, want %d", gotRequests, want)
			}
			// Requests or retries that were aborted by a shutdown must be rejected (i.e. sent to the DLQ).
			if rejected != 1 {
				t.Errorf("Sender.sendWithRetries() called reject %d times, want 1", rejected)
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

	sender, err := NewSender(t.Context(),
		map[string]any{"method": http.MethodPut, "url": server.URL, "timeout": "25ms"},
		"TestSenderSendOnceNetworkError", config.SenderTypeHTTP, nil,
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

// newRefreshFailingServer returns a server whose OAuth token endpoint ("/token") issues one token that's already
// expired (within the refresh margin of [oauth2.ReuseTokenSource]), so every later [auth.Config.SetHeader] call
// has to refresh it, and then fails every refresh with a 500 status. Other paths are data endpoints.
func newRefreshFailingServer(t *testing.T) (server *httptest.Server, tokenRequests, dataRequests *atomic.Int32) {
	t.Helper()

	tokenRequests, dataRequests = new(atomic.Int32), new(atomic.Int32)
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			dataRequests.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		if tokenRequests.Add(1) > 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"t0k3n","token_type":"Bearer","expires_in":1}`))
	}))
	t.Cleanup(server.Close)

	return server, tokenRequests, dataRequests
}

func oauthTestConfig(tokenURL string) map[string]any {
	return map[string]any{
		"type":                "oauth",
		"oauth_client_id":     "id",
		"oauth_client_secret": "secret",
		"oauth_token_url":     tokenURL, // Plain HTTP is allowed for loopback hosts.
	}
}

// A failed token refresh must be retried like a failed request, without dereferencing a nil response.
func TestCollectorTokenRefreshFailure(t *testing.T) {
	t.Parallel()

	server, tokenRequests, dataRequests := newRefreshFailingServer(t)
	base, err := config.NewBaseCollector(
		map[string]any{"type": config.CollectorTypeHTTP, "schedule": "@once", "destination": "test"},
		t.Name(), map[string]config.Sender{"": nil, "test": dest.Discard},
	)
	if err != nil {
		t.Fatalf("config.NewBaseCollector() error: %v", err)
	}
	coll, err := NewCollector(base, map[string]any{
		"method": http.MethodGet, "url": server.URL + "/data", "auth": oauthTestConfig(server.URL + "/token"),
		"retries": map[string]any{"type": retryTypeStatic, "max_attempts": int64(2), "interval": "1ms"},
	})
	if err != nil {
		t.Fatalf("NewCollector() error: %v", err)
	}

	if !coll.Start(t.Context()) {
		t.Fatal("Collector.Start() = false, want true")
	}
	select {
	case <-coll.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Collector.Done() isn't closed after its one-time execution")
	}

	// The initial flow, and then one failed refresh per attempt.
	if got, want := int(tokenRequests.Load()), 1+coll.retries.MaxAttempts; got != want {
		t.Errorf("token endpoint got %d requests, want %d", got, want)
	}
	if got := dataRequests.Load(); got != 0 {
		t.Errorf("data endpoint got %d requests, want 0", got)
	}
}

// A failed token refresh must be retried like a failed request, without dereferencing a nil response,
// and eventually pass the data to the fallback sender (the DLQ) instead of crashing.
func TestSenderTokenRefreshFailure(t *testing.T) {
	t.Parallel()

	server, tokenRequests, dataRequests := newRefreshFailingServer(t)
	sender, err := NewSender(t.Context(), map[string]any{
		"method": http.MethodPut, "url": server.URL + "/data", "auth": oauthTestConfig(server.URL + "/token"),
		"retries": map[string]any{"type": retryTypeStatic, "max_attempts": int64(2), "interval": "1ms"},
	}, t.Name(), config.SenderTypeHTTP, nil)
	if err != nil {
		t.Fatalf("NewSender() error: %v", err)
	}
	fallback, fallbackSent := recordSends(t)
	sender.fallback = fallback

	sender.Send(t.Context(), flow.Blobs{[]byte("payload")}, codec.FormatDefault)
	sender.state.InProgress.Wait()

	// The initial flow, and then one failed refresh per attempt.
	if got, want := int(tokenRequests.Load()), 1+sender.retries.MaxAttempts; got != want {
		t.Errorf("token endpoint got %d requests, want %d", got, want)
	}
	if got := dataRequests.Load(); got != 0 {
		t.Errorf("data endpoint got %d requests, want 0", got)
	}
	if got, want := fallbackSent(), []string{"blobs: payload (raw)"}; !slices.Equal(got, want) {
		t.Errorf("sent to fallback = %q, want %q", got, want)
	}
}

func TestAuthErrorResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantRetry  bool
	}{
		{"canceled", fmt.Errorf("wrapped: %w", context.Canceled), http.StatusServiceUnavailable, false},
		{"deadline_exceeded", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), http.StatusGatewayTimeout, true},
		{"identity_provider_failure", errors.New("token endpoint unavailable"), http.StatusBadGateway, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp, retry := authErrorResponse(tt.err)
			t.Cleanup(func() { _ = resp.Body.Close() })

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("authErrorResponse() StatusCode = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if retry != tt.wantRetry {
				t.Errorf("authErrorResponse() retry = %v, want %v", retry, tt.wantRetry)
			}
		})
	}
}

func testContext(t *testing.T, canceled bool) context.Context {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	if canceled {
		cancel()
	}
	return ctx
}

func TestRequestOnceTokenFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		canceled   bool
		wantStatus int
		wantRetry  bool
	}{
		{"identity_provider_failure", false, http.StatusBadGateway, true},
		{"canceled_context", true, http.StatusServiceUnavailable, false},
	}
	for _, tt := range tests {
		t.Run("collector_"+tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, dataRequests := newRefreshFailingServer(t)
			base := &config.BaseCollector{Type: config.CollectorTypeHTTP, Name: t.Name()}
			c, err := NewCollector(base, map[string]any{
				"method": http.MethodGet, "url": server.URL + "/data", "auth": oauthTestConfig(server.URL + "/token"),
			})
			if err != nil {
				t.Fatalf("NewCollector() error: %v", err)
			}
			c.client = clientH2(&tls.Config{}, 0, time.Second, t.Name())
			flowCtx := context.WithValue(t.Context(), oauth2.HTTPClient, oauthClient(tls.VersionTLS12, time.Second))
			if err := c.auth.DoInitialOAuthFlow(flowCtx); err != nil {
				t.Fatalf("DoInitialOAuthFlow() error: %v", err)
			}

			resp, retry := c.requestOnce(testContext(t, tt.canceled))
			t.Cleanup(func() { _ = resp.Body.Close() })

			if resp.StatusCode != tt.wantStatus || retry != tt.wantRetry {
				t.Errorf("Collector.requestOnce() = (%d, %v), want (%d, %v)", resp.StatusCode, retry, tt.wantStatus, tt.wantRetry)
			}
			if got := dataRequests.Load(); got != 0 {
				t.Errorf("data endpoint got %d requests, want 0", got)
			}
		})

		t.Run("sender_"+tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, dataRequests := newRefreshFailingServer(t)
			sender, err := NewSender(t.Context(), map[string]any{
				"method": http.MethodPut, "url": server.URL + "/data", "auth": oauthTestConfig(server.URL + "/token"),
			}, t.Name(), config.SenderTypeHTTP, nil)
			if err != nil {
				t.Fatalf("NewSender() error: %v", err)
			}

			var bodyRequests atomic.Int32
			getBody := func() (io.ReadCloser, error) {
				bodyRequests.Add(1)
				return io.NopCloser(strings.NewReader("payload")), nil
			}
			resp, retry := sender.sendOnce(testContext(t, tt.canceled), sender.url, sender.headers, getBody, 7)
			t.Cleanup(func() { _ = resp.Body.Close() })

			if resp.StatusCode != tt.wantStatus || retry != tt.wantRetry {
				t.Errorf("Sender.sendOnce() = (%d, %v), want (%d, %v)", resp.StatusCode, retry, tt.wantStatus, tt.wantRetry)
			}
			if got := bodyRequests.Load(); got != 0 {
				t.Errorf("request body was opened %d times, want 0 (it would be left unclosed)", got)
			}
			if got := dataRequests.Load(); got != 0 {
				t.Errorf("data endpoint got %d requests, want 0", got)
			}
		})
	}
}

func TestOAuthClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		timeout     time.Duration
		wantTimeout time.Duration
	}{
		{"zero_timeout", 0, CloseTimeout},
		{"short_timeout", time.Second, time.Second},
		{"long_timeout", time.Minute, CloseTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := oauthClient(tls.VersionTLS12, tt.timeout)
			if c.Timeout != tt.wantTimeout {
				t.Errorf("oauthClient().Timeout = %v, want %v", c.Timeout, tt.wantTimeout)
			}

			tr, ok := c.Transport.(*http.Transport)
			if !ok {
				t.Fatalf("oauthClient().Transport is a %T, want *http.Transport", c.Transport)
			}
			if !tr.Protocols.HTTP1() || !tr.Protocols.HTTP2() {
				t.Errorf("oauthClient() protocols = %v, want HTTP/1.1 and HTTP/2", tr.Protocols)
			}
			cfg := tr.TLSClientConfig
			if cfg.MinVersion != tls.VersionTLS12 {
				t.Errorf("oauthClient() TLS MinVersion = %#x, want %#x", cfg.MinVersion, tls.VersionTLS12)
			}
			if cfg.InsecureSkipVerify || cfg.RootCAs != nil || len(cfg.Certificates) > 0 {
				t.Errorf("oauthClient() TLS config = %+v, want certificate verification with system CAs only", cfg)
			}
		})
	}
}

// Token requests must not use the component's own client, which is configured for its own server.
func TestOAuthTokenRequestsIgnoreComponentTLS(t *testing.T) {
	t.Parallel()

	tokenHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"t0k3n","token_type":"Bearer","expires_in":3600}`))
	})
	tlsServer := httptest.NewTLSServer(tokenHandler) // Self-signed certificate.
	t.Cleanup(tlsServer.Close)
	plainServer := httptest.NewServer(tokenHandler)
	t.Cleanup(plainServer.Close)

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsServer.Certificate().Raw})
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}

	tests := []struct {
		name       string
		senderType string
		tls        map[string]any
		tokenURL   string
		wantErr    bool
	}{
		{
			name:       "dont_verify_doesnt_apply",
			senderType: config.SenderTypeHTTP,
			tls:        map[string]any{"dont_verify": true},
			tokenURL:   tlsServer.URL + "/token",
			wantErr:    true, // Unverified certificate.
		},
		{
			name:       "server_ca_doesnt_apply",
			senderType: config.SenderTypeHTTP,
			tls:        map[string]any{"server_ca_cert_file": caFile},
			tokenURL:   tlsServer.URL + "/token",
			wantErr:    true, // The component's CA isn't trusted for token requests (and doesn't replace system CAs).
		},
		{
			name:       "http3_sender_uses_tcp",
			senderType: config.SenderTypeHTTP3,
			tokenURL:   plainServer.URL + "/token",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewSender(t.Context(), map[string]any{
				"method": http.MethodPut, "url": "https://example.com/data", "tls": tt.tls,
				"auth": oauthTestConfig(tt.tokenURL),
			}, tt.name, tt.senderType, nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewSender() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// newRedirectedRequest returns a request to the given URL, and the requests that redirected to it (via).
func newRedirectedRequest(t *testing.T, method, rawURL string, via []string) (*http.Request, []*http.Request) {
	t.Helper()

	newRequest := func(u string) *http.Request {
		req, err := http.NewRequestWithContext(t.Context(), method, u, http.NoBody)
		if err != nil {
			t.Fatalf("http.NewRequestWithContext(%q) error: %v", u, err)
		}
		return req
	}
	var reqs []*http.Request
	for _, u := range via {
		reqs = append(reqs, newRequest(u))
	}
	return newRequest(rawURL), reqs
}

func TestCheckRedirect(t *testing.T) {
	t.Parallel()

	if checkRedirect("") != nil {
		t.Error("checkRedirect(\"\") != nil, want nil (the default behavior)")
	}

	tests := []struct {
		name     string
		via      []string
		url      string
		wantAuth bool
		wantErr  bool
	}{
		{name: "same_host", via: []string{"https://a.com/x"}, url: "https://a.com/y", wantAuth: true},
		{name: "same_host_different_case", via: []string{"https://a.com/x"}, url: "https://A.COM/y", wantAuth: true},
		{name: "other_host", via: []string{"https://a.com/x"}, url: "https://b.com/y"},
		{name: "other_port", via: []string{"https://a.com:8443/x"}, url: "https://a.com:9443/y", wantAuth: true},
		{name: "subdomain", via: []string{"https://a.com/x"}, url: "https://sub.a.com/y", wantAuth: true},
		{name: "subdomain_with_port", via: []string{"https://a.com/x"}, url: "https://SUB.a.com:8443/y", wantAuth: true},
		{name: "parent_domain", via: []string{"https://sub.a.com/x"}, url: "https://a.com/y"},
		{name: "same_suffix", via: []string{"https://a.com/x"}, url: "https://evila.com/y"},
		{name: "ipv6_zone_suffix", via: []string{"http://www.a.com/x"}, url: "http://[fe80::1%25.www.a.com]/y"},
		{name: "back_to_origin", via: []string{"https://a.com/x", "https://b.com/y"}, url: "https://a.com/z"},
		{
			name:     "subdomain_and_back",
			via:      []string{"https://a.com/x", "https://sub.a.com/y"},
			url:      "https://a.com/z",
			wantAuth: true,
		},
		{name: "https_downgrade", via: []string{"https://a.com/x"}, url: "http://a.com/y"},
		{
			name: "https_downgrade_after_subdomain",
			via:  []string{"https://a.com/x", "https://sub.a.com/y"},
			url:  "http://sub.a.com/z",
		},
		{name: "http_upgrade", via: []string{"http://a.com/x"}, url: "https://a.com/y", wantAuth: true},
		{name: "http_upgrade_with_ports", via: []string{"http://a.com:8080/x"}, url: "https://A.com:8443/y", wantAuth: true},
		{name: "http_upgrade_to_other_host", via: []string{"http://a.com/x"}, url: "https://b.com/y"},
		{name: "http_upgrade_after_other_host", via: []string{"http://a.com/x", "http://b.com/y"}, url: "https://a.com/z"},
		{
			name:     "http_upgrade_and_back",
			via:      []string{"http://a.com/x", "https://a.com/y"},
			url:      "http://a.com/z",
			wantAuth: true, // The original request was already unencrypted.
		},
		{name: "same_non_ascii_host", via: []string{"https://bücher.de/x"}, url: "https://bücher.de/y", wantAuth: true},
		{name: "non_ascii_host_different_case", via: []string{"https://bücher.de/x"}, url: "https://BÜCHER.de/y"},
		{name: "non_ascii_lookalike", via: []string{"https://key.com/x"}, url: "https://\u212aey.com/y"}, // Kelvin sign.
		{
			name:    "too_many_redirects",
			via:     slices.Repeat([]string{"https://a.com/x"}, MaxRedirects),
			url:     "https://a.com/y",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req, via := newRedirectedRequest(t, http.MethodGet, tt.url, tt.via)
			req.Header.Set("X-Api-Key", "s3cr3t")

			err := checkRedirect("X-Api-Key")(req, via)
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkRedirect() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got := req.Header.Get("X-Api-Key") != ""; !tt.wantErr && got != tt.wantAuth {
				t.Errorf("auth header kept = %v, want %v", got, tt.wantAuth)
			}
		})
	}
}

// newRedirectingServer returns the URL of a server that redirects all requests to the "/final" path of another server
// with another hostname, or its own if sameHost is true. Each request to that path sends its "X-Api-Key" header to
// the channel.
func newRedirectingServer(t *testing.T, sameHost bool) (string, <-chan string) {
	t.Helper()

	received := make(chan string, MaxRedirects)
	final := func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("X-Api-Key")
		w.WriteHeader(http.StatusOK)
	}
	target := httptest.NewServer(http.HandlerFunc(final))
	t.Cleanup(target.Close)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			final(w, r)
			return
		}
		to := strings.Replace(target.URL, "127.0.0.1", "localhost", 1) + "/final" // Ports don't count.
		if sameHost {
			to = "/final" // Relative to the origin.
		}
		http.Redirect(w, r, to, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(origin.Close)

	return origin.URL, received
}

// Custom auth headers aren't covered by the redirect policy of [http.Client], which removes only
// the "Authorization" header and cookies, so components must not send them to other hosts.
func TestRedirectsDropAuthHeaderOfOtherHosts(t *testing.T) {
	t.Parallel()

	staticAuth := map[string]any{"type": "static", "static_header": "x-api-key", "static_prefix": "", "static_secret": "s3cr3t"}
	tests := []struct {
		name      string
		collector bool
		sameHost  bool
		want      string
	}{
		{name: "collector_same_host", collector: true, sameHost: true, want: "s3cr3t"},
		{name: "collector_other_host", collector: true, want: ""},
		{name: "sender_same_host", sameHost: true, want: "s3cr3t"},
		{name: "sender_other_host", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			serverURL, received := newRedirectingServer(t, tt.sameHost)
			if tt.collector {
				base, err := config.NewBaseCollector(
					map[string]any{"type": config.CollectorTypeHTTP, "schedule": "@once", "destination": "test"},
					t.Name(), map[string]config.Sender{"": nil, "test": dest.Discard},
				)
				if err != nil {
					t.Fatalf("config.NewBaseCollector() error: %v", err)
				}
				coll, err := NewCollector(base, map[string]any{"method": http.MethodGet, "url": serverURL, "auth": staticAuth})
				if err != nil {
					t.Fatalf("NewCollector() error: %v", err)
				}
				if !coll.Start(t.Context()) {
					t.Fatal("Collector.Start() = false, want true")
				}
				<-coll.Done()
			} else {
				sender, err := NewSender(
					t.Context(),
					map[string]any{"method": http.MethodPut, "url": serverURL, "auth": staticAuth},
					t.Name(),
					config.SenderTypeHTTP,
					nil,
				)
				if err != nil {
					t.Fatalf("NewSender() error: %v", err)
				}
				sender.Send(t.Context(), flow.Blobs{[]byte("payload")}, codec.FormatDefault)
				sender.state.InProgress.Wait()
			}

			if got := len(received); got != 1 {
				t.Fatalf("redirect target got %d requests, want 1", got)
			}
			if got := <-received; got != tt.want {
				t.Errorf("redirect target got auth header %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOAuthCheckRedirect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		via     []string
		url     string
		wantErr bool
	}{
		{name: "same_origin_307", status: 307, via: []string{"https://a.com/token"}, url: "https://a.com/v2/token"},
		{name: "same_origin_308", status: 308, via: []string{"https://a.com/token"}, url: "https://a.com/v2/token"},
		{name: "same_origin_different_case", status: 307, via: []string{"https://a.com/token"}, url: "HTTPS://A.COM/v2"},
		{name: "same_origin_301", status: 301, via: []string{"https://a.com/token"}, url: "https://a.com/v2", wantErr: true},
		{name: "same_origin_302", status: 302, via: []string{"https://a.com/token"}, url: "https://a.com/v2", wantErr: true},
		{name: "same_origin_303", status: 303, via: []string{"https://a.com/token"}, url: "https://a.com/v2", wantErr: true},
		{name: "other_host", status: 307, via: []string{"https://a.com/token"}, url: "https://b.com/token", wantErr: true},
		{name: "other_port", status: 307, via: []string{"https://a.com/token"}, url: "https://a.com:8443/token", wantErr: true},
		{name: "subdomain", status: 307, via: []string{"https://a.com/token"}, url: "https://sub.a.com/token", wantErr: true},
		{name: "https_downgrade", status: 308, via: []string{"https://a.com/token"}, url: "http://a.com/token", wantErr: true},
		{name: "http_upgrade", status: 307, via: []string{"http://a.com/token"}, url: "https://a.com/token"},
		{
			name:   "http_same_origin_307",
			status: 307,
			via:    []string{"http://127.0.0.1:8080/token"},
			url:    "http://127.0.0.1:8080/v2",
		},
		{
			name:    "http_other_port",
			status:  307,
			via:     []string{"http://127.0.0.1:8080/token"},
			url:     "http://127.0.0.1:9090/v2",
			wantErr: true,
		},
		{
			name:   "non_ascii_same_host",
			status: 307,
			via:    []string{"https://bücher.de/token"},
			url:    "https://bücher.de/v2",
		},
		{
			name:    "non_ascii_case_difference",
			status:  307,
			via:     []string{"https://bücher.de/token"},
			url:     "https://BÜCHER.de/v2", // Not folded: only ASCII hosts are compared case-insensitively.
			wantErr: true,
		},
		{
			name:    "non_ascii_lookalike",
			status:  307,
			via:     []string{"https://key.com/token"},
			url:     "https://\u212aey.com/token", // Kelvin sign.
			wantErr: true,
		},
		{
			name:   "multiple_same_origin_hops",
			status: 307,
			via:    []string{"https://a.com/token", "https://a.com/v2"},
			url:    "https://a.com/v3",
		},
		{
			name:    "too_many_redirects",
			status:  307,
			via:     slices.Repeat([]string{"https://a.com/token"}, MaxRedirects),
			url:     "https://a.com/token",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req, via := newRedirectedRequest(t, http.MethodPost, tt.url, tt.via)
			req.Response = &http.Response{StatusCode: tt.status}
			if tt.status != http.StatusTemporaryRedirect && tt.status != http.StatusPermanentRedirect {
				req.Method = http.MethodGet // Like [http.Client] does, before calling CheckRedirect.
			}

			if err := oauthCheckRedirect(req, via); (err != nil) != tt.wantErr {
				t.Errorf("oauthCheckRedirect() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// Token requests contain the client secret, so they may follow only 307 and 308 redirects to the same origin.
func TestOAuthTokenRedirects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		otherOrigin bool
		wantErr     bool
	}{
		{name: "same_origin_307", status: http.StatusTemporaryRedirect},
		{name: "same_origin_308", status: http.StatusPermanentRedirect},
		{name: "same_origin_302", status: http.StatusFound, wantErr: true},
		{name: "other_origin_307", status: http.StatusTemporaryRedirect, otherOrigin: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var redirectedRequests atomic.Int32
			issueToken := func(w http.ResponseWriter, r *http.Request) {
				redirectedRequests.Add(1)
				if r.Method != http.MethodPost || r.ParseForm() != nil || r.PostForm.Get("grant_type") == "" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"t0k3n","token_type":"Bearer"}`))
			}
			other := httptest.NewServer(http.HandlerFunc(issueToken))
			t.Cleanup(other.Close)

			location := "/moved" // Relative to the origin.
			if tt.otherOrigin {
				location = other.URL + "/moved"
			}
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/moved" {
					issueToken(w, r)
					return
				}
				http.Redirect(w, r, location, tt.status)
			}))
			t.Cleanup(origin.Close)

			_, err := NewSender(t.Context(), map[string]any{
				"method": http.MethodPut, "url": origin.URL + "/data", "auth": oauthTestConfig(origin.URL + "/token"),
			}, t.Name(), config.SenderTypeHTTP, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewSender() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "refusing to redirect OAuth") {
				t.Errorf("NewSender() error = %v, want a redirect error", err)
			}
			if got := redirectedRequests.Load(); tt.wantErr && got != 0 {
				t.Errorf("redirect target got %d requests, want 0", got)
			}
		})
	}
}
