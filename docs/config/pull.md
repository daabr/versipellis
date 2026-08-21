# Versipellis Configuration File

## `[pull]` Sections

`type` - what kind of data to pull / how to pull it

- Required
- Options (case insensitive):
  - `"none"`
  - `"http"` (HTTP/1.1 + HTTP/2)
  - `"http/3"`
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
  - Any valid identifier from the [IANA Time Zone database](https://en.wikipedia.org/wiki/List_of_tz_database_time_zones) (case sensitive!), e.g. `"America/Los_Angeles"`

## `[pull.sql]` Sub-Section

`type` - type of the SQL-based relational database to connect to

- Required
- Options (case insensitive):
  - `"cockroachdb"` ([CockroachDB](https://www.cockroachlabs.com/))
  - `"mariadb"` ([MariaDB](https://mariadb.com/))
  - `"mysql"` ([MySQL](https://www.mysql.com/))
  - `"postgres"` or `"postgresql"` ([PostgreSQL](https://www.postgresql.org/))
  - `"sqlite"` ([SQLite](https://sqlite.org/))

`connection` - database connection string for the SQL client

- Required
- More details here: [formats and documentation links](./sql.md)

`query` - simple SQL query to execute

- Required **unless** `query_file` is specified (mutually exclusive)

`query_file` - file path to multiline SQL query to execute

- Required **unless** `query` is specified (mutually exclusive)

`timeout` - timeout for SQL client queries

- Optional
- Default: `"1m"`
- Format: string containing 1-3 numbers, each with a unit suffix: `h` (hours), `m` (minutes), and `s` (seconds)
- Special case: `"0"` and negative values (e.g. `"-1s"`) = no client-side timeout
