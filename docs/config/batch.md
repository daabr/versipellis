# Versipellis Configuration Reference

## `[*.batch]` Sub-Section

This sub-section can be added to these configurations:

- Collectors: [SQL](./collector/sql.md)
- Senders: [HTTP](./sender/http.md), [HTTP/3](./sender/http3.md)

However, note that senders ignore batch settings in these cases:

- Full HTTP requests from HTTP receivers can't be joined, each one has its own headers, body, and lifecycle
- Full HTTP responses from HTTP collectors are also sent individually, for the same reason

At any rate, this is entirely optional. Components that support batching specify their default behavior in their documentation pages.

Additional notes:

- Collectors dispatch any remaining data at the end of each scheduled operation
- All components dispatch all available data before shutting down

`max_items` - maximum number of items/records to accumulate and dispatch together in each batch

- Optional
- Valid range: any positive integer up to `1_000_000`
- Special case: `0` = batching is disabled
  - `1` also causes immediate dispatching, but unlike `0` it also splits pre-chunked data into single items/records
- Negative integers are normalized to `0`
- Integers greater than the maximum are normalized to the maximum

<!-- Out of scope for now, but kept as a reminder:
`max_bytes` - maximum byte size for each batch

- Optional
- Valid range: any positive integer up to `1_073_741_824` (1 GiB)
- Special case: `0` = no size limit
- Negative integers are normalized to `0`
- Integers greater than the maximum are normalized to the maximum
- Individual data items that exceed the limit are sent on their own, not dropped
-->

`time_window` - maximum amount of time to wait after the first item before dispatching a partial batch

- Optional
- Format: string containing decimal numbers, each with a unit suffix, e.g., `s` (seconds), and `ms` (milliseconds)
- Special case: `"0"` and negative values (e.g., `"-1s"`) = no time limit,\
  dispatch batches only when they're ready, or when shutting down
