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

| Directory | Purpose |
| :-- | :-- |
| `cmd/versi` | CLI entry point, flag parsing, OS signal handling, lifecycle orchestration |
| `pkg/config` | TOML config parsing & validation, `BaseCollector`, `BaseReceiver`, `Sender`, `State` |
| `pkg/http` | Ingress and egress over HTTP/1.1, HTTP/2, and HTTP/3 with TLS/mTLS |
| `pkg/sql` | SQL collectors supporting various relational databases |
| `pkg/flow` | Generic `Batcher` mechanism, using typed `Chunk`s of data |
| `pkg/codec` | Various data formatters and encoders |
| `pkg/dest` | Default senders for local destinations: `discard`, `stdout`, `dead_letter_queue` |
| `pkg/cache` | Concurrency-safe in-memory caching mechanisms |
| `pkg/cron` | Cron schedule parsing and triggering for collectors |

## Development & Coding Practices

(See also [`CONTRIBUTING.md`](./CONTRIBUTING.md))

### Primary Language: [Go 1.27](./go.mod#L3)

Use modern Go whenever possible, and ignore compatibility with older versions, e.g.:

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
- Propagate and respect `context.Context` cancellation (except where a [detached lifecycle](#concurrency) applies).

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

Logging: see the [Logging](#logging) section.

Testing:

- Prefer table-driven tests with `t.Run()`; the `dupl` lint check flags duplicated test bodies.
- Use `t.Parallel()` and `t.Helper()` where appropriate.

### Preventing Test Flakiness

- Timing-dependent tests should use `testing/synctest` (`synctest.Test`, `synctest.Sleep`, `synctest.Wait`).
- SQL tests use in-memory SQLite (`modernc.org/sqlite`, pure Go) and a fake `pgPool`, so no database server is needed.
- `pkg/http/sys_test.go` runs end-to-end tests with local loopback servers and self-generated TLS certificates.

## Design and Patterns

### Initialization

- Stages: default & configured senders → configured collectors → configured receivers.
  - Collectors are started concurrently (capped at `GOMAXPROCS`).
  - Receivers are started sequentially, ordered by name (so conflicts produce consistent errors).
- Doesn't fail fast:
  - Each stage validates all relevant components and logs all errors.
  - The process exits if any stage fails, but only before moving to the next stage, and after shutting down the components that already started (in the normal [shutdown order](#lifecycle-and-shutdown)).

### Lifecycle and Shutdown

- **Shutdown signal**: SIGINT/SIGTERM cancels the root context and causes the app to enter "lame-duck" mode: graceful completion of work in progress without accepting new work.
- **Shutdown order**: receivers close in parallel with collectors draining, then all senders close except the Dead-Letter Queue, and the DLQ closes **last**, so it's able to save data that other senders fail to deliver while they close (including the data of their aborted workers).

#### `config.State`

`config.State` (embedded by value) manages the lifecycle of collectors, receivers, and senders; its API is documented in [`state.go`](./pkg/config/state.go). Rules that aren't obvious from it:

- Each component calls `StartWith(ctx)` (collectors, which also get `schedCtx` and `execCtx`) or `Start()` exactly once, and its `Close()` returns early if `!IsStarted()`, so it doesn't consume `CloseOnce` before startup.
- `BeginClose()` and `FinishClose()` close channels, so they're **not** idempotent: call them only inside `CloseOnce.Do()`.
- `IfClosingElseLock()` returns `false` **with the read lock still held**: the caller must call `CloseMu.RUnlock()`.
- **`CloseMu` rules** (senders, receivers, and batcher `guard`s):
  - `BeginClose()` takes the write lock, so it waits for all current readers before any close timeout starts. Senders and receivers register work in `InProgress` only under the read lock, so none can register after `BeginClose()`.
  - **Nothing under the read lock may block**: it would delay `BeginClose()`, and a pending `Lock()` blocks new readers. Only check and register work under it; encode and serialize in the registered goroutines. HTTP bodies are fully buffered before `Send()`, so reading them is never network I/O.
  - **Never acquire the read lock recursively** (e.g., in dispatch functions, which already run under that of `Send()` or `guard`): it may deadlock with a pending `Lock()`.

#### `Close()` Sequences

How each component rejects new work, and its `Close()` sequence (configurable timeouts are capped at `CloseTimeout`):

| Component | Rejecting new work | `Close()` sequence | Max wait |
| :-- | :-- | :-- | :-- |
| Collectors | Scheduling stops once `schedCtx` is canceled (see [Collectors' Contexts](#collectors-contexts)) | `BeginClose()` → `InProgress.Wait()` up to the timeout → on timeout, `StopStragglers()` → wait up to `AbortTimeout` → `FinishClose()` | `CloseTimeout + AbortTimeout` |
| HTTP receivers | `ServeHTTP()` responds with `503`; otherwise it releases the read lock before reading the body | `BeginClose()` → graceful server `Shutdown()` → `InProgress.Wait()` → `FinishClose()`, all within the timeout | `CloseTimeout` |
| HTTP senders | `Send()` holds the read lock throughout, so each chunk is accepted or rejected as a whole; rejected data goes to the fallback | `BeginClose()` → `Batcher.Flush()` and `InProgress.Wait()` up to the timeout → `FinishClose()`, which also aborts in-flight requests after a timeout (their contexts are detached) → wait up to `AbortTimeout`, for aborted workers to divert their data to the DLQ | `CloseTimeout + AbortTimeout` |
| DLQ | Like HTTP senders, but rejected data is discarded with a warning | `BeginClose()` → `InProgress.Wait()` up to 1 second → `FinishClose()` | 1 second |
| `stdout` | Its own lame-duck flag (no `config.State`), then discards | Waits up to 1 second | 1 second |
| `discard` | Never | No-op | None |

- SQL collectors also close their connection pools if the first wait completed in time (see the [SQL](#sql) section).

#### Collectors' Contexts

- `schedCtx` derives from the root context, so the shutdown signal cancels it before the scheduler goroutine calls `Close()`. Therefore `schedCtx.Err() != nil` means "shutdown in progress": the scheduling loop checks it before acquiring a concurrency slot (a `select` may pick a ready timer over a canceled context), and workers check it to stop retrying.
- `execCtx` is detached with `context.WithoutCancel()`, so in-flight data retrieval gets a grace period until `StopStragglers()`. Data retrieval loops check it right before each `Batcher.AddItem()`, with nothing that may block in between, so they stop adding data once it's canceled.
- The scheduler goroutine calls `Close()` (deferred) after its loop ends; `main()` never calls it, it only waits for `Done()`.

#### Batcher Guard

The batcher `guard` (collectors and senders) holds `CloseMu.RLock()` during timed dispatches; synchronous dispatches by `Batcher.Add*()` are covered by the caller's read lock (e.g., `Send()`'s).

- Senders' `guard` is `State.IfNotClosing()`, which also checks `IsClosing()`. Collectors' `guard` doesn't need to: `Flush()` invalidates the batch of any pending timer (generation check), so later timed dispatches find nothing to dispatch.
- A timed dispatch may have already taken a batch when a collector's `Close()` calls `Batcher.Flush()`, so `Close()` holds `CloseMu.Lock()` across its final `Flush()` and `FinishClose()`: the dispatch reaches the sender before `Done()` lets `main()` close senders.

#### Graceful Rejection

Data that a sender accepted but can't deliver goes to its fallback sender via `deadLetter()`: the DLQ if it's available, otherwise `dest.Discard`, which safely releases resources (closes HTTP request and response bodies).

- Use-cases:
  - The DLQ, `stdout`, and `discard` senders always use `dest.Discard`.
  - Structured batches with encoding errors.
  - Operations still waiting for a concurrency slot when `Close()` begins (including batches that it flushes).
  - HTTP deliveries that fail after the last retry attempt (`sendWithRetries()` calls its `reject` function).
  - Data that reaches a sender after it started closing (e.g., from straggler collector workers after `Done()`, or HTTP receiver handlers after a forced server close): senders in lame-duck mode pass it to their fallback.
  - Producers that call the DLQ's `Send()` after it closed (see the [Dead-Letter Queue](#dead-letter-queue) section).
- Each rejection path must pass the data in a usable state: HTTP requests/responses get a fresh body from `GetBody`/the buffered copy, because the original may have been read or closed.

### Concurrency

- **Detached lifecycles**: when passing a context to a function that may outlive the caller's context (e.g., `Sender.Send()`, `Batcher.Add*()`, and `Batcher.Flush()` compared to their callers), the **target method** is responsible for replacing `ctx` with `context.WithoutCancel(ctx)`, when it keeps the context after returning.
- **Concurrency limits**:
  - Collectors and senders should support this as a configurable option, with reasonable defaults and guardrails.
  - **Collectors skip** excess operations instead of catching up.
  - **Senders queue** excess operations (without blocking), they never reject/drop operations (until shutdown, when pending operations are sent to the sender's fallback, see [Graceful Rejection](#graceful-rejection)).

### Data Flow (`pkg/flow`)

- Data is always passed in `Chunk`s: flat (never nested), homogeneous slices of recognized item types (`flow.Item`, depending on the source and optional formatting of the data). When passing a single item, it still needs to be wrapped.
- Chunks and items in chunks must never be `nil`, and empty items and payloads should be ignored.
- **Structured data** has two chunk types: `JSON` (valid JSON objects, kept verbatim) and `Rows` (tabular data, e.g., SQL results, with column names shared per result-set and typed values). Senders encode them only when sending.
- **Ownership**:
  - `Batcher.AddItem()` takes ownership of the item provided to it.
  - `Batcher.AddChunk()` and `Sender.Send()` take ownership of both the chunk/slice and the items it contains.
  - Callers must never modify or reuse them afterwards.

### Batching

- Any collector, receiver, or sender *may* be integrated with a batcher, when it's meaningful and beneficial.
- Batching occurs based on the amount of items/records or a time window - whichever comes first. `MaxBytes` exists in `flow.Limits`, but nothing implements `sizeOf` functions yet, so don't enable it.
- Batch sizes count **items, never chunks or calls**; chunk boundaries carry no meaning: senders may split or merge them.
- **Collectors** with a batcher should call `Flush()` at the end of each retrieval operation (e.g., SQL query): if there's remaining buffered data there's no point in waiting for the batch window timeout, and if there isn't then it's a no-op.
- `Batcher` dispatches **synchronously**: in the caller's goroutine, or in the timer's goroutine via `guard`. Don't parallelize it. Owners rely on this for `IsClosing()`/`WaitGroup` correctness, so concurrency belongs in the dispatch function.
- The **order of batches is non-deterministic**, both across components and at the destination.

### Formatting (`pkg/codec`)

- `Format`s are meaningful only for structured data.
- Collectors/receivers pass their configured format downstream in `Send()`.
- If a sender has its own explicit format, it overrides the upstream one; specifically, `stdout` always prints readable NDJSON.
- Configuring a non-default format in HTTP collectors/receivers causes them to discard HTTP headers and pass only the body: as `Blobs` (`raw`) or parsed `JSON` objects (JSON formats); HTTP requests/responses with unparsable bodies go to their `Fallback` (the DLQ), and receivers respond with a `422` status code.
- SQL collectors default to `ndjson`, and the HTTP sender falls back to `ndjson` when neither it nor the source sets a format.
- `codec` encoders are strict and lossless: one unencodable value fails the entire payload, except non-finite floats, which become the strings `"NaN"`, `"Infinity"`, and `"-Infinity"` (`codec.NonFiniteFloats`, shared with `stdout`).
- `DecodeJSON` is zero-copy: each object aliases the payload (24 B per object, regardless of its size), so body buffers must never be modified or reused (e.g., pooled) afterwards.
  - Don't decode JSON into maps: each one costs hundreds of bytes, so untrusted payloads of tiny objects could amplify memory usage dozens of times.
- `Rows` keep column names as-is; the `codec` formatters make duplicate/empty names unique only when encoding rows as JSON objects, and the `stdout` sender prints them as-is.

### Configuration

- `config/versi.toml` (the default config path) is git-ignored and may hold local test settings or even junk.
- If the default file is missing, the app runs with an empty config and exits because nothing is configured.
- Example configs live in other files under `config/`, e.g., `sql_queries.toml` and `http_proxy.toml`.
- `pkg/config` decodes a TOML file into a raw `map[string]any`; each package parses its own sub-sections with the helpers `config.Value[T]` and `config.BoundedInt`.
- TOML integers are `int64` and floats are `float64`; do not expect or provide `int` and `float` in config maps or tests.
- Section names are TOML paths too: `ExtractSubmaps` finds a key (`collector`, `receiver`, `sender`) at any depth, e.g., `[collector]`, `[abc.collector]`, and `[[x.y.z.collector]]` all define collectors.
  - Exception: the DLQ is governed by a single, optional top-level `[dead_letter_queue]` table (parsed by `parseDLQ()` in `pkg/dest/config.go`).

### Logging

- Lint-enforced: use only the `log/slog` package, with `snake_cased` attribute keys, and only attribute functions (e.g., `slog.String("key", val)`), never bare `"key", value` pairs.
- When recording an error, `slog.Any("error", err)` must always be the first attribute.
- `Info`: Useful(!) status updates that convey more than just "time has passed".
- `Warn`: Config/data/runtime issues that the user should be aware of (but the app is functioning as expected).
- `Error`:
  - Unexpected app issues, usually bugs (at least in the sense that they should be handled better in order to recover/ignore).
  - Unrecoverable configuration issues that cause the app to abort on startup.
- Security & privacy:
  - If logging URLs, use `url.Redacted()` to prevent credential leakage.
  - Never log SQL connection strings, HTTP headers, or TLS key material.

### HTTP

- **HTTP transport caching**: via `pkg/cache.FastCache`, keyed by a composite `transportID` built from the TLS config checksum, max header size, and timeout.
  - Any setting that affects transport behavior **must** be added to that key, or clients with different settings will silently share a transport.
- **Retries**: POST and PATCH have no retries unless configured explicitly, because they may not be idempotent. Other methods default to exponential backoff (3 attempts, 1s interval, 20s cap). Once closing, senders stop retrying, and `State.Closing` interrupts their backoffs.
- **Body reuse for retries**: received requests get a `GetBody` function, and collected responses wrap their body in `reusableBody` (the `bodyProvider` interface), so retries don't copy large payloads. Keep this zero-copy path.
- **Memory**: work registered for retries can live for many seconds, so after encoding a payload, release references to the original items (e.g., `data, encode = nil, nil` in captured variables) so only the encoded body stays in memory.
- **Receiver responses**: `readRequest()` writes only error statuses; `ServeHTTP()` writes the success status (`202`) after processing the body, so it can still reject it.
- Payload types that the HTTP sender supports: `[]byte` (with auto-detected MIME content type), `*http.Request` (from receivers), `*http.Response` (from collectors), and `JSON` objects and `Rows` (batched and encoded in the resolved format, see the [Formatting](#formatting-pkgcodec) section).

### SQL

- SQL transactions are not read-only because some database drivers don't support read-only transactions, and it's better for other databases to have a consistent behavior.
- Transactions are committed only after all the results are processed and flushed, otherwise the transactions are rolled back. Partial results were already dispatched, so they may be collected again next time, but never lost (i.e. "at least once" semantics).
- PostgreSQL rollbacks use `execCtx` as well as a 1-second `AbortTimeout` to make sure they don't delay shutdowns. Generic rollbacks don't accept a context argument.
- SQL collectors close their connection pools only if no queries are in progress when `Close()` begins waiting (e.g., `@once` collectors that finished on their own, while the process keeps running). Otherwise the pools stay open, because:
  - Closing a pool blocks until all connections are released, so it could stall `Close()` before `FinishClose()`.
  - It wouldn't abort hung queries anyway (canceling `execCtx` does).
  - The process exits right after shutdown.

### Dead-Letter Queue

The DLQ is the last resort for data that a component accepted but can't deliver (see [Graceful Rejection](#graceful-rejection)). Its internals (write path, encoding, size accounting) are documented in `pkg/dest/dlq.go` and [`docs/dead_letter_queue.md`](./docs/dead_letter_queue.md). Rules for components that use it:

- **Getting it**: `NewBaseCollector()` and `NewBaseReceiver()` set `Fallback` to the DLQ's `Send()`, and senders receive the DLQ in their constructor. It's `nil` when there's no DLQ (e.g., in tests), so always call it via `deadLetter()` (`pkg/http/format.go`), which falls back to `dest.Discard` and returns the name for the `fallback` log attribute.
- **What to pass**: any `flow.Chunk`, with the component's format. Structured data is encoded in that format, blobs get it as their file extension, and HTTP requests/responses are written as-is (pass `codec.FormatDefault`). The DLQ takes ownership, closes HTTP bodies, and redacts sensitive headers and query parameters itself, so don't redact before passing data.
- **Fire and forget**: `Send()` never blocks and never reports back. It may discard data (when `max_size` is reached, after a write's last retry, or after the DLQ closed, including writes that were still waiting for its initial size measurement), always with a log message. So producers log their own rejection (with the `fallback` attribute), which is also the only record of *why* data was dead-lettered (DLQ files don't record it yet, see roadmap), and never retry through the DLQ.
- **Lifecycle**: it starts before, and closes after, every other component. Producers that outlive their own close timeouts may call `Send()` after it closed, which then discards the data with a warning. That's rare in practice: DLQ writes take milliseconds compared to its 1-second timeout, and the process exits as soon as the DLQ is closed.
