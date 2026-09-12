package http

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/daabr/versipellis/pkg/config"
)

const (
	defaultTLSMinVersion = "1.3"
	defaultTLSDontVerify = false
)

// loadClientTLSConfig initializes TLS and mTLS configurations for HTTP and gRPC clients. It returns
// nil in cases of invalid parameters or runtime errors. It also returns a checksum which is based on
// the configurable parameters, which is used by clients as caching keys for reusable [http.Transport]s.
// This enables connection pooling, while preventing clients from using different configurations.
func loadClientTLSConfig(rawCfg any, httpVer string) (*tls.Config, string, error) {
	if rawCfg == nil {
		rawCfg = map[string]any{}
	}
	cfg, ok := rawCfg.(map[string]any)
	if !ok {
		return nil, "", fmt.Errorf("%q must be a table of key-value pairs, got %T", "tls", rawCfg)
	}
	for _, serverKey := range []string{"server_cert_file", "server_key_file", "mtls_client_ca_cert_file"} {
		if _, found := cfg[serverKey]; found {
			return nil, "", fmt.Errorf("server-side TLS setting %q does not belong in this config section", serverKey)
		}
	}

	t := &tls.Config{}
	var err error
	hash := sha256.New()

	if t.MinVersion, err = parseTLSMinVersion(config.Value(cfg, "min_version", defaultTLSMinVersion), httpVer); err != nil {
		return nil, "", err
	}
	t.InsecureSkipVerify = parseTLSDontVerifyForClients(cfg)
	_, _ = fmt.Fprintf(hash, "client\x00%d\x00%t\x00", t.MinVersion, t.InsecureSkipVerify)

	// Optional TLS server CA certificate.
	var raw []byte
	t.RootCAs, raw, err = loadCertPool(config.Value(cfg, "server_ca_cert_file", ""), "TLS server CA certificate")
	if err != nil {
		return nil, "", err
	}
	hash.Write(raw)
	hash.Write([]byte{0})

	// Optional mTLS client key pair.
	t.Certificates, err = loadKeyPair(cfg, "mtls_client_cert_file", "mtls_client_key_file", "mTLS client", false)
	if err != nil {
		return nil, "", err
	}
	if len(t.Certificates) > 0 {
		if httpVer == config.CollectorTypeHTTP3 {
			return nil, "", errors.New("HTTP/3 (QUIC) does not support mTLS")
		}
		for _, der := range t.Certificates[0].Certificate {
			hash.Write(der)
			hash.Write([]byte{0})
		}
	}

	return t, hex.EncodeToString(hash.Sum(nil)), nil
}

func loadServerTLSConfig(rawCfg any, httpVer string) (*tls.Config, error) {
	if rawCfg == nil {
		return nil, nil
	}
	cfg, ok := rawCfg.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q must be a table of key-value pairs, got %T", "tls", rawCfg)
	}
	for _, clientKey := range []string{"server_ca_cert_file", "mtls_client_cert_file", "mtls_client_key_file"} {
		if _, found := cfg[clientKey]; found {
			return nil, fmt.Errorf("client-side TLS setting %q does not belong in this config section", clientKey)
		}
	}

	t := &tls.Config{}
	var err error

	if t.MinVersion, err = parseTLSMinVersion(config.Value(cfg, "min_version", defaultTLSMinVersion), httpVer); err != nil {
		return nil, err
	}

	// Required TLS server certificate and private key.
	t.Certificates, err = loadKeyPair(cfg, "server_cert_file", "server_key_file", "TLS server", true)
	if err != nil {
		return nil, err
	}

	// Optional mTLS client CA certificate.
	var raw []byte
	t.ClientCAs, raw, err = loadCertPool(config.Value(cfg, "mtls_client_ca_cert_file", ""), "mTLS client CA certificate")
	if err != nil {
		return nil, err
	}

	if raw != nil && httpVer == config.CollectorTypeHTTP3 {
		return nil, errors.New("HTTP/3 (QUIC) does not support mTLS")
	}

	t.ClientAuth = parseTLSDontVerifyForServers(cfg, t)

	return t, nil
}

func parseTLSMinVersion(tlsVer, httpVer string) (uint16, error) {
	tlsVer = strings.TrimSpace(tlsVer)
	switch {
	case tlsVer == "1.3" || tlsVer == "":
		return tls.VersionTLS13, nil
	case tlsVer == "1.2" && httpVer != config.CollectorTypeHTTP3:
		slog.Warn("TLS 1.2 is enabled, which is less secure than TLS 1.3")
		return tls.VersionTLS12, nil
	case tlsVer == "1.2" && httpVer == config.CollectorTypeHTTP3:
		return 0, errors.New("HTTP/3 (QUIC) does not support TLS 1.2")
	case tlsVer == "1.0" || tlsVer == "1.1":
		return 0, fmt.Errorf("TLS version %s is deprecated", tlsVer)
	default:
		return 0, errors.New("invalid TLS version: " + tlsVer)
	}
}

func parseTLSDontVerifyForClients(cfg map[string]any) bool {
	dontVerify := config.Value(cfg, "dont_verify", defaultTLSDontVerify)
	if dontVerify {
		slog.Warn("TLS certificate verification is disabled")
	}
	return dontVerify
}

func parseTLSDontVerifyForServers(cfg map[string]any, t *tls.Config) tls.ClientAuthType {
	if t.ClientCAs == nil {
		return tls.NoClientCert // No mTLS config, so client certificates are not required anyway.
	}
	if config.Value(cfg, "dont_verify", defaultTLSDontVerify) { // Use mTLS, but insecurely.
		slog.Warn("mTLS certificate verification is disabled")
		return tls.RequireAnyClientCert
	}
	return tls.RequireAndVerifyClientCert // Regular, secure mTLS.
}

func loadPEM(path, description string, required bool) ([]byte, error) {
	path = strings.TrimSpace(path)
	if required && path == "" {
		return nil, errors.New(description + " file is required but not provided")
	}
	if path == "" {
		return nil, nil
	}

	data, err := os.ReadFile(path) //gosec:disable G304 // Path is configurable by design.
	if err != nil {
		return nil, fmt.Errorf("failed to read %s file: %w", description, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("%s file is empty: %s", description, path)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to parse PEM block for %s from: %s", description, path)
	}

	return data, nil
}

func loadCertPool(path, description string) (*x509.CertPool, []byte, error) {
	cert, err := loadPEM(path, description, false)
	if err != nil {
		return nil, nil, err
	}

	// If [tls.Config.RootCAs] is nil, the client uses [x509.SystemCertPool].
	// If [tls.Config.ClientCAs] is nil, the server will not support mTLS.
	if cert == nil {
		return nil, nil, nil
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(cert) {
		return nil, nil, fmt.Errorf("failed to parse %s from: %s", description, strings.TrimSpace(path))
	}
	return pool, cert, nil
}

func loadKeyPair(cfg map[string]any, certField, keyField, description string, required bool) ([]tls.Certificate, error) {
	cert, err := loadPEM(config.Value(cfg, certField, ""), description+" certificate", required)
	if err != nil {
		return nil, err
	}

	key, err := loadPEM(config.Value(cfg, keyField, ""), description+" private key", required)
	if err != nil {
		return nil, err
	}

	switch {
	case cert == nil && key == nil:
		return nil, nil
	case cert != nil && key == nil:
		return nil, errors.New(description + " certificate provided without corresponding private key")
	case cert == nil && key != nil:
		return nil, errors.New(description + " private key provided without corresponding certificate")
	}

	c, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, fmt.Errorf("parsing error in %s key pair: %w", description, err)
	}

	return []tls.Certificate{c}, nil
}
