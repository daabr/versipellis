package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/dest"
)

func TestNewReceiver(t *testing.T) {
	// Can't use [testing.T.Parallel] here because [parseAddress] requires network access.

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
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, gotErr := NewReceiver(tt.base, tt.cfg)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("NewReceiver() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			if b := r.Base(); !reflect.DeepEqual(b, tt.base) {
				t.Errorf("Receiver.Base() = %v, want %v", b, tt.base)
			}
		})
	}
}

func TestParseAddress(t *testing.T) {
	// Can't use [testing.T.Parallel] here because [parseAddress] requires network access.

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
	for _, tt := range tests {
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

func TestReceiverServeHTTPAndClose(t *testing.T) {
	// Can't use [testing.T.Parallel] here because [parseAddress] requires network access.

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
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := &config.BaseReceiver{Type: config.ReceiverTypeHTTP, Name: tt.name, Sender: dest.Discard}
			cfg := map[string]any{"address": "127.0.0.1:0", "max_body_size": int64(10)}
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
