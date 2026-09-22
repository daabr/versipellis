# Versipellis Configuration Reference

## `[receiver.http]` Sub-Section

`address` - local TCP address to receive HTTP/1.1 and HTTP/2 requests

- Required
- Examples:
  - `"localhost:2474"`
  - `"127.0.0.1:2474"` (IPv4)
  - `"[::1]:2474"` (IPv6, must be enclosed in square brackets)

`max_body_size` - maximum byte size of a client request's whole body

- Optional
- Default: `10485760` (10,485,760 bytes = 10 MiB)
- Valid range: any positive integer up to `104857600` (100 MiB)
- `0` and negative integers are normalized to the default
- Integers greater than the maximum are normalized to the maximum

`max_headers_size` - maximum byte size of a client request's headers (as a whole, not each key-value pair)

- Optional
- Default: `1048576` (1,048,576 bytes = 1 MiB)
- Valid range: any positive integer up to `104857600` (100 MiB)
- `0` and negative integers are normalized to the default
- Integers greater than the maximum are normalized to the maximum

`timeout` - maximum duration of time for each incoming HTTP client request to complete

- Optional
- Default: `"5s"` (5 seconds)
- Format: string containing decimal numbers, each with a unit suffix, e.g., `m` (minutes), and `s` (seconds)
- Special case: `"0"` and negative values (e.g., `"-1s"`) = no server-side timeout

## `[receiver.http.tls]` Sub-Section

- Optional for HTTP/1.1, required for HTTP/2
- [See this page](../tls.md)
