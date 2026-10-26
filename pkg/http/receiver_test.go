package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/dest"
)

func TestNewReceiver(t *testing.T) { //nolint:paralleltest // [parseAddress] requires network access.
	tempDir := t.TempDir()
	caPEM, _, _, caKey := generateTestCert(t, true, nil, nil)
	caPath := filepath.Join(tempDir, "ca.pem")
	writeTestFile(t, caPath, caPEM)
	keyPath := filepath.Join(tempDir, "key.pem")
	writePrivateKeyFile(t, keyPath, caKey)

	tests := []struct {
		name    string
		base    *config.BaseReceiver
		cfg     map[string]any
		wantErr bool
	}{
		{
			name:    "missing_address",
			base:    &config.BaseReceiver{Type: config.ReceiverTypeHTTP},
			cfg:     map[string]any{"address": ""},
			wantErr: true,
		},
		{
			name:    "invalid_timeout",
			base:    &config.BaseReceiver{Type: config.ReceiverTypeHTTP},
			cfg:     map[string]any{"address": ":https", "timeout": "not-a-duration"},
			wantErr: true,
		},
		{
			name:    "tls_not_a_table",
			base:    &config.BaseReceiver{Type: config.ReceiverTypeHTTP},
			cfg:     map[string]any{"address": ":https", "tls": "invalid"},
			wantErr: true,
		},
		{
			name:    "invalid_format",
			base:    &config.BaseReceiver{Type: config.ReceiverTypeHTTP},
			cfg:     map[string]any{"address": ":https", "format": "invalid"},
			wantErr: true,
		},
		{
			name:    "http3_requires_tls",
			base:    &config.BaseReceiver{Type: config.ReceiverTypeHTTP3},
			cfg:     map[string]any{"address": ":https"},
			wantErr: true,
		},
		{
			name: "valid_config",
			base: &config.BaseReceiver{Type: config.ReceiverTypeHTTP3},
			cfg: map[string]any{
				"address": ":https",
				"tls": map[string]any{
					"server_cert_file": caPath,
					"server_key_file":  keyPath,
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests { //nolint:paralleltest // [parseAddress] requires network access.
		t.Run(tt.name, func(t *testing.T) {
			r, gotErr := NewReceiver(tt.base, tt.cfg)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("NewReceiver() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			if b := r.Base(); !reflect.DeepEqual(b, tt.base) {
				t.Errorf("Receiver.Base() = %+v, want %+v", b, tt.base)
			}
		})
	}
}

func TestParseAddress(t *testing.T) { //nolint:paralleltest // [parseAddress] requires network access.
	tests := []struct {
		name    string
		addr    string
		proto   string
		want    string
		wantErr bool
	}{
		{
			name:    "empty_address",
			addr:    "",
			proto:   config.ReceiverTypeHTTP,
			wantErr: true,
		},
		{
			name:    "invalid_tcp_address",
			addr:    ":invalid",
			proto:   config.ReceiverTypeHTTP,
			wantErr: true,
		},
		{
			name:    "invalid_udp_address",
			addr:    ":invalid",
			proto:   config.ReceiverTypeHTTP3,
			wantErr: true,
		},
		{
			name:    "invalid_proto",
			addr:    ":https",
			proto:   "invalid",
			wantErr: true,
		},
		{
			name:  "basic_https",
			addr:  ":https",
			proto: config.ReceiverTypeHTTP,
			want:  ":443",
		},
	}
	for _, tt := range tests { //nolint:paralleltest // [parseAddress] requires network access.
		t.Run(tt.name, func(t *testing.T) {
			got, gotErr := parseAddress(tt.addr, tt.proto)
			if (gotErr != nil) != tt.wantErr {
				t.Errorf("parseAddress() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseAddress() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReceiverServeHTTPAndClose(t *testing.T) { //nolint:paralleltest // [parseAddress] requires network access.
	tests := []struct {
		name       string
		length     int64
		body       io.ReadCloser
		wantStatus int
	}{
		{
			name:       "content_length_too_large",
			length:     11,
			body:       io.NopCloser(strings.NewReader("12345678901")),
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name:       "content_length_inaccurate",
			length:     10,
			body:       io.NopCloser(strings.NewReader("123456789")),
			wantStatus: http.StatusAccepted,
		},
	}
	for _, tt := range tests { //nolint:paralleltest // [parseAddress] requires network access.
		t.Run(tt.name, func(t *testing.T) {
			base := &config.BaseReceiver{Type: config.ReceiverTypeHTTP, Name: tt.name, Send: dest.Discard.Send}
			cfg := map[string]any{"address": "127.0.0.1:0", "max_body_size": int64(10), "timeout": "-1s"}
			rcv, err := NewReceiver(base, cfg)
			if err != nil {
				t.Fatalf("NewReceiver() error = %v", err)
			}

			w := &httptest.ResponseRecorder{}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", tt.body)
			req.ContentLength = tt.length
			rcv.ServeHTTP(w, req)

			if w.Result().StatusCode != tt.wantStatus {
				t.Errorf("ServeHTTP() status = %v, want %v", w.Result().StatusCode, tt.wantStatus)
			}

			if !rcv.Start(t.Context()) {
				t.Errorf("Receiver failed to start")
			}

			rcv.Close(t.Context())
		})
	}
}

func TestReceiverServeHTTPWhileClosing(t *testing.T) { //nolint:paralleltest // [parseAddress] requires network access.
	base := &config.BaseReceiver{Type: config.ReceiverTypeHTTP, Name: "TestReceiverServeHTTPWhileClosing"}
	rcv, err := NewReceiver(base, map[string]any{"address": "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewReceiver() error = %v", err)
	}
	rcv.state.Start() // Without listening: this test calls [Receiver.ServeHTTP] directly.

	serve := func() int {
		w := httptest.NewRecorder()
		body := slowReader{strings.NewReader("x")}
		rcv.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", body))
		return w.Code
	}

	// Requests during [Receiver.Close] must be either served or rejected, and registering them must not race with
	// waiting for them (the race detector flags [sync.WaitGroup.Add] calls that race with [sync.WaitGroup.Wait]).
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if code := serve(); code != http.StatusAccepted && code != http.StatusServiceUnavailable {
					t.Errorf("ServeHTTP() during Close status = %d, want %d or %d",
						code, http.StatusAccepted, http.StatusServiceUnavailable)
				}
			}
		})
	}

	// Ensure that requests are in progress when Close begins. Not using atomics or channels to wait for them,
	// because synchronizing with the requests would hide races from the race detector.
	time.Sleep(20 * time.Millisecond)
	rcv.Close(t.Context())
	close(stop)
	wg.Wait()

	if code := serve(); code != http.StatusServiceUnavailable {
		t.Errorf("ServeHTTP() after Close status = %d, want %d", code, http.StatusServiceUnavailable)
	}
}

// slowReader delays every read, so requests that read it are still in progress when [Receiver.Close] begins
// waiting for them (the race detector checks [sync.WaitGroup] misuse only when [sync.WaitGroup.Wait] blocks).
type slowReader struct {
	io.Reader
}

func (r slowReader) Read(p []byte) (int, error) {
	time.Sleep(time.Millisecond)
	return r.Reader.Read(p) //nolint:wrapcheck // Unit tests don't need error wrapping.
}

func TestReceiverServeHTTPFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantSent   []string
		wantDLQ    []string
	}{
		{
			name:       "valid_body",
			body:       `{"a":1}`,
			wantStatus: http.StatusAccepted,
			wantSent:   []string{`json: [{"a":1}] (json)`},
		},
		{
			name:       "unparsable_body",
			body:       "not JSON",
			wantStatus: http.StatusUnprocessableEntity,
			wantDLQ:    []string{"requests: not JSON (default)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			send, sent := recordSends(t)
			dlq, dlqSent := recordSends(t)
			base := config.BaseReceiver{Name: tt.name, Send: send, Fallback: dlq}
			rcv := &Receiver{BaseReceiver: base, maxBodySize: defaultMaxBodySize, format: "json"}

			w := httptest.NewRecorder()
			rcv.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(tt.body)))

			if got := w.Result().StatusCode; got != tt.wantStatus {
				t.Errorf("ServeHTTP() status = %v, want %v", got, tt.wantStatus)
			}
			if got := sent(); !reflect.DeepEqual(got, tt.wantSent) {
				t.Errorf("sent = %q, want %q", got, tt.wantSent)
			}
			if got := dlqSent(); !reflect.DeepEqual(got, tt.wantDLQ) {
				t.Errorf("sent to DLQ = %q, want %q", got, tt.wantDLQ)
			}
		})
	}
}

func TestReadRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		req        *http.Request
		body       io.ReadCloser
		contLen    int64
		maxBytes   int64
		want       []byte
		wantOK     bool
		wantStatus int
	}{
		{
			name:       "reported_content_length_too_large",
			req:        httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil),
			contLen:    10,
			maxBytes:   9,
			wantOK:     false,
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name:       "actual_body_length_too_large",
			req:        httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil),
			body:       io.NopCloser(strings.NewReader("1234567890")),
			maxBytes:   9,
			wantOK:     false,
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name:       "body_read_error",
			req:        httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil),
			body:       io.NopCloser(iotest.ErrReader(errors.New("read error"))),
			maxBytes:   9,
			wantOK:     false,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "success",
			req:        httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil),
			body:       io.NopCloser(strings.NewReader("123")),
			maxBytes:   9,
			want:       []byte("123"),
			wantOK:     true,
			wantStatus: http.StatusOK, // Not written: [Receiver.ServeHTTP] writes it after processing the body.
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := &httptest.ResponseRecorder{}
			tt.req.ContentLength = tt.contLen
			tt.req.Body = tt.body

			got, gotOK := readRequest(w, tt.req, tt.name, tt.maxBytes)
			if gotOK != tt.wantOK {
				t.Errorf("readRequest() ok = %v, want %v", gotOK, tt.wantOK)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("readRequest() = %q, want %q", got, tt.want)
			}
			if gotStatus := w.Result().StatusCode; gotStatus != tt.wantStatus {
				t.Errorf("readRequest() status = %v, want %v", gotStatus, tt.wantStatus)
			}
		})
	}
}

func TestReceiverClose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		receiver *Receiver
		wantOK   bool
	}{
		{
			name:     "adjust_timeout",
			receiver: &Receiver{},
			wantOK:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.receiver.Close(t.Context())
			// No panic = success.
		})
	}
}

func TestIsAnyError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		targets []error
		want    bool
	}{
		{
			name:    "nil_error",
			err:     nil,
			targets: []error{nil},
			want:    true,
		},
		{
			name:    "non_nil_error_matches_target",
			err:     http.ErrServerClosed,
			targets: []error{context.DeadlineExceeded, http.ErrServerClosed},
			want:    true,
		},
		{
			name:    "non_nil_error_does_not_match_target",
			err:     errors.New("some error"),
			targets: []error{errors.New("another error")},
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isAnyError(tt.err, tt.targets...); got != tt.want {
				t.Errorf("isAnyError() = %v, want %v", got, tt.want)
			}
		})
	}
}
