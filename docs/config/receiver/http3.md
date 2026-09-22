# Versipellis Configuration Reference

## `[receiver.http3]` Sub-Section

`address` - local UDP address to receive HTTP/3 requests

- Required
- Examples:
  - `"localhost:3474"`
  - `"127.0.0.1:3474"` (IPv4)
  - `"[::1]:3474"` (IPv6, must be enclosed in square brackets)

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

## `[receiver.http3.tls]` Sub-Section

- Required
- [See this page](../tls.md)
