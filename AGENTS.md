# AI Agent Instructions for the Versipellis Project

## Overview

Versipellis (`versi`) transfers and transforms data across services, networks, protocols and formats, without altering the data itself.

Design priorities: ease of use, efficiency at scale, security, low footprint, and maintainability.

### User Documentation

- [`README.md`](./README.md)
- `docs/`: [config reference](./docs/config/), [roadmap](./docs/roadmap.md), [tutorials](./docs/tutorials/), etc.

## Architecture

### Primary Abstractions

- **Collector**: Pull-based ingress retrieving data from a source server on a schedule or trigger.
- **Receiver**: Push-based ingress listening for inbound data from source clients.
- **Sender**: Asynchronous egress pushing data to a destination sink or server.

```text
Collectors (cron schedule) ──┐
                             ├──► (optional batching) ──► Senders (async egress + optional batcher)
Receivers (HTTP servers) ────┘
```

### Package Layout

| Directory     | Purpose                                                                                      |
| :------------ | :------------------------------------------------------------------------------------------- |
| `cmd/versi/`  | CLI entry point, flag parsing, OS signal handling, lifecycle orchestration                   |
| `pkg/config/` | TOML config parsing & validation, `BaseCollector` & `BaseReceiver` types, `Sender` interface |
| `pkg/http/`   | Ingress and egress over HTTP/1.1, HTTP/2, and HTTP/3 with TLS/mTLS                           |
| `pkg/sql/`    | SQL collectors supporting many relational databases                                          |
| `pkg/flow/`   | Generic `Batcher` mechanism, using typed `Chunk`s of data                                    |
| `pkg/dest/`   | Default senders for local destinations: `discard`, `stdout`, `dead_letter_queue`             |
| `pkg/cache/`  | Concurrency-safe in-memory caching mechanisms                                                |
| `pkg/cron/`   | Cron schedule parsing and triggering for collectors                                          |

## Development & Coding Practices

(See also [`CONTRIBUTING.md`](./CONTRIBUTING.md))

### Primary Language: [Go 1.27](./go.mod#L3)

Ignore compatibility issues and behavior changes in older Go versions!

Common examples:

- `for range` over integers and iterators, `new(expr)`.
- Promoted fields from embedded structs in struct literals.
- `http.Response.Body.Close()` auto-drains HTTP/1 response bodies (up to 256 KiB or 50 ms).
- Prefer `time.After()` over `time.NewTimer()`, unless you specifically need `Stop()` or `Reset()`.
- Methods may declare their own generic type parameters, independent of the receiver's.
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
# Run complete test suite to check correctness and detect flakiness.
go test -count 5 ./pkg/...

# Run complete test suite with race detection and test coverage reporting.
go test -race -coverprofile=coverage.out ./pkg/...

# Exclude HTTP tests in sandboxed environments (without loopback socket access).
go test -race $(go list ./pkg/... | grep -v /pkg/http)

# Targeted test execution.
go test -count 1 -run TestFuncName ./pkg/http/...
```

Auto-formatting + static analysis with auto-fixes (details in the next section):

```shell
golangci-lint fmt && golangci-lint run
```

### Linter-Enforced Conventions ([`.golangci.yml`](./.golangci.yml))

Contexts:

- Structs may not contain `context.Context` fields.
- Propagate and respect `context.Context` cancellation (except where a detached lifecycle applies, see below).

Documentation:

- Comments must be full grammatically correct sentences, beginning with a capital letter and ending with a period.
- `//nolint` directives must be specific and followed by a short justification.
  - **Attention**: `//gosec:disable` directives are not covered by the `nolintlint` check, but should follow the same rules.
- Comments starting with `TODO`, `BUG`, and `FIXME` may not be merged into the main branch.

Error handling:

- Returned errors and type assertions should always be checked.
  - Exception 1: if an error really doesn't matter and shouldn't even be logged, avoid lint warnings with `_ = funcName()`.
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

- Code: 130 characters.
- Doc comments: 120 characters.

Logging:

- Use only the `log/slog` package.
- Attribute key names must be `snake_cased`.
- Use only attribute functions (e.g., `slog.String("key", val)`), never bare `"key", value` pairs.

