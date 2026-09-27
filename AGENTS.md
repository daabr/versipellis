# AI Agent Instructions for the Versipellis Project

## Overview

Versipellis (`versi`) transfers and transforms data between protocols and formats without altering the data itself.

It acts as an adapter and conduit for pipelines, sources, and sinks; it is not a data processing pipeline itself.

Design priorities: ease of use, efficiency at scale, security, low footprint, and maintainability.

### User Documentation

- [`README.md`](./README.md)
- `docs/`: [config reference](./docs/config/), [roadmap](./docs/roadmap.md), [tutorials](./docs/tutorials/), etc.

## Architecture

### Primary Abstractions

- **Collector**: Pull-based ingress retrieving data from a source server on a schedule or trigger.
- **Receiver**: Push-based ingress listening for inbound data from source clients.
- **Sender**: Asynchronous egress pushing data to a destination sink or external server.

```text
Collector (cron schedule) ──┐
                            ├──► Sender.Send (async egress, potentially batched - coming soon)
Receiver (HTTP servers) ────┘
```

### Package Layout

| Directory     | Purpose                                                                                              |
| :------------ | :--------------------------------------------------------------------------------------------------- |
| `cmd/versi/`  | CLI entry point, flag parsing, OS signal handling, lifecycle orchestration                           |
| `pkg/config/` | TOML configuration parsing, validation, `BaseCollector` and `BaseReceiver` types, `Sender` interface |
| `pkg/http/`   | Ingress and egress over HTTP/1.1, HTTP/2, HTTP/3 (QUIC) with TLS/mTLS                                |
| `pkg/sql/`    | SQL collectors supporting many relational databases                                                  |
| `pkg/dest/`   | Local / no-op destination sinks (`stdout`, `discard`, `dead_letter_queue`)                           |
| `pkg/cache/`  | Concurrency-safe in-memory caching mechanisms                                                        |
| `pkg/cron/`   | Cron schedule parsing and triggering for collectors                                                  |

## Development & Coding Practices

(See also [`CONTRIBUTING.md`](./CONTRIBUTING.md))

### Primary Language: [Go 1.27](./go.mod#L3)

Ignore compatibility issues and behavior changes in older Go versions!

Common examples:

- `for range` over integers and iterators, `new(expr)`.
- Promoted fields from embedded structs in struct literals.
- `http.Response.Body.Close()` auto-drains HTTP/1 response bodies (up to 256 KiB or 50 ms).
- Prefer `time.After()` over `time.NewTimer()`, unless you specifically need `Stop()` or `Reset()`.
- New APIs in existing packages: `errors.AsType`, `os.Root`, `sync.WaitGroup.Go`.
- New standard library packages: `encoding/json/v2`, `testing/synctest`.

### Useful Commands

Build:

```shell
# Standard pure-Go build.
CGO_ENABLED=0 go build ./cmd/versi

# With (and only for) ODBC and Oracle Database support.
CGO_ENABLED=1 go build -tags=odbc ./cmd/versi
```

Testing:

```shell
# Run complete test suite to check correctness.
go test ./pkg/...

# Run complete test suite with race detection and test coverage reporting.
go test -race -coverprofile=coverage.out ./pkg/...

# Exclude HTTP tests in sandboxed environments (without loopback socket access).
go test -race $(go list ./pkg/... | grep -v /pkg/http)

# Targeted test execution.
go test -count 1 -run TestXXX ./pkg/http/...
```

Auto-formatting and static analysis (with some auto-fixes):

```shell
golangci-lint fmt
golangci-lint run
```

### Mostly Lint-Enforced Conventions ([`.golangci.yml`](./.golangci.yml))

Contexts:

- Structs may not contain `context.Context` fields.

Documentation:

- Comments must be full grammatically correct sentences, beginning with a capital letter and ending with a period.
- `//nolint` directives must be specific and followed by a short justification.
  - **Attention**: `//gosec:disable` directives are not covered by the `nolintlint` check, but should follow the same rules!
- Comments starting with `TODO`, `BUG`, and `FIXME` may not be merged into the main branch.

Error handling:

- Returned errors and type assertions should always be checked.
  - Exception 1: if an error really doesn't matter and shouldn't even be logged, avoid lint warnings with `_ = someFunc()`.
  - Exception 2: `defer something.Close()` doesn't trigger a lint warning even if `Close()` returns an error.
- Use `errors.Is` / `errors.As` instead of direct equality or type assertions.
  - **Attention**: the `errorlint` check doesn't suggest `errors.AsType`, but prefer it over `errors.As` with a target variable.
- Wrap errors from external packages with `fmt.Errorf("message: %w", err)`.
  - Exceptions: errors from the `os` and `go-toml` packages are exempt from wrapping.

Imports:

- Strict 3-block ordering:
  1. Standard library packages.
  2. Third-party/external dependencies.
  3. Local module packages (`github.com/daabr/versipellis/...`).

Line length:

- Code: 130 characters
- Doc comments: 120 characters

Logging:

