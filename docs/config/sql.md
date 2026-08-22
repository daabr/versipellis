# Versipellis Configuration File

## Connection Strings for SQL-Based Relational Databases

### CockroachDB

- Same as the [PostgreSQL driver](#postgresql) (see below)

- Documentation:

  - <https://www.cockroachlabs.com/docs/v26.3/connect-to-the-database#go>
  - <https://www.cockroachlabs.com/docs/v26.3/connection-parameters>

### MariaDB

- Same as the [MySQL driver](#mysql) (see below)

### Microsoft SQL Server

- Formats:

  - URI: `sqlserver://username:password@host[:port][/instance][?param1=value1&...&paramN=valueN]`
  - ADO: `key1=value1;key2=value2;...;keyN=valueN`
  - ODBC-like: `odbc:key1=value1;key2=value2;...;keyN=valueN`

- Documentation:

  - <https://github.com/microsoft/go-mssqldb#connection-parameters-and-dsn>
  - <https://learn.microsoft.com/en-us/dotnet/framework/data/adonet/connection-string-syntax>

### MySQL

- Format: `username:password@[protocol(address)]/dbname[?param1=value1&...&paramN=valueN]`

- Documentation:

  - <https://github.com/go-sql-driver/mysql#dsn-data-source-name>
  - <https://github.com/go-sql-driver/mysql#parameters>
  - <https://github.com/go-sql-driver/mysql#examples>
  - <https://dev.mysql.com/doc/refman/en/server-system-variables.html>
  - <https://mariadb.com/docs/server/server-management/variables-and-modes/server-system-variables>

### Oracle Database

- Formats:

  - [Easy Connect](https://www.oracle.com/pls/topic/lookup?ctx=dblatest&id=GUID-B0437826-43C1-49EC-A94D-B650B6A4A6EE): `username[/password]@[//]host[:port][/[service][:server][/instance]]`
  - [Easy Connect Plus](https://download.oracle.com/ocomdocs/global/Oracle-Net-Easy-Connect-Plus.pdf): `[[protocol:]//]host1{,host12}[:port1]{,host2:port2}{;host1{,host12}[:port1]}[/[service][:server][/instance]][?param1=value1&...&paramN=valueN]`
  - [Connect Descriptor](https://www.oracle.com/pls/topic/lookup?ctx=dblatest&id=GUID-2BDF9E52-4147-4F46-84E2-A5AE1018A373)
  - [Service name](https://www.oracle.com/pls/topic/lookup?ctx=dblatest&id=GUID-7F967CE5-5498-427C-9390-4A5C6767ADAA) (as defined in a `tnsnames.ora` file)
  - URI: `oracle://username[:password]@server:port/service?param1=value1&...&paramN=valueN`
  - Space-separated key/value pairs: `user="scott" password="tiger" connectString="host:port/service" param1=value1 ... paramN=valueN`

- Documentation:

  - <https://github.com/godror/godror#connection>
  - <https://pkg.go.dev/github.com/godror/godror#pkg-overview>
  - <https://www.oracle.com/developer/working-in-go-applications-with-oracle-database-and-oracle-cloud-autonomous-database/>

### PostgreSQL

- Formats:

  - URI: `postgres[ql]://[user[:password]@][host[:port][,host2[:port2],...]][/dbname][?param1=value1&...&paramN=valueN]`
  - Space-separated key/value pairs: `key1=value1 key2=value2 ... keyN=valueN`

- Documentation:

  - <https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING>
  - <https://pkg.go.dev/github.com/jackc/pgx/v5/stdlib>

> [!TIP]
> Use the [`.pgpass` file](https://www.postgresql.org/docs/current/libpq-pgpass.html) to avoid exposing the user password in the connection string.\
> To use a non-default path for it, specify the [`passfile` parameter](https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNECT-PASSFILE) in the connection string, or set the `PGPASSFILE` environment variable.

### SAP HANA

- Format: `hdb://user:password@something.hanacloud.ondemand.com:port?TLSServerName=something.hanacloud.ondemand.com`

- Documentation:

  - <https://github.com/SAP/go-hdb#hana-cloud-connection>
  - <https://help.sap.com/docs/SAP_HANA_CLIENT/f1b440ded6144a54ada97ff95dac7adf/0ffbe86c9d9f44338441829c6bee15e6.html>
  - <https://help.sap.com/docs/hana-cloud-data-lake/client-interfaces/go-golang-driver>

### Snowflake

- Formats:

  - `username:password@<account_identifier>/dbname[/schemaname][?param1=value1&...&paramN=valueN]`
  - `username:password@hostname:port/dbname/schemaname?account=<account_identifier>[&param1=value1&...&paramN=valueN]`

- Documentation:

  - <https://pkg.go.dev/github.com/snowflakedb/gosnowflake/v2#hdr-Connection_String>
  - <https://pkg.go.dev/github.com/snowflakedb/gosnowflake/v2#hdr-Connection_Parameters>
  - <https://docs.snowflake.com/en/user-guide/admin-account-identifier>
  - <https://docs.snowflake.com/en/sql-reference/parameters>

### SQLite

- Formats:

  - File path: `/path/filename.sqlite`
  - URI: `file:///path/filename.sqlite[?param1=value1&...&paramN=valueN]`

- Documentation:

  - <https://pkg.go.dev/modernc.org/sqlite#Driver.Open>
  - <https://sqlite.org/c3ref/open.html>
  - <https://sqlite.org/pragma.html>
  - <https://sqlite.org/uri.html>
