# Versipellis Demo: SQL Data Collection

This demo shows how to use Versipellis to export or replicate data from SQL-based databases.

To explore more configuration options and details in-depth, see the [configuration reference](../config.md), and in particular the [SQL page](../config/sql.md).

## Run Versipellis

We will be using this TOML configuration from Versipellis's sample file [`config/sql_queries.toml`](../../config/sql_queries.toml):

```toml
[collector]
type = "SQL"
schedule = "@every 5s"

[collector.sql]
type = "SQLite"
connection = "file::memory:?cache=shared"
query = "SELECT 'abc' AS bytes, 123 AS nums, CURRENT_TIMESTAMP AS objs UNION SELECT X'deadbeef', 3.14159, NULL"

# ...

destination = "stdout"
```

Start Versipellis:

```shell
versi -d -c config/sql_queries.toml
```

- `-d` = display debug log messages too (default = only info level and above)
- `-c config/sql_queries.toml` = override the default configuration file path (`config/versi.toml`)

Wait 5 seconds, and... Presto! That's it!

## Results & Explanations

When Versipellis starts, it initializes a data collector that executes SQL queries (`type = "SQL"`) every 5 seconds (`schedule = "@every 5s"`):

```log
INF starting to execute SQL queries driver=sqlite schedule="TZ=UTC @every 5s"
```

> [!TIP]
> Performance tip: SQL data collectors manage a connection pool with each configured database when they start. These pools are already efficient by default, but they also support additional tuning options tailored to your specific needs - see the [configuration reference](../config.md).

Our example query (`query = "SELECT ..."`) uses an in-memory instance (`connection = "file::memory:"`) of the SQLite database (`type = "SQLite"`).

For the sake of simplicity, this query doesn't actually read data from a table, it merely returns 2 disjoint rows of fake static data, which Versipellis dumps to Stdout (`destination = "stdout"`), encoded as [NDJSON, a.k.a. JSONL](https://ndjson.com/):

```json
{"bytes":"abc","nums":123,"objs":"2026-08-25 04:12:17"}
{"bytes":"3q2+7w==","nums":3.14159,"objs":null}
```

> [!TIP]
> While NDJSON is a convenient default for demos and testing, Versipellis also supports other formats for advanced datasets that require a schema, or huge datasets which would benefit from binary compression and resource efficiency - see the [configuration reference](../config.md).

```log
DBG SQL query execution completed successfully driver=sqlite rows=2 exec_duration=1.05ms in_use_conns=1 idle_conns=0
```

## Next Steps

Follow-up tutorials for advanced topics based on this one:

1. [Basic connection setup with real databases](./sql_2.md)

2. 🚧 **Coming soon:** Secure connection setup with real databases (auth and TLS)

3. 🚧 **Coming soon:** Dynamic evaluation of configuration expressions (secrets management & data checkpointing)
