# Versipellis Configuration Reference

## `[sender.http]` Sub-Section

`url` - server address, in the form `"http[s]://host[:port][/path][?query]"`

- Required
- If the host is an IPv6 address, it must be enclosed in square brackets, e.g., `[fe80::1]`
- Query parameters may be specified here as a suffix (e.g., `?param1=value1&param2=value2`), but it's recommended to specify them in `query` (see the notes there)

`method` - HTTP request method, a.k.a. verb, for sending data

- Optional
- Default: `"POST"`
- Options (case insensitive): `"PATCH"`, `"POST"`, `"PUT"`

`query` - HTTP query parameters

- Optional
- Query parameters may be specified as a suffix in `url`, but it's recommended to specify them here:
  - Values in `query` override values in `url`, if they have the same parameter names
  - Values in `query` are allowed to contain dynamic expressions which are evaluated during runtime (although this is not implemented yet), while `url` must be static
- The TOML file format supports multiple representation options for key-value pairs:

  ```toml
  [sender.http]
  # ...
  query = { param1 = "value1", param2 = "value2" }
  ```

  ```toml
  [sender.http]
  # ...
  query = {
      param1 = "value1",
      param2 = "value2",
  }
  ```

  ```toml
  [sender.http]
  # ...
  query.param1 = "value1"
  query.param2 = "value2"
  ```

  ```toml
  [sender.http]
  # ...

  [sender.http.query]
  param1 = "value1"
  param2 = "value2"
  ```

`headers` - HTTP request headers

- Optional
- The TOML file format does not allow multiple instances of the same field, so if you want to specify a header with multiple values, specify them as a single comma-separated string:

  ```toml
  accept = "text/plain, application/json, application/xml"
  ```

- The TOML file format supports multiple representation options for key-value pairs:

  ```toml
  [sender.http]
  # ...
  headers = { header1 = "value1", header2 = "value2" }
  ```

  ```toml
  [sender.http]
  # ...
  headers = {
      header1 = "value1",
      header2 = "value2",
  }
  ```

  ```toml
  [sender.http]
  # ...
  headers.header1 = "value1"
  headers.header2 = "value2"
  ```

  ```toml
  [sender.http]
  # ...

  [sender.http.headers]
  header1 = "value1"
  header2 = "value2"
  ```

`timeout` - maximum duration of time for each HTTP client request to complete

- Optional
- Default: `"5s"` (5 seconds)
- Format: string containing decimal numbers, each with a unit suffix, e.g., `h` (hours), `m` (minutes), and `s` (seconds)
- Special case: `"0"` and negative values (e.g., `"-1s"`) = no client-side timeout

## `[sender.http.tls]` Sub-Section

[See this page](../tls.md)

## `[sender.http.retries]` Sub-Section

[See this page](../retries.md)
