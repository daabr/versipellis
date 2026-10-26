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
- Default: `10_485_760` (10 MiB)
- Valid range: any positive integer up to `104_857_600` (100 MiB)
- `0` and negative integers are normalized to the default
- Integers greater than the maximum are normalized to the maximum

`max_headers_size` - maximum byte size of a client request's headers (as a whole, not each key-value pair)

- Optional
- Default: `1_048_576` (1 MiB)
- Valid range: any positive integer up to `104_857_600` (100 MiB)
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
- If the body can't be parsed in the configured format, the entire HTTP request is sent to the [Dead-Letter Queue (DLQ)](../../dead_letter_queue.md), and the client receives a `422 Unprocessable Content` response

## `[receiver.http.tls]` Sub-Section

- Optional for HTTP/1.1, required for HTTP/2
- [See this page](../tls.md)
