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

`type` - driver type of the SQL-based database to connect to

- Required
- Options (case insensitive):
  - `"cockroachdb"` ([CockroachDB](https://www.cockroachlabs.com/))
  - `"mssql"` or `"sqlserver"` ([Microsoft SQL Server](https://www.microsoft.com/en-us/sql-server))
  - `"mariadb"` ([MariaDB](https://mariadb.com/))
  - `"mysql"` ([MySQL](https://www.mysql.com/))
  - `"odbc"` ([Open Database Connectivity](https://github.com/Microsoft/ODBC-Specification) - see additional [setup instructions](./sql.md))
  - `"postgres"` or `"postgresql"` ([PostgreSQL](https://www.postgresql.org/))
  - `"sap_hana"` ([SAP HANA](https://www.sap.com/products/data-cloud/hana/what-is-sap-hana.html))
  - `"snowflake"` ([Snowflake](https://www.snowflake.com/))
  - `"sqlite"` ([SQLite](https://sqlite.org/))

`connection` - database connection string for the SQL client

- Required
- More details here: [formats and documentation links](./sql.md#connection-strings-for-sql-based-databases)

`query` or `query_file` - SQL query to execute

- Required, but...
- Only one of them, not both (they're mutually exclusive):
  - Inline (e.g., `"SELECT * FROM table;"`) - usually when it's short
  - Relative or absolute path to a file containing the query (e.g., `"config/query.sql"` or `"/path/query.sql"`) - usually when it's complex or sensitive

`timeout` - timeout for SQL client queries

- Optional
- Default: `"1m"`
- Format: string containing 1-3 numbers, each with a unit suffix: `h` (hours), `m` (minutes), and `s` (seconds)
- Special case: `"0"` and negative values (e.g., `"-1s"`) = no client-side timeout
