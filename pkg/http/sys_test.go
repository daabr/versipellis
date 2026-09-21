package http

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"github.com/daabr/versipellis/pkg/config"
	"github.com/daabr/versipellis/pkg/dest"
)

type testCase struct {
	name      string
	baseType  string
	tls       bool
	collector bool
	receiver  bool
}

func TestCollectorReceiverSender(t *testing.T) {
	t.Parallel()

	tests := []testCase{
		{
			name:      "http1_collector_without_tls",
			baseType:  "http",
			tls:       false,
			collector: true,
		},
		{
			name:      "http1_collector_with_tls",
			baseType:  "http",
			tls:       true,
			collector: true,
		},
		{
			name:     "http1_receiver_without_tls",
			baseType: "http",
			tls:      false,
			receiver: true,
		},
		{
			name:     "http1_receiver_with_tls",
			baseType: "http",
			tls:      true,
			receiver: true,
		},
		{
			name:      "http3_collector_with_tls",
			baseType:  "http3",
			tls:       true,
			collector: true,
		},
		{
			name:     "http3_receiver_with_tls",
			baseType: "http3",
			tls:      true,
			receiver: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			done := make(chan struct{})
			var serverSent bytes.Buffer        // Already isolated + single request, so no need.
			var serverReceived strings.Builder // To synchronize serverSent and serverReceived.
			serverURL, certPath, caPath, keyPath := startServers(t, tt, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				// Respond to collector.
				case http.MethodGet:
					w.WriteHeader(http.StatusOK)
					body := rand.Text()
					serverSent.WriteString(body)
					_, _ = w.Write([]byte(body))
					t.Logf("server sent to collector: %q", body)
				// Respond to sender.
				case http.MethodPost:
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("request body reading error: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(err.Error()))
					}
					serverReceived.Write(body)
					w.WriteHeader(http.StatusOK)
					t.Logf("server received from destination: %q", body)
					close(done)
				}
			})

			// Sender.
			cfg := map[string]any{"url": serverURL}
			if tt.tls {
				cfg["tls"] = map[string]any{"server_ca_cert_file": certPath}
			}
			dst, err := NewDestination(cfg, "TestCollectorReceiverSender.sender", tt.baseType)
			if err != nil {
				t.Fatalf("NewDestination() error: %v", err)
			}
			senders := map[string]config.Sender{"test": dst.Send}

			// Collector.
			if tt.collector {
				cfg = map[string]any{"type": tt.baseType, "schedule": "@once", "destination": "test"}
				base, err := config.NewBaseCollector(cfg, "TestCollectorReceiverSender.collector", senders)
				if err != nil {
					t.Fatalf("config.NewBaseCollector() error: %v", err)
				}

				cfg = map[string]any{"url": serverURL}
				if tt.tls {
					cfg["tls"] = map[string]any{"server_ca_cert_file": certPath}
				}
				coll, err := NewCollector(base, cfg)
				if err != nil {
					t.Fatalf("NewCollector() error: %v", err)
				}

				if !coll.Start(t.Context()) {
					t.Fatalf("Collector.Start() = false, want true")
				}

				<-coll.Done()
			}

			if tt.receiver {
				// Receiver.
				cfg = map[string]any{"type": tt.baseType, "destination": "test"}
				base, err := config.NewBaseReceiver(cfg, "TestCollectorReceiverSender.receiver", senders)
				if err != nil {
					t.Fatalf("config.NewBaseReceiver() error: %v", err)
				}

				cfg = map[string]any{"address": "127.0.0.1:0"}
				if tt.tls {
					cfg["tls"] = map[string]any{"server_cert_file": caPath, "server_key_file": keyPath}
				}
				rcv, err := NewReceiver(base, cfg)
				if err != nil {
					t.Fatalf("NewReceiver() error: %v", err)
				}

				if !rcv.Start(t.Context()) {
					t.Fatalf("Receiver.Start() = false, want true")
				}

				// Detached sender as a test client.
				cfg = map[string]any{"url": "http://" + rcv.address}
				if tt.tls {
					cfg["url"] = "https://" + rcv.address
					cfg["tls"] = map[string]any{"server_ca_cert_file": caPath}
				}
				dst, err = NewDestination(cfg, "TestCollectorReceiverSender.client", tt.baseType)
				if err != nil {
					t.Fatalf("NewDestination() error: %v", err)
				}

				body := rand.Text()
				l := int64(len(body))
				serverSent.WriteString(body)
				r := io.NopCloser(strings.NewReader(body))
				_, retry := dst.sendOnce(t.Context(), dst.url, dst.headers, r, l) //nolint:bodyclose // Closed in sendOnce.
				if retry {
					t.Fatalf("client failed to send to receiver")
				}
				t.Logf("client sent to receiver: %q", body)

				rcv.Close(t.Context())
			}

			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("timeout during wait for server to receive data")
			}

			if serverSent.String() != serverReceived.String() {
				t.Errorf("collected data = %q, sent data = %q", serverSent.String(), serverReceived.String())
			}
		})
	}
}

