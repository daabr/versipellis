# Versipellis Tutorial: SQL Data Collection - Part 2

This tutorial is a continuation of the [SQL data collection](./sql_1.md) demo. It's essentially the same, but it shows you how to use real databases instead of SQLite, albeit with the simplest possible setup.

To explore more configuration options and details in-depth, see the [configuration reference](../config.md), and in particular the [SQL page](../config/sql.md). See also these [basic setup guides](../setup.md).

## Prerequisite: Comment-Out SQLite

Comment-out the SQLite block (lines 6-9) in the file [`config/sql_queries.toml`](../../config/sql_queries.toml#L6-L9).

## MariaDB

1. [Basic setup instructions for this database](../setup/mariadb.md)

2. Uncomment this block in the file [`config/sql_queries.toml`](../../config/sql_queries.toml#L11-L14):

   ```toml
   [collector.sql]
   type = "MariaDB"
   connection = "larry:woof@tcp(localhost)/versi_db?parseTime=true&timeout=3s"
   query = "SELECT * FROM input_data"
   ```

3. Restart Versipellis:

   ```shell
   versi -d -c config/sql_queries.toml
   ```

## MySQL

1. [Basic setup instructions for this database](../setup/mysql.md)

2. Uncomment this block in the file [`config/sql_queries.toml`](../../config/sql_queries.toml#L16-L19):

   ```toml
   [collector.sql]
   type = "MySQL"
   connection = "larry:woof@tcp(localhost)/versi_db?parseTime=true&timeout=3s"
   query = "SELECT * FROM input_data"
   ```

3. Restart Versipellis:

   ```shell
   versi -d -c config/sql_queries.toml
   ```

## Oracle Database

1. [Basic setup instructions for this database](../setup/oracle.md)

2. Uncomment this block in the file [`config/sql_queries.toml`](../../config/sql_queries.toml#L21-L24):

   ```toml
   [collector.sql]
   type = "Oracle"
   connection = "larry/woof@localhost/FREEPDB1"
   query = "SELECT * FROM input_data"
   ```

3. Restart Versipellis:

   ```shell
   versi -d -c config/sql_queries.toml
   ```

## PostgreSQL

1. [Basic setup instructions for this database](../setup/postgresql.md)

2. Uncomment this block in the file [`config/sql_queries.toml`](../../config/sql_queries.toml#L26-L31):

   ```toml
   [collector.sql]
   type = "PostgreSQL"
   connection = "replace this string with one of the two equivalent options below"
   # kv_option = "host=localhost dbname=versi_db user=larry passfile=config/.pgpass"
   # uri_option = "postgres://larry@localhost/versi_db?passfile=config/.pgpass"
   query = "SELECT * FROM input_data"
   ```

3. Set the value of the `connection` string field to either of these equivalent options:

   - Key-value pairs format: `"host=localhost dbname=versi_db user=larry passfile=config/.pgpass"`
   - URI format: `"postgres://larry@localhost/versi_db?passfile=config/.pgpass"`

4. Remove the `.example` suffix from the name of the file [`config/.pgpass.example`](../../config/.pgpass.example), and restrict access to it:

   ```shell
   mv config/.pgpass.example config/.pgpass
   chmod 0600 config/.pgpass # u=rw,go-rwx (owner can read and write, but no one else can)
   ```

5. Restart Versipellis:

   ```shell
   versi -d -c config/sql_queries.toml
   ```

> [!NOTE]
> This PostgreSQL example is different from the others in this page: it shows how to configure a connection more securely, without exposing the password in the TOML configuration file. The next tutorials expand on this topic.

## Next Steps

Follow-up tutorials for advanced topics based on this one:

1. 🚧 **Coming soon:** Secure connection setup with real databases (auth and TLS)

2. 🚧 **Coming soon:** Dynamic evaluation of configuration expressions (secrets management & data checkpointing)
