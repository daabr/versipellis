# Versipellis Configuration Reference

## Additional Setup Instructions

- ODBC: <https://github.com/alexbrainman/odbc/wiki>

- Oracle Database: install the [Oracle Instant Client (basic lite)](https://www.oracle.com/database/technologies/instant-client.html)

Other drivers below don't require any setup prior to running Versipellis.

## Connection Strings for SQL-Based Databases

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
  - ADO.NET: `server=host; password="pass;word"; key1=value1; key2=value2; ... keyN=valueN`
  - ODBC: `odbc:server=host;user id=username;password={pass;word};key1=value1;key2=value2;...;keyN=valueN`

- Documentation:

  - <https://github.com/microsoft/go-mssqldb#connection-parameters-and-dsn>
  - <https://learn.microsoft.com/en-us/dotnet/framework/data/adonet/connection-string-syntax>
  - <https://learn.microsoft.com/en-us/dotnet/api/system.data.sqlclient.sqlconnection.connectionstring>
  - <https://github.com/microsoft/go-mssqldb/blob/main/msdsn/conn_str_test.go>

### MySQL

- Format: `username:password@[protocol(address)]/dbname[?param1=value1&...&paramN=valueN]`

- Documentation:

  - <https://github.com/go-sql-driver/mysql#dsn-data-source-name>
  - <https://github.com/go-sql-driver/mysql#parameters>
  - <https://github.com/go-sql-driver/mysql#examples>
  - <https://dev.mysql.com/doc/refman/en/server-system-variables.html>
  - <https://mariadb.com/docs/server/server-management/variables-and-modes/server-system-variables>
  - <https://github.com/go-sql-driver/mysql/blob/master/dsn_test.go>

### ODBC

- Formats:

  - Predefined DSN: `DSN=...;key1=value1;key2=value2;...;keyN=valueN`
  - Driver without DSN: `Driver={...};key1=value1;key2=value2;...;keyN=valueN`

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
  - <https://github.com/godror/godror/blob/main/doc/connection.md>
  - <https://www.oracle.com/developer/working-in-go-applications-with-oracle-database-and-oracle-cloud-autonomous-database/>
  - <https://www.oracle.com/pls/topic/lookup?ctx=dblatest&id=GUID-19423B71-3F6C-430F-84CC-18145CC2A818>
  - <https://github.com/godror/godror/blob/main/dsn/dsn_test.go>

### PostgreSQL

- Formats:

  - URI: `postgres[ql]://[user[:password]@][host[:port][,host2[:port2],...]][/dbname][?param1=value1&...&paramN=valueN]`
  - Space-separated key/value pairs: `key1=value1 key2=value2 ... keyN=valueN`

- Documentation:

  - <https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING>
  - <https://pkg.go.dev/github.com/jackc/pgx/v5/stdlib>
  - <https://github.com/jackc/pgx/tree/master/pgconn>

> [!TIP]
> Use the [`.pgpass` file](https://www.postgresql.org/docs/current/libpq-pgpass.html) to avoid exposing the user password in the connection string.\
> To use a non-default path for it, specify the [`passfile` parameter](https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNECT-PASSFILE) in the connection string, or set the `PGPASSFILE` environment variable.

### SAP HANA

- Format: `hdb://user:password@something.hanacloud.ondemand.com:port?TLSServerName=something.hanacloud.ondemand.com`

- Documentation:

  - <https://github.com/SAP/go-hdb#hana-cloud-connection>
  - <https://pkg.go.dev/github.com/SAP/go-hdb/driver#DSN>
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

### Teradata

- Use the generic [ODBC driver](#odbc) (see above), with an OS-specific Teradata driver:

  - [Linux](https://downloads.teradata.com/download/connectivity/odbc-driver/linux)
  - [macOS](https://downloads.teradata.com/download/connectivity/teradata-odbc-driver-for-mac-os-x)
  - [Windows](https://downloads.teradata.com/download/connectivity/odbc-driver/windows)

- Documentation:

  - <https://docs.teradata.com/r/Enterprise_IntelliFlex_Lake_VMware/ODBC-Driver-for-Teradata-User-Guide-20.00>
