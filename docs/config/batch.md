# Versipellis Configuration Reference

## `[*.batch]` Sub-Section

This sub-section can be added to **any** configurable collector, receiver, and sender.

It is entirely optional, but it's also ignored in the following cases:

- Full HTTP requests from HTTP receivers - because each HTTP request has its own headers, body, and lifecycle
- Full HTTP responses from HTTP collectors - for the same reason as HTTP requests

`max_items` - maximum number of items/records to accumulate and dispatch together in each batch

- Optional
- Default: _(depending on collector/receiver/sender type)_
- Valid range: any positive integer up to `1_000_000`
- Special case: `0` = batching is disabled
  - `1` also causes immediate dispatching, but unlike `0` it also splits pre-chunked data into single items/records
- Negative integers are normalized to `0`
- Integers greater than the maximum are normalized to the maximum

<!-- Out of scope for now, but kept as a reminder:
`max_bytes` - maximum byte size for each batch

- Optional
- Default: _(depending on collector/receiver/sender type)_
- Valid range: any positive integer up to `1_073_741_824` (1 GiB)
- Special case: `"0"` = no size limit
- Negative integers are normalized to `0`
- Integers greater than the maximum are normalized to the maximum
- Items that are larger than the limit are sent on their own, not dropped
-->

`time_window` - maximum amount of time to wait after the first item before dispatching a partial batch

- Optional
- Default: _(depending on collector/receiver/sender type)_
- Format: string containing decimal numbers, each with a unit suffix, e.g., `s` (seconds), and `ms` (milliseconds)
- Special case: `"0"` and negative values (e.g., `"-1s"`) = no time limit,\
  dispatch batches only when they're full, or when shutting down
