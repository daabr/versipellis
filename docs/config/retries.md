# Versipellis Configuration Reference

## `[*.retries]` Sub-Section

This sub-section can be added to [HTTP](./collector/http.md) and [HTTP/3](./collector/http3.md) collectors.

🚧 **Coming soon:** sender support.

It is entirely optional, but some fields may be required in some use-cases.

> [!NOTE]
> The default retry policy is: up to 3 attempts, with exponential-backoff delays between them, starting from 1 second (±10% jitter).

> [!IMPORTANT]
> `PATCH` and `POST` requests do not have a default retry policy, they require an explicit configuration of retries, if desired. This is because these HTTP methods are not guaranteed to be stateless or idempotent, so retrying them by default could lead to data corruption or duplication or other undesirable side effects.

`type` - which retry strategy to use when sending requests

- Optional
- Default: `"backoff"`
- Options (case insensitive):
  - `"disabled"` or `"none"` (no retries)
  - `"backoff"` (exponential backoff)
  - `"static"` (constant interval)

`max_attempts` - maximum number of request attempts (original + retries)

- Optional, effective only when `type ≠ "disabled"`
- Default: `3`
- Valid range: any integer between `1` and `10`
- `1` means a single attempt without retries, effectively the same as `type = "disabled"`
- `n` (where `n > 1`) means 1 initial attempt followed by up to `n-1` retries

`interval` - initial wait period between attempts 

- Optional, effective only when `type ≠ "disabled"`
- Default: `"1s"` (1 second)
- Valid range: between `"1ms"` (1 millisecond) and `"1m"` (1 minute)
- Format: string containing decimal numbers, each with a unit suffix, e.g., `s` (seconds), and `ms` (milliseconds)
- When `type = "backoff"`, a jitter of ±10% is applied to each delay interval,\
  unless `max_interval ≤ interval`, which effectively forces `type = "static"`

`max_interval` - when the backoff interval exceeds this (it's multiplied by 2 on every iteration), reset it back to the initial `interval`

- Optional, effective only when `type = "backoff"`
- Default: `"20s"`
- Range: between whatever the `interval` value is and `"5m"`
- Format: string containing decimal numbers, each with a unit suffix, e.g., `s` (seconds), and `ms` (milliseconds)
- If `max_interval ≤ interval` then `max_interval` is normalized to `interval`, which effectively forces `type = "static"`
