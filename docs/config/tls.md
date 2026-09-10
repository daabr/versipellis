# Versipellis Configuration Reference

## `[*.tls]` Sub-Section

This sub-section can be added to [HTTP](./collector/http.md) and [HTTP/3](./collector/http3.md) collectors.

🚧 **Coming soon:** sender and receiver support.

It is entirely optional, but some fields may be required in some use-cases.

### m/TLS Client (Collector or Sender)

`server_ca_cert_file` - relative or absolute path to the server's **CA** certificate

- Optional for both TLS and mTLS
- The OS default pool may be customized with these environment variables:
  - `SSL_CERT_FILE` - file path containing PEM-encoded root certificate authorities
  - `SSL_CERT_DIR` - directory path(s) containing certificate files, separated by colons (or semicolons on Windows)

`mtls_client_cert_file` - relative or absolute path to the client certificate

- Required for mTLS
- Must not be specified for regular TLS
- Note: HTTP/3 (QUIC) does not support mTLS

`mtls_client_key_file` - relative or absolute path to the client private key

- Required for mTLS
- Must not be specified for regular TLS
- Note: HTTP/3 (QUIC) does not support mTLS

### m/TLS Server (Receiver)

`server_cert_file` - relative or absolute path to the server certificate

- Required for both TLS and mTLS

`server_key_file` - relative or absolute path to the server private key

- Required for both TLS and mTLS

`mtls_client_ca_cert_file` - relative or absolute path to the client's **CA** certificate

- Required for mTLS
- Must not be specified for regular TLS
- Note: HTTP/3 (QUIC) does not support mTLS

### Common to Both, but Not Recommended

> [!CAUTION]
> Do not use either of these settings in a production environment!

`min_version` - minimum TLS version to allow

- Optional
- Default: `"1.3"`
- Options: `"1.2"`, `"1.3"`
- Note: HTTP/3 (QUIC) does not support TLS 1.2

`dont_verify` - disable verifying the certificate chain and host name

- Optional
- Default: `false`
- Options: `true`, `false`
