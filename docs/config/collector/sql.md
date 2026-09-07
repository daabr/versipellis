# Versipellis Configuration Reference

## `[collector.sql]` Sub-Section

`type` - driver type of the SQL-based database to connect to

- Required
- Options (case insensitive):
  - `"cockroachdb"` ([CockroachDB](https://www.cockroachlabs.com/))
  - `"mssql"` or `"sqlserver"` ([Microsoft SQL Server](https://www.microsoft.com/en-us/sql-server))
  - `"mariadb"` ([MariaDB](https://mariadb.com/))
  - `"mysql"` ([MySQL](https://www.mysql.com/))
  - `"odbc"` ([Open Database Connectivity](https://github.com/Microsoft/ODBC-Specification) - see additional [setup instructions](../sql.md))
  - `"oracle"` ([Oracle Database](https://www.oracle.com/database/) - see additional [setup instructions](../sql.md))
  - `"postgres"` or `"postgresql"` ([PostgreSQL](https://www.postgresql.org/))
  - `"sap_hana"` ([SAP HANA](https://www.sap.com/products/data-cloud/hana/what-is-sap-hana.html))
  - `"snowflake"` ([Snowflake](https://www.snowflake.com/))
  - `"sqlite"` ([SQLite](https://sqlite.org/))

`connection` - database connection string for the SQL client

- Required
- More details here: [formats and documentation links](../sql.md#connection-strings-for-sql-based-databases)

`query` or `query_file` - SQL query to execute

- Required, but...
- Use only one of them, not both (they're mutually exclusive):
  - Inline (e.g., `"SELECT * FROM table"`) - usually when the query is short and simple
  - Relative or absolute path to a file containing the query (e.g., `"config/query.sql"` or `"/path/query.sql"`) - when it's long, complex, or sensitive

`timeout` - maximum duration of time for each SQL client query to complete

- Optional
- Default: `"1m"` (1 minute)
- Format: string containing 1-3 numbers, each with a unit suffix: `h` (hours), `m` (minutes), and `s` (seconds)
- Special case: `"0"` and negative values (e.g., `"-1s"`) = no client-side timeout
