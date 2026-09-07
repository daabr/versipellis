# Versipellis Configuration Reference

## `[collector]` Sections

`type` - what kind of data to actively retrieve / how to retrieve it

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

`destination` - where to send the data to

- Optional
- Default: `"discard"` / `"none"`
- Options (case insensitive):
  - `"discard"` / `"none"`
  - `"stdout"`

## `[collector.http]` (HTTP/1.1 + HTTP/2) Sub-Section

[See this dedicated page](./collector/http.md).

## `[collector.http3]` Sub-Section

[See this dedicated page](./collector/http3.md).

## `[collector.sql]` Sub-Section

[See this dedicated page](./collector/sql.md).