Testing:

- Prefer table-driven tests with `t.Run()`; the `dupl` lint check flags duplicated test bodies.
- Use `t.Parallel()` and `t.Helper()` where appropriate.

## Patterns, Conventions, Design Details

### Initialization

- Stages: default & configured senders → configured collectors → configured receivers.
  - Collectors are started concurrently (capped at `GOMAXPROCS`).
  - Receivers are started sequentially, ordered by name (so conflicts produce consistent errors).
- Doesn't fail fast:
  - Each stage validates all relevant components and logs all errors.
  - The process exits if any stage fails, but only before moving to the next stage.

### Lifecycle and Concurrency

- **Collectors** use two cancelable contexts:
  - `schedCtx` is canceled when initiating shutdown, which stops scheduling and retries.
  - `execCtx` is detached with `context.WithoutCancel()`, so in-flight data retrieval gets a grace period.
    - Canceled only if `Close()` times out.
- Collectors also have a `closed` channel which is closed at the end of `Close()`, and exposed to `main()` via `Done()` to signal completion.
- **Detached lifecycles**: when passing a context to a function that may outlive the caller's context (e.g., `Sender.Send()`, `Batcher.Add*()`, and `Batcher.Flush()` compared to their callers), the **target method** is responsible for replacing `ctx` with `context.WithoutCancel(ctx)`, when it keeps the context after returning.
- **Concurrency limits**:
  - Collectors and senders should support this as a configurable option, with reasonable defaults and guardrails.
  - **Collectors skip** excess operations instead of catching up.
  - **Senders queue** excess operations (without blocking), they never reject/drop operations (until shutdown).

### Shutting Down

- SIGINT/SIGTERM cancels the root context (the app enters "lame-duck" mode: graceful completion of work in progress without accepting new work).
- Shutdown order: receivers close in parallel with collectors draining, then all senders close except the Dead Letter Queue, and the DLQ closes **last**, so it's able to save data that other senders fail to deliver while they close.
- **Senders** use `lameDuck atomic.Bool` plus a `closeMu sync.RWMutex`: `Send()` holds `closeMu.RLock()` for its entire duration, checks `lameDuck`, then registers work in a `sync.WaitGroup`, so each chunk is accepted or rejected as a whole. An extra `lameDuck` check before acquiring the lock is an optional fast path.
  - **Nothing under `closeMu.RLock()` may block**: `Close()` waits for it before its timeout starts, and a pending `Lock()` blocks new readers. Only check and register work under the lock; encode and serialize in the registered goroutines. HTTP bodies must be fully buffered before `Send()`, so reading them is never network I/O.
  - **Batching**: a full batch dispatched synchronously by `Batcher.Add*()` is covered by `Send()`'s read lock, and delayed dispatches are covered by the batcher's `guard`, which acquires the read lock and checks `lameDuck`. Dispatch functions must not acquire `closeMu.RLock()` again, because recursive read locking may deadlock with a pending `closeMu.Lock()`.
  - `Close()`: `closeMu.Lock()` → `lameDuck = true` → unlock → `Batcher.Flush()` (if any) → `inProgress.Wait()` with a timeout. Holding the write lock ensures no `Send()` can register after the wait begins.
  - Exceptions: `discard` and `stdout` address simpler use-cases.
- **Collectors with a batcher** use the same `guard` pattern: a timed dispatch may have already taken a batch from the buffer when `Close()` calls `Batcher.Flush()`, so `Close()` sets `closing` under `closingMu.Lock()` before flushing, to ensure the dispatch reaches the sender before `Done()` signals `main()` to close senders.
- **Graceful rejection**: payloads rejected while shutting down are routed to `dest.Discard` to safely release their resources (closes HTTP request and response bodies).
- `Close()` methods are idempotent and safe before startup. HTTP/SQL collectors wait at most `CloseTimeout + abortTimeout`; HTTP senders and receivers wait at most `CloseTimeout` (or an earlier context deadline). Local destination senders return immediately or use a fixed one-second timeout.

### Data Flow