- Use only the `log/slog` package
- Attribute key names must be `snake_cased`.
- Use only attribute functions (e.g., `slog.String("key", val)`), never bare `"key", value` pairs.

Testing:

- Prefer table-driven tests with `t.Run()`; the `dupl` lint check flags duplicated test bodies.
- Use `t.Parallel()` and `t.Helper()` where appropriate.

## Patterns, Conventions, and Other Non-Obvious Details

Lifecycle and concurrency:

- Senders are initialized first, then collectors are started concurrently (capped at `GOMAXPROCS`), then receivers are started (sequentially, sorted by name, so port conflicts produce the same errors on every run).

- Init never fails fast: every stage is validated and all errors are logged, then the process exits if anything failed within each stage.

- **Collectors** use two contexts: `schedCtx` (cancelled when initiating shutdown) stops scheduling and retries, `execCtx` is derived from `context.WithoutCancel` and has its own cancel function, so in-flight data retrieval gets a grace period. It is cancelled only if `Close()` times out. A `closed` channel (exposed via `Done()`) signals completion back to `main()`, and an `aborted` flag suppresses sending results from workers that were forcibly stopped.

- Collectors skip missed schedule runs instead of catching up, and skip runs when the concurrency-limit semaphore is full.

- **Non-blocking contract**: `Sender.Send` must return quickly and never block the caller.

Shutting down:

- **Graceful shutdown**: always propagate and respect `context.Context` cancellation.

- **Detached lifecycles**: when passing a context to a task that may outlive the caller's context (e.g., `Sender.Send` calls from collectors and receivers), the caller must pass `context.WithoutCancel(ctx)` instead of `ctx` or `context.Background()`.

- SIGINT/SIGTERM cancels the root context ("lame-duck mode"). Shutdown order: receivers close in parallel with collectors draining, then all senders close except the Dead Letter Queue, and the DLQ closes **last**, so it can still accept data that other senders fail to deliver while they close.

- **Senders** use `lameDuck atomic.Bool` plus a `closeMu sync.RWMutex` in a double-checked pattern: `Send` checks `lameDuck`, serializes the payload, acquires `closeMu.RLock`, checks `lameDuck` again, then registers work in a `sync.WaitGroup`.

- `Sender.Close` acquires `closeMu.Lock` in order to set `lameDuck = true`, so no `Send` can register after `Wait` begins.

- **Graceful rejection**: payloads rejected during or after shutdown are routed to `dest.Discard` to safely release their resources (closes HTTP request and response bodies).

- `Close` methods of all entities are idempotent (`sync.Once`), safe to call on components that were never started, and wait at most the component's configured `timeout`, with a non-configurable upper bound (`CloseTimeout` + `abortTimeout` = 5s + 1s).

Configuration:

- `config/versi.toml` (the default config path) is git-ignored and may hold local test settings or even junk. If the default file is missing, the app runs with an empty config and exits because nothing is configured. Example configs live in other files under `config/`, e.g. `sql_queries.toml` and `http_proxy.toml`.

- `pkg/config` decodes a TOML file into a raw `map[string]any`; each package parses its own sub-sections with the generic `config.Value[T]` helper.

- TOML integers are `int64` and floats are `float64`; do not expect or provide `int` and `float` in config maps or tests.

- Section names are TOML paths too: `ExtractSubmaps` finds a key (`collector`, `receiver`, `sender`) at any depth, so `[collector]`, `[abc.collector]`, and `[[x.y.z.collector]]` all define collectors.

Data flow:

- Payload types that senders must handle: `[]byte`, `*http.Request` (from HTTP receivers), `*http.Response` (from HTTP
  collectors), `[]map[string]any` (rows from SQL collectors), and anything else as JSON via `encoding/json/v2`.

- `nil` is reserved as a batch start/end sentinel (coming soon). Senders must handle/ignore it, and never forward it.

Logging:

- When recording an error, `slog.Any("error", err)` must always be the first attribute.

- Always log URLs using `url.Redacted()` to prevent credential leakage.

Test flakiness prevention:

- Timing-dependent tests should use `testing/synctest` (`synctest.Test`, `synctest.Wait`, `synctest.Sleep`).

- SQL tests use in-memory SQLite (`modernc.org/sqlite`, pure Go) and a fake `pgPool`, so no database server is needed.

- `pkg/http/sys_test.go` runs end-to-end tests with local loopback servers and self-generated TLS certificates.

HTTP:

- **HTTP transport caching**: via `pkg/cache.FastCache`, keyed by a composite `transportID` built from the TLS config checksum, max header size, and timeout.

  - Any setting that affects transport behavior **must** be added to that key, or clients with different settings will silently share a transport.

- **Retries**: POST and PATCH have no retries unless configured explicitly, because they may not be idempotent. Other methods default to exponential backoff (3 attempts, 1s interval, 20s cap).

- **Body reuse for retries**: received requests get a `GetBody` func, and collected responses wrap their body in `reusableBody` (the `bodyProvider` interface), so retries don't copy large payloads. Keep this zero-copy path.
