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

`timeout` - maximum amount of time for each incoming HTTP client request to complete

- Optional
- Default: `"5s"` (5 seconds)
- Format: string containing decimal numbers, each with a unit suffix, e.g., `m` (minutes), and `s` (seconds)
- Special case: `"0"` and negative values (e.g., `"-1s"`) = no server-side timeout

`format` - discard all HTTP headers, pass only the HTTP body

- Optional
- Default: none (don't discard HTTP headers)
- Options:
  - `"json"` (an array if the body contains multiple objects, otherwise an object)
  - `"json_array"` (always an array, even when there are fewer than 2 objects)
  - `"ndjson"` ([NDJSON](https://ndjson.com/), a.k.a. JSONL)
  - `"raw"` (opaque byte payload)
- This format is a downstream preference...
  - It does not have to be identical to the body's own content type, for example: you can receive JSON and pass it as NDJSON, and vice versa
  - Either way, the configured [sender](../sender/README.md) may override this
- If the body can't be parsed in the configured format, the entire HTTP request is moved to the Dead-Letter Queue (DLQ), and the client receives a `422 Unprocessable Content` response

## `[receiver.http3.tls]` Sub-Section

- Required
- [See this page](../tls.md)
