package http

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daabr/versipellis/pkg/config"
)

func generateTestCert(t *testing.T, ca bool, parent *x509.Certificate, parentKey any) ([]byte, []byte, *x509.Certificate, any) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), nil)
	if err != nil {
		t.Fatalf("failed to generate private key: %v", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("failed to generate serial number: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{Organization: []string{"Versipellis Test"}, CommonName: "localhost"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},

		BasicConstraintsValid: true,
	}

	if ca {
		template.IsCA = true
		template.KeyUsage |= x509.KeyUsageCertSign
	}

	signingCert := template
	signingKey := any(priv)
	if parent != nil && parentKey != nil {
		signingCert = parent
		signingKey = parentKey
	}

	der, err := x509.CreateCertificate(rand.Reader, template, signingCert, &priv.PublicKey, signingKey)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	pemCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("failed to marshal private key: %v", err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	return pemCert, pemKey, template, priv
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("failed to write test file %s: %v", path, err)
	}
}

func TestLoadClientTLSConfig(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	caPEM, _, caCert, caKey := generateTestCert(t, true, nil, nil)
	caFile := filepath.Join(tempDir, "ca.pem")
	writeTestFile(t, caFile, caPEM)

	clientPEM, clientKeyPEM, _, _ := generateTestCert(t, false, caCert, caKey)
	clientCertFile := filepath.Join(tempDir, "client.pem")
	clientKeyFile := filepath.Join(tempDir, "client.key")
	writeTestFile(t, clientCertFile, clientPEM)
	writeTestFile(t, clientKeyFile, clientKeyPEM)

	_, otherKeyPEM, _, _ := generateTestCert(t, false, nil, nil)
	mismatchedKeyFile := filepath.Join(tempDir, "mismatched.key")
	writeTestFile(t, mismatchedKeyFile, otherKeyPEM)

	emptyFile := filepath.Join(tempDir, "empty.pem")
	writeTestFile(t, emptyFile, []byte(""))

	invalidPEMFile := filepath.Join(tempDir, "invalid.pem")
	writeTestFile(t, invalidPEMFile, []byte("NOT A PEM ENCODED CERTIFICATE"))

	tests := []struct {
		name               string
		rawCfg             any
		httpVer            string
		wantErr            bool
		wantDefaultHash    bool
		wantMinVersion     uint16
		wantDontVerify     bool
		wantHasClientCerts bool
	}{
		{
			name:            "nil_config_uses_defaults",
			rawCfg:          nil,
			httpVer:         config.CollectorTypeHTTP,
			wantMinVersion:  tls.VersionTLS13,
			wantDontVerify:  false,
			wantDefaultHash: false,
			wantErr:         false,
		},
		{
			name:            "empty_table_uses_defaults",
			rawCfg:          map[string]any{},
			httpVer:         config.CollectorTypeHTTP,
			wantMinVersion:  tls.VersionTLS13,
			wantDontVerify:  false,
			wantDefaultHash: false,
			wantErr:         false,
		},
		{
			name:    "not_a_table",
			rawCfg:  "not a table",
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "server_cert_file_not_allowed",
			rawCfg: map[string]any{
				"server_cert_file": clientCertFile,
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "server_key_file_not_allowed",
			rawCfg: map[string]any{
				"server_key_file": clientKeyFile,
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "mtls_client_ca_cert_file_not_allowed",
			rawCfg: map[string]any{
				"mtls_client_ca_cert_file": caFile,
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "valid_tls_12_and_dont_verify",
			rawCfg: map[string]any{
				"min_version": "1.2",
				"dont_verify": true,
			},
			httpVer:        config.CollectorTypeHTTP,
			wantMinVersion: tls.VersionTLS12,
			wantDontVerify: true,
		},
		{
			name: "invalid_min_version",
			rawCfg: map[string]any{
				"min_version": "1.1",
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "valid_ca_cert_file",
			rawCfg: map[string]any{
				"server_ca_cert_file": caFile,
			},
			httpVer:        config.CollectorTypeHTTP,
			wantMinVersion: tls.VersionTLS13,
		},
		{
			name: "ca_cert_file_not_found",
			rawCfg: map[string]any{
				"server_ca_cert_file": filepath.Join(tempDir, "nonexistent.pem"),
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "ca_cert_file_empty",
			rawCfg: map[string]any{
				"server_ca_cert_file": emptyFile,
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "valid_mtls_http1_and_2",
			rawCfg: map[string]any{
				"server_ca_cert_file":   caFile,
				"mtls_client_cert_file": clientCertFile,
				"mtls_client_key_file":  clientKeyFile,
			},
			httpVer:            config.CollectorTypeHTTP,
			wantMinVersion:     tls.VersionTLS13,
			wantHasClientCerts: true,
		},
		{
			name: "mtls_unsupported_in_http3",
			rawCfg: map[string]any{
				"mtls_client_cert_file": clientCertFile,
				"mtls_client_key_file":  clientKeyFile,
			},
			httpVer: config.CollectorTypeHTTP3,
			wantErr: true,
		},
		{
			name: "mtls_missing_key",
			rawCfg: map[string]any{
				"mtls_client_cert_file": clientCertFile,
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "mtls_missing_cert",
			rawCfg: map[string]any{
				"mtls_client_key_file": clientKeyFile,
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "mtls_cert_not_found",
			rawCfg: map[string]any{
				"mtls_client_cert_file": filepath.Join(tempDir, "nonexistent.pem"),
				"mtls_client_key_file":  clientKeyFile,
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "mtls_mismatched_key",
			rawCfg: map[string]any{
				"mtls_client_cert_file": clientCertFile,
				"mtls_client_key_file":  mismatchedKeyFile,
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, hash, gotErr := loadClientTLSConfig(tt.rawCfg, tt.httpVer)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("loadClientTLSConfig() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			if tt.wantDefaultHash && hash != "default" {
				t.Errorf("hash = %q, want %q", hash, "default")
			}
			if !tt.wantDefaultHash && len(hash) != 64 {
				t.Errorf("hash length = %d, want 64", len(hash))
			}
			if got.MinVersion != tt.wantMinVersion {
				t.Errorf("MinVersion = 0x%04x, want 0x%04x", got.MinVersion, tt.wantMinVersion)
			}
			if got.InsecureSkipVerify != tt.wantDontVerify {
				t.Errorf("InsecureSkipVerify = %v, want %v", got.InsecureSkipVerify, tt.wantDontVerify)
			}
			if (len(got.Certificates) > 0) != tt.wantHasClientCerts {
				t.Errorf("has Certificates = %v, want %v", len(got.Certificates) > 0, tt.wantHasClientCerts)
			}
		})
	}
}

func TestLoadServerTLSConfig(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	caPEM, _, caCert, caKey := generateTestCert(t, true, nil, nil)
	caFile := filepath.Join(tempDir, "ca.pem")
	writeTestFile(t, caFile, caPEM)

	serverPEM, serverKeyPEM, _, _ := generateTestCert(t, false, caCert, caKey)
	serverCertFile := filepath.Join(tempDir, "server.pem")
	serverKeyFile := filepath.Join(tempDir, "server.key")
	writeTestFile(t, serverCertFile, serverPEM)
	writeTestFile(t, serverKeyFile, serverKeyPEM)

	_, otherKeyPEM, _, _ := generateTestCert(t, false, nil, nil)
	mismatchedKeyFile := filepath.Join(tempDir, "mismatched.key")
	writeTestFile(t, mismatchedKeyFile, otherKeyPEM)

	tests := []struct {
		name            string
		untypedCfg      any
		httpVer         string
		wantErr         bool
		wantDefaultHash bool
		wantMinVersion  uint16
		wantClientAuth  tls.ClientAuthType
		wantHasCerts    bool
		wantHasCAs      bool
	}{
		{
			name:            "nil_config_returns_default",
			untypedCfg:      nil,
			httpVer:         config.CollectorTypeHTTP,
			wantDefaultHash: true,
			wantErr:         false,
		},
		{
			name:       "not_a_table",
			untypedCfg: 123,
			httpVer:    config.CollectorTypeHTTP,
			wantErr:    true,
		},
		{
			name: "client_setting_server_ca_not_allowed",
			untypedCfg: map[string]any{
				"server_ca_cert_file": caFile,
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "client_setting_mtls_cert_not_allowed",
			untypedCfg: map[string]any{
				"mtls_client_cert_file": serverCertFile,
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "client_setting_mtls_key_not_allowed",
			untypedCfg: map[string]any{
				"mtls_client_key_file": serverKeyFile,
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name:       "missing_server_cert_and_key",
			untypedCfg: map[string]any{},
			httpVer:    config.CollectorTypeHTTP,
			wantErr:    true,
		},
		{
			name: "missing_server_key",
			untypedCfg: map[string]any{
				"server_cert_file": serverCertFile,
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "missing_server_cert",
			untypedCfg: map[string]any{
				"server_key_file": serverKeyFile,
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "valid_server_tls_defaults",
			untypedCfg: map[string]any{
				"server_cert_file": serverCertFile,
				"server_key_file":  serverKeyFile,
			},
			httpVer:        config.CollectorTypeHTTP,
			wantMinVersion: tls.VersionTLS13,
			wantClientAuth: tls.NoClientCert,
			wantHasCerts:   true,
			wantHasCAs:     false,
		},
		{
			name: "valid_server_mtls",
			untypedCfg: map[string]any{
				"server_cert_file":         serverCertFile,
				"server_key_file":          serverKeyFile,
				"mtls_client_ca_cert_file": caFile,
				"min_version":              "1.2",
			},
			httpVer:        config.CollectorTypeHTTP,
			wantMinVersion: tls.VersionTLS12,
			wantClientAuth: tls.RequireAndVerifyClientCert,
			wantHasCerts:   true,
			wantHasCAs:     true,
		},
		{
			name: "valid_server_mtls_dont_verify",
			untypedCfg: map[string]any{
				"server_cert_file":         serverCertFile,
				"server_key_file":          serverKeyFile,
				"mtls_client_ca_cert_file": caFile,
				"dont_verify":              true,
			},
			httpVer:        config.CollectorTypeHTTP,
			wantMinVersion: tls.VersionTLS13,
			wantClientAuth: tls.RequireAnyClientCert,
			wantHasCerts:   true,
			wantHasCAs:     true,
		},
		{
			name: "invalid_min_version",
			untypedCfg: map[string]any{
				"server_cert_file": serverCertFile,
				"server_key_file":  serverKeyFile,
				"min_version":      "1.0",
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "client_ca_cert_file_not_found",
			untypedCfg: map[string]any{
				"server_cert_file":         serverCertFile,
				"server_key_file":          serverKeyFile,
				"mtls_client_ca_cert_file": filepath.Join(tempDir, "nonexistent.pem"),
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "server_key_mismatch",
			untypedCfg: map[string]any{
				"server_cert_file": serverCertFile,
				"server_key_file":  mismatchedKeyFile,
			},
			httpVer: config.CollectorTypeHTTP,
			wantErr: true,
		},
		{
			name: "valid_server_tls_http3",
			untypedCfg: map[string]any{
				"server_cert_file": serverCertFile,
				"server_key_file":  serverKeyFile,
			},
			httpVer:        config.CollectorTypeHTTP3,
			wantMinVersion: tls.VersionTLS13,
			wantClientAuth: tls.NoClientCert,
			wantHasCerts:   true,
			wantHasCAs:     false,
		},
		{
			name: "mtls_unsupported_in_http3",
			untypedCfg: map[string]any{
				"server_cert_file":         serverCertFile,
				"server_key_file":          serverKeyFile,
				"mtls_client_ca_cert_file": caFile,
			},
			httpVer: config.CollectorTypeHTTP3,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, gotErr := loadServerTLSConfig(tt.untypedCfg, tt.httpVer)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("loadServerTLSConfig() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			if got == nil {
				if !tt.wantDefaultHash {
					t.Errorf("got unexpected nil tls.Config")
				}
				return
			}
			if got.MinVersion != tt.wantMinVersion {
				t.Errorf("MinVersion = 0x%04x, want 0x%04x", got.MinVersion, tt.wantMinVersion)
			}
			if got.ClientAuth != tt.wantClientAuth {
				t.Errorf("ClientAuth = %v, want %v", got.ClientAuth, tt.wantClientAuth)
			}
			if (len(got.Certificates) > 0) != tt.wantHasCerts {
				t.Errorf("has Certificates = %v, want %v", len(got.Certificates) > 0, tt.wantHasCerts)
			}
			if (got.ClientCAs != nil) != tt.wantHasCAs {
				t.Errorf("has ClientCAs = %v, want %v", got.ClientCAs != nil, tt.wantHasCAs)
			}
		})
	}
}

func TestParseTLSMinVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ver     string
		httpVer string
		want    uint16
		wantErr bool
	}{
		{
			name: "empty_returns_tls13",
			ver:  "",
			want: tls.VersionTLS13,
		},
		{
			name: "whitespace_returns_tls13",
			ver:  "   ",
			want: tls.VersionTLS13,
		},
		{
			name: "tls13",
			ver:  "1.3",
			want: tls.VersionTLS13,
		},
		{
			name: "tls13_with_spaces",
			ver:  " 1.3 ",
			want: tls.VersionTLS13,
		},
		{
			name: "tls12",
			ver:  "1.2",
			want: tls.VersionTLS12, // Warning log.
		},
		{
			name:    "tls12",
			ver:     "1.2",
			httpVer: config.CollectorTypeHTTP3,
			wantErr: true,
		},
		{
			name: "tls12_with_spaces",
			ver:  " 1.2 ",
			want: tls.VersionTLS12,
		},
		{
			name:    "tls10_deprecated",
			ver:     "1.0",
			wantErr: true,
		},
		{
			name:    "tls11_deprecated",
			ver:     "1.1",
			wantErr: true,
		},
		{
			name:    "invalid_version",
			ver:     "1.4",
			wantErr: true,
		},
		{
			name:    "unknown_string",
			ver:     "TLS1.3",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, gotErr := parseTLSMinVersion(tt.ver, tt.httpVer)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("parseTLSMinVersion() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseTLSMinVersion() = 0x%04x, want 0x%04x", got, tt.want)
			}
		})
	}
}

func TestParseTLSDontVerifyForClients(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  map[string]any
		want bool
	}{
		{
			name: "empty_cfg_returns_false",
			cfg:  map[string]any{},
			want: false,
		},
		{
			name: "dont_verify_false",
			cfg: map[string]any{
				"dont_verify": false,
			},
			want: false,
		},
		{
			name: "dont_verify_true",
			cfg: map[string]any{
				"dont_verify": true,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := parseTLSDontVerifyForClients(tt.cfg); got != tt.want {
				t.Errorf("parseTLSDontVerifyForClients() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseTLSDontVerifyForServers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		cfg          map[string]any
		hasClientCAs bool
		want         tls.ClientAuthType
	}{
		{
			name:         "no_client_cas_returns_no_client_cert",
			cfg:          map[string]any{},
			hasClientCAs: false,
			want:         tls.NoClientCert,
		},
		{
			name: "no_client_cas_with_dont_verify_returns_no_client_cert",
			cfg: map[string]any{
				"dont_verify": true,
			},
			hasClientCAs: false,
			want:         tls.NoClientCert,
		},
		{
			name:         "with_client_cas_defaults_to_require_and_verify",
			cfg:          map[string]any{},
			hasClientCAs: true,
			want:         tls.RequireAndVerifyClientCert,
		},
		{
			name: "with_client_cas_and_dont_verify_false",
			cfg: map[string]any{
				"dont_verify": false,
			},
			hasClientCAs: true,
			want:         tls.RequireAndVerifyClientCert,
		},
		{
			name: "with_client_cas_and_dont_verify_true",
			cfg: map[string]any{
				"dont_verify": true,
			},
			hasClientCAs: true,
			want:         tls.RequireAnyClientCert,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfgTLS := &tls.Config{}
			if tt.hasClientCAs {
				cfgTLS.ClientCAs = x509.NewCertPool()
			}
			if got := parseTLSDontVerifyForServers(tt.cfg, cfgTLS); got != tt.want {
				t.Errorf("parseTLSDontVerifyForServers() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadPEM(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	certPEM, _, _, _ := generateTestCert(t, false, nil, nil)
	validCertFile := filepath.Join(tempDir, "cert.pem")
	writeTestFile(t, validCertFile, certPEM)

	emptyFile := filepath.Join(tempDir, "empty.pem")
	writeTestFile(t, emptyFile, []byte(""))

	invalidFile := filepath.Join(tempDir, "invalid.pem")
	writeTestFile(t, invalidFile, []byte("NOT A PEM"))

	tests := []struct {
		name        string
		path        string
		description string
		required    bool
		wantNil     bool
		wantErr     bool
	}{
		{
			name:        "empty_path_optional",
			path:        "",
			description: "test cert",
			required:    false,
			wantNil:     true,
			wantErr:     false,
		},
		{
			name:        "whitespace_path_optional",
			path:        "   ",
			description: "test cert",
			required:    false,
			wantNil:     true,
			wantErr:     false,
		},
		{
			name:        "empty_path_required",
			path:        "",
			description: "test cert",
			required:    true,
			wantErr:     true,
		},
		{
			name:        "whitespace_path_required",
			path:        "   ",
			description: "test cert",
			required:    true,
			wantErr:     true,
		},
		{
			name:        "valid_pem_file",
			path:        validCertFile,
			description: "test cert",
			required:    true,
			wantNil:     false,
			wantErr:     false,
		},
		{
			name:        "nonexistent_file",
			path:        filepath.Join(tempDir, "nonexistent.pem"),
			description: "test cert",
			required:    false,
			wantErr:     true,
		},
		{
			name:        "empty_file",
			path:        emptyFile,
			description: "test cert",
			required:    false,
			wantErr:     true,
		},
		{
			name:        "invalid_pem_content",
			path:        invalidFile,
			description: "test cert",
			required:    false,
			wantErr:     true,
		},
		{
			name:        "directory_instead_of_file",
			path:        tempDir,
			description: "test cert",
			required:    false,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, gotErr := loadPEM(tt.path, tt.description, tt.required)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("loadPEM() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if !tt.wantErr && (data == nil) != tt.wantNil {
				t.Errorf("loadPEM() data is nil = %v, wantNil %v", data == nil, tt.wantNil)
			}
		})
	}
}

func TestLoadCertPool(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	caPEM, _, _, _ := generateTestCert(t, true, nil, nil)
	validCAFile := filepath.Join(tempDir, "ca.pem")
	writeTestFile(t, validCAFile, caPEM)

	emptyFile := filepath.Join(tempDir, "empty.pem")
	writeTestFile(t, emptyFile, []byte(""))

	invalidPEMFile := filepath.Join(tempDir, "invalid.pem")
	writeTestFile(t, invalidPEMFile, []byte("NOT PEM DATA"))

	tests := []struct {
		name        string
		path        string
		description string
		wantNilPool bool
		wantNilRaw  bool
		wantErr     bool
	}{
		{
			name:        "empty_path_returns_nil_pool",
			path:        "",
			description: "CA certificate",
			wantNilPool: true,
			wantNilRaw:  true,
			wantErr:     false,
		},
		{
			name:        "whitespace_path_returns_nil_pool",
			path:        "   ",
			description: "CA certificate",
			wantNilPool: true,
			wantNilRaw:  true,
			wantErr:     false,
		},
		{
			name:        "valid_ca",
			path:        validCAFile,
			description: "CA certificate",
			wantNilPool: false,
			wantNilRaw:  false,
			wantErr:     false,
		},
		{
			name:        "nonexistent_file",
			path:        filepath.Join(tempDir, "missing.pem"),
			description: "CA certificate",
			wantErr:     true,
		},
		{
			name:        "empty_file",
			path:        emptyFile,
			description: "CA certificate",
			wantErr:     true,
		},
		{
			name:        "invalid_pem",
			path:        invalidPEMFile,
			description: "CA certificate",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, raw, gotErr := loadCertPool(tt.path, tt.description)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("loadCertPool() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if !tt.wantErr {
				if (pool == nil) != tt.wantNilPool {
					t.Errorf("loadCertPool() pool is nil = %v, wantNilPool %v", pool == nil, tt.wantNilPool)
				}
				if (raw == nil) != tt.wantNilRaw {
					t.Errorf("loadCertPool() raw is nil = %v, wantNilRaw %v", raw == nil, tt.wantNilRaw)
				}
			}
		})
	}
}

func TestLoadKeyPair(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	certPEM, keyPEM, _, _ := generateTestCert(t, false, nil, nil)
	validCertFile := filepath.Join(tempDir, "valid.pem")
	validKeyFile := filepath.Join(tempDir, "valid.key")
	writeTestFile(t, validCertFile, certPEM)
	writeTestFile(t, validKeyFile, keyPEM)

	_, otherKeyPEM, _, _ := generateTestCert(t, false, nil, nil)
	mismatchedKeyFile := filepath.Join(tempDir, "mismatched.key")
	writeTestFile(t, mismatchedKeyFile, otherKeyPEM)

	emptyFile := filepath.Join(tempDir, "empty.pem")
	writeTestFile(t, emptyFile, []byte(""))

	invalidFile := filepath.Join(tempDir, "invalid.pem")
	writeTestFile(t, invalidFile, []byte("NOT PEM"))

	tests := []struct {
		name        string
		cfg         map[string]any
		certField   string
		keyField    string
		description string
		required    bool
		wantNil     bool
		wantErr     bool
	}{
		{
			name:        "both_empty_optional",
			cfg:         map[string]any{},
			certField:   "cert",
			keyField:    "key",
			description: "test",
			required:    false,
			wantNil:     true,
			wantErr:     false,
		},
		{
			name:        "both_empty_required",
			cfg:         map[string]any{},
			certField:   "cert",
			keyField:    "key",
			description: "test",
			required:    true,
			wantErr:     true,
		},
		{
			name: "cert_without_key",
			cfg: map[string]any{
				"cert": validCertFile,
			},
			certField:   "cert",
			keyField:    "key",
			description: "test",
			required:    false,
			wantErr:     true,
		},
		{
			name: "key_without_cert",
			cfg: map[string]any{
				"key": validKeyFile,
			},
			certField:   "cert",
			keyField:    "key",
			description: "test",
			required:    false,
			wantErr:     true,
		},
		{
			name: "valid_pair",
			cfg: map[string]any{
				"cert": validCertFile,
				"key":  validKeyFile,
			},
			certField:   "cert",
			keyField:    "key",
			description: "test",
			required:    false,
			wantNil:     false,
			wantErr:     false,
		},
		{
			name: "cert_not_found",
			cfg: map[string]any{
				"cert": filepath.Join(tempDir, "nonexistent.pem"),
				"key":  validKeyFile,
			},
			certField:   "cert",
			keyField:    "key",
			description: "test",
			required:    false,
			wantErr:     true,
		},
		{
			name: "key_not_found",
			cfg: map[string]any{
				"cert": validCertFile,
				"key":  filepath.Join(tempDir, "nonexistent.key"),
			},
			certField:   "cert",
			keyField:    "key",
			description: "test",
			required:    false,
			wantErr:     true,
		},
		{
			name: "cert_empty",
			cfg: map[string]any{
				"cert": emptyFile,
				"key":  validKeyFile,
			},
			certField:   "cert",
			keyField:    "key",
			description: "test",
			required:    false,
			wantErr:     true,
		},
		{
			name: "key_empty",
			cfg: map[string]any{
				"cert": validCertFile,
				"key":  emptyFile,
			},
			certField:   "cert",
			keyField:    "key",
			description: "test",
			required:    false,
			wantErr:     true,
		},
		{
			name: "mismatched_key",
			cfg: map[string]any{
				"cert": validCertFile,
				"key":  mismatchedKeyFile,
			},
			certField:   "cert",
			keyField:    "key",
			description: "test",
			required:    false,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			certs, gotErr := loadKeyPair(tt.cfg, tt.certField, tt.keyField, tt.description, tt.required)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("loadKeyPair() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
			if !tt.wantErr && (certs == nil) != tt.wantNil {
				t.Errorf("loadKeyPair() certs is nil = %v, wantNil %v", certs == nil, tt.wantNil)
			}
		})
	}
}