- Data is always passed in `Chunk`s: flat (never nested), homogeneous slices of recognized types (depending on the source). When passing a single item, it still needs to be wrapped.
- Chunks and items in chunks must never be `nil`, and empty items and payloads should be ignored.
- **Ownership**:
  - `Batcher.AddItem()` takes ownership of the item provided to it.
  - `Batcher.AddChunk()` and `Sender.Send()` take ownership of both the chunk/slice and the items it contains.
  - Callers must never modify or reuse them afterwards.

### Batching

- Any collector, receiver, or sender *may* be integrated with a batcher, when it's meaningful and beneficial.
- Batching occurs based on the amount of items/records or a time window - whichever comes first. `MaxBytes` exists in `flow.Limits`, but nothing implements `sizeOf` functions yet, so don't enable it.
- Batch sizes count **items, never chunks or calls**; chunk boundaries carry no meaning: senders may split or merge them.
- **Collectors** with a batcher should call `Flush()` at the end of each retrieval operation (e.g., SQL query): if there's remaining buffered data there's no point in waiting for the batch window timeout, and if there isn't then it's a no-op.
- `Batcher` dispatches **synchronously**: in the caller's goroutine, or in the timer's goroutine via `guard`. Don't parallelize it. Owners rely on this for `lameDuck`/`WaitGroup` correctness, so concurrency belongs in the dispatch function.
- The **order of batches is non-deterministic**, both across components and at the destination.

### HTTP

- **HTTP transport caching**: via `pkg/cache.FastCache`, keyed by a composite `transportID` built from the TLS config checksum, max header size, and timeout.
  - Any setting that affects transport behavior **must** be added to that key, or clients with different settings will silently share a transport.
- **Retries**: POST and PATCH have no retries unless configured explicitly, because they may not be idempotent. Other methods default to exponential backoff (3 attempts, 1s interval, 20s cap).
- **Body reuse for retries**: received requests get a `GetBody` function, and collected responses wrap their body in `reusableBody` (the `bodyProvider` interface), so retries don't copy large payloads. Keep this zero-copy path.
- Payload types that the HTTP sender supports: `[]byte` (with auto-detected MIME content type), `*http.Request` (from receivers), `*http.Response` (from collectors), and `map[string]any` (encoded as NDJSON using `encoding/json/v2`).

### Logging

- `Info`: Useful(!) status updates that convey more than just "time has passed".
- `Warn`: Config/data/runtime issues that the user should be aware of (but the app is functioning as expected).
- `Error`:
  - Unexpected app issues, usually bugs (at least in the sense that they should be handled better in order to recover/ignore).
  - Unrecoverable configuration issues that cause the app to abort on startup.
- When recording an error, `slog.Any("error", err)` must always be the first attribute.
- Security & privacy:
  - If logging URLs, use `url.Redacted()` to prevent credential leakage.
  - Never log SQL connection strings, HTTP headers, or TLS key material.

### Preventing Test Flakiness

- Timing-dependent tests should use `testing/synctest` (`synctest.Test`, `synctest.Sleep`, `synctest.Wait`).
- SQL tests use in-memory SQLite (`modernc.org/sqlite`, pure Go) and a fake `pgPool`, so no database server is needed.
- `pkg/http/sys_test.go` runs end-to-end tests with local loopback servers and self-generated TLS certificates.

### Configuration

- `config/versi.toml` (the default config path) is git-ignored and may hold local test settings or even junk.
- If the default file is missing, the app runs with an empty config and exits because nothing is configured.
- Example configs live in other files under `config/`, e.g., `sql_queries.toml` and `http_proxy.toml`.
- `pkg/config` decodes a TOML file into a raw `map[string]any`; each package parses its own sub-sections with the helpers `config.Value[T]` and `config.BoundedInt`.
- TOML integers are `int64` and floats are `float64`; do not expect or provide `int` and `float` in config maps or tests.
- Section names are TOML paths too: `ExtractSubmaps` finds a key (`collector`, `receiver`, `sender`) at any depth, e.g., `[collector]`, `[abc.collector]`, and `[[x.y.z.collector]]` all define collectors.
