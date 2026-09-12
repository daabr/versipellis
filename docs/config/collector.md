# Versipellis Configuration Reference

## `[collector]` Sections

`type` - what kind of data to retrieve / how to retrieve it

- Required
- Options (case insensitive):
  - `"http"` (HTTP/1.1 + HTTP/2)
  - `"http3"`
  - `"sql"`

`schedule` - cron schedule expression

- Required
- More details here: [syntax and examples](./schedule.md)

`timezone` - name of the effective time zone for the schedule

- Optional
- Default: `"UTC"`
- Options:
  - `"UTC"` (case insensitive)
  - `"local"` (case insensitive)
  - Any valid identifier from the [IANA Time Zone database](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones) (case sensitive!), e.g., `"America/Los_Angeles"`

`concurrency_limit` - how many collection operations are allowed to run concurrently

- Optional
- Default: `1` (no concurrency)
- Valid range: any non-negative integer between `0` and `100`
- `0` and `1` both have the same effect: at most 1 operation of this collector may be in progress at any given time
- Larger positive integers (`2 ≤ n ≤ 100`): up to `n` concurrent operations of this collector may be in progress
- If the collector is at its concurrency limit when a new operation gets triggered, the operation is skipped, not delayed

> [!WARNING]
> Concurrency limit > 1 should be reserved for stateless, idempotent, or partitioned data collection - to prevent concurrent runs from processing the same data more than once.

`destination` - where to send the data to

- Optional
- Default: `"discard"` or `"none"`
- Options (case insensitive):
  - `"discard"` or `"none"`
  - `"stdout"`

## `[collector.http]` (HTTP/1.1 + HTTP/2) Sub-Section

[See this dedicated page](./collector/http.md).

## `[collector.http3]` Sub-Section

[See this dedicated page](./collector/http3.md).

## `[collector.sql]` Sub-Section

[See this dedicated page](./collector/sql.md).