func startServers(t *testing.T, tt testCase, handler http.HandlerFunc) (serverURL, certPath, caPath, keyPath string) {
	t.Helper()

	// HTTP/1 test server.
	startFn := httptest.NewServer
	if tt.tls {
		startFn = httptest.NewTLSServer
	}
	srv1 := startFn(handler)
	t.Cleanup(srv1.Close)
	serverURL = srv1.URL

	if !tt.tls {
		return serverURL, "", "", ""
	}

	// Setup for TLS tests.
	tempDir := t.TempDir()
	if tt.baseType == "http" {
		certPath = filepath.Join(tempDir, "server_cert.pem")
		block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv1.Certificate().Raw})
		writeTestFile(t, certPath, block)
	}
	if tt.receiver || tt.baseType == "http3" {
		caPEM, _, _, caKey := generateTestCert(t, true, nil, nil)
		caPath = filepath.Join(tempDir, "ca.pem")
		writeTestFile(t, caPath, caPEM)
		keyPath = filepath.Join(tempDir, "key.pem")
		writePrivateKeyFile(t, keyPath, caKey)
	}

	if tt.baseType == "http" {
		return serverURL, certPath, caPath, keyPath
	}

	// HTTP/3 server.
	conn, err := new(net.ListenConfig).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.ListenConfig.ListenPacket() error: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	serverURL = "https://" + conn.LocalAddr().String()

	cfg := map[string]any{"server_cert_file": caPath, "server_key_file": keyPath}
	tlsCfg, err := loadServerTLSConfig(cfg, config.ReceiverTypeHTTP3)
	if err != nil {
		t.Fatalf("loadServerTLSConfig() error: %v", err)
	}

	srv3 := &http3.Server{Addr: conn.LocalAddr().String(), Handler: handler, TLSConfig: tlsCfg}
	go func() {
		_ = srv3.Serve(conn)
	}()
	t.Cleanup(func() { _ = srv3.Close() })

	return serverURL, caPath, caPath, keyPath
}

func writePrivateKeyFile(t *testing.T, path string, key any) {
	t.Helper()

	priv, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("key is not an *ecdsa.PrivateKey")
	}

	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("failed to marshal PKCS8 private key: %v", err)
	}

	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("failed to write private key file: %v", err)
	}
}

func TestHTTP1mTLSClientAndServer(t *testing.T) {
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

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientPool,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	caPath := filepath.Join(tempDir, "ca.pem")
	certPath := filepath.Join(tempDir, "client.pem")
	keyPath := filepath.Join(tempDir, "client.key")
	writeTestFile(t, caPath, caPEM)
	writeTestFile(t, certPath, clientPEM)
	writeTestFile(t, keyPath, clientKeyPEM)

	senders := map[string]config.Sender{"": dest.Discard}
	cfg := map[string]any{"type": config.CollectorTypeHTTP, "schedule": "@once"}
	base, err := config.NewBaseCollector(cfg, "TestHTTP1mTLSClientAndServer", senders)
	if err != nil {
		t.Fatalf("config.NewBaseCollector() error: %v", err)
	}

	coll, err := NewCollector(base, map[string]any{
		"method": http.MethodGet,
		"url":    srv.URL,
		"tls": map[string]any{
			"server_ca_cert_file":   caPath,
			"mtls_client_cert_file": certPath,
			"mtls_client_key_file":  keyPath,
		},
	})
	if err != nil {
		t.Fatalf("NewCollector() error: %v", err)
	}

	if !coll.Start(t.Context()) {
		t.Fatalf("Collector.Start() = false, want true")
	}

	<-coll.Done()

	resp, retry := coll.requestOnce(t.Context())
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK || retry {
		t.Fatalf("mTLS request failed with status %d, retry = %v", resp.StatusCode, retry)
	}
}
