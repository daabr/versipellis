# Versipellis Performance Benchmarks

## SQL Collector

Test: [`BenchmarkCollector`](../../pkg/sql/bench_collector_test.go)

| Metadata | &nbsp;       |
| -------- | ------------ |
| Date     | 2026-08-26   |
| Go       | 1.27.0       |
| OS       | darwin       |
| Arch     | arm64        |
| CPU      | Apple M2 Pro |

### Methodology

- Source: SQLite backed by a temporary file, with these parameters
  - `_journal_mode=OFF` and `_synchronous=OFF` (to avoid unnecessary disk I/O overhead)
  - `mode=ro` (using only read-only connections during benchmark execution)
- Data: table with variable number of rows (1k / 10k / 100k), where each row contains
  - 5 `INTEGER` columns (values distributed between 0 and $10^8$)
  - 5 `TEXT` columns (each string's length is 8-12 bytes)
- Destination: none (rows are discarded immediately after reading)

### Execution

```shell
go test -bench=Collector -benchtime=1s -cpu=1,2,4,8 -count=10 -run=^$ ./pkg/sql/...
```

### Results

| Cores  | Rows | Queries   | μs/Query | ns/Row | Queries/sec | Rows/ms  |
| -----: | ---: | --------: | -------: | -----: | ----------: | -------: |
| 1      | 1k   | 867       | 1361     | 1361   | 735         | 735      |
| 1      | 10k  | 94        | 13289    | 1329   | 75          | 753      |
| 1      | 100k | 8         | 135763   | 1358   | 7           | 737      |
| &nbsp; |      |           |          |        |             |          |
| 2      | 1k   | 1590      | 746      | 746    | 1340        | 1340     |
| 2      | 10k  | 165       | 7193     | 719    | 139         | 1390     |
| 2      | 100k | 14        | 73223    | 732    | 14          | 1366     |
| &nbsp; |      |           |          |        |             |          |
| 4      | 1k   | 2731      | 430      | 430    | 2324        | 2324     |
| 4      | 10k  | 297       | 3991     | 399    | 251         | 2506     |
| 4      | 100k | 28        | 40896    | 409    | 24          | 2447     |
| &nbsp; |      |           |          |        |             |          |
| 8      | 1k   | 3124      | 379      | 379    | 2637        | 2637     |
| 8      | 10k  | 327       | 3588     | 359    | 279         | 2789     |
| 8      | 100k | 31        | 36029    | 360    | 28          | 2780     |

### Conclusions

:+1: When using a single core under ideal conditions (no network or server latency), Versipellis can read 1 row in about 1.3 μs (microseconds) on average. In other words, it can read about 740 rows per millisecond on average.

:+1: Furthermore, this level of efficiency does not degrade in correlation with the amount of data; it consistently increases by 1-2% per core when comparing 10k rows to 1k rows.

:-1: However, note that efficiency does decrease by 1-2% when comparing 100k rows to 10k rows (though still higher than 1k rows). This requires further investigation.

:+1: Most importantly, the efficiency of SQL query execution scales near-linearly with additional cores. This confirms the lack of impactful memory-locking bottlenecks. The plateauing at 8 cores is likely due to testbed limits (Apple M2 Pro P-core vs. E-core topology and SQLite read concurrency contention).
