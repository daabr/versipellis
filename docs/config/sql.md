# Versipellis Configuration File

## Connection Strings for SQL-Based Relational Databases

### CockroachDB

- Same as the [PostgreSQL driver](#postgresql) (see below)

- Documentation:

  - <https://www.cockroachlabs.com/docs/v26.2/connect-to-the-database#go>
  - <https://www.cockroachlabs.com/docs/v26.2/connection-parameters>

### MariaDB

- Same as the [MySQL driver](#mysql) (see below)

### MySQL

- Format: `username:password@[protocol(address)]/dbname[?param1=value1&...&paramN=valueN]`

- Documentation:

  - <https://github.com/go-sql-driver/mysql#dsn-data-source-name>
  - <https://github.com/go-sql-driver/mysql#parameters>
  - <https://github.com/go-sql-driver/mysql#examples>

### SQLite

- Formats:

  - File path: `/path/filename.sqlite`
  - URI: `file:///path/filename.sqlite[?param1=value1&...&paramN=valueN]`

- Documentation:

  - <https://pkg.go.dev/modernc.org/sqlite#Driver.Open>
  - <https://sqlite.org/c3ref/open.html>
  - <https://sqlite.org/pragma.html>
  - <https://sqlite.org/uri.html>
  
### PostgreSQL

- Formats:

  - URI: `postgres[ql]://user:password@[host[:port][,host2[:port2],...]][/dbname][?param1=value1&...&paramN=valueN]`
  - Space-separated key/value pairs: `key1=value1 key2=value2 ... keyN=valueN`

- Documentation:

  - <https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING>
  - <https://pkg.go.dev/github.com/jackc/pgx/v5/stdlib>
