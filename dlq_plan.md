# Plan: Dead-Letter Queue for HTTP Delivery Failures

This plan covers the first item of the roadmap, and the DLQ size limits it requires.

## Goal

When the HTTP sender gives up on a request, the data goes to the sender's fallback (the DLQ) instead of only being
logged (`sendWithRetries()` in `pkg/http/client.go`). Afterwards, any data that a sender accepted is either delivered,
saved in the DLQ, or explicitly discarded (only when there's no fallback, or when the DLQ is full).

## Current State

Data already reaches the sender's fallback via `deadLetter()` in these cases:

| Path                                                            | Where                                                  |
| :-------------------------------------------------------------- | :----------------------------------------------------- |
| Unparsable request/response bodies (non-default `format`)       | HTTP receivers and collectors (`Fallback`)             |
| `Send()` calls after `Close()` began                            | `Sender.Send()`                                        |
| Operations waiting for a concurrency slot when `Close()` begins | `Sender.sendWithConcurrencyLimit()` `reject` callbacks |
| Structured batches that fail to encode                          | `Sender.sendStructured()`                              |

Not covered: **delivery failures after retries**, which this plan addresses.

## Design Decisions

### 1. Every final non-2xx outcome is a delivery failure

All of these go to the DLQ:

- Retries exhausted (5xx, 429, timeouts, network errors).
- Non-retryable 4xx responses: data errors (400, 422) and configuration errors (401, 403, 404), as well as 413.
- Request construction errors.
- Requests aborted by a forceful `Close()` (via `Sender.stop`).
- First attempts that fail during shutdown, which aren't retried (`i > 0 && s.lameDuck.Load()`).

In every case, the destination didn't acknowledge the data, so a single rule keeps the behavior simple and auditable.

### 2. Save the sender's input, not the outgoing request

| Data type                  | What the DLQ receives                                                                                   | Why                                                                                                                                                               |
| :------------------------- | :------------------------------------------------------------------------------------------------------ | :---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Blob                       | The blob, in its resolved format (`raw` unless the source set another format, e.g., an encoded payload) | Same as the existing `reject` path, see `Sender.outputFormat()`                                                                                                   |
| Request (from receivers)   | The original request, with a fresh body from `GetBody`                                                  | Same as `Sender.rejectRequest()`                                                                                                                                  |
| Response (from collectors) | The original response, with a fresh body                                                                | Same as `Sender.rejectResponse()`                                                                                                                                 |
| Structured batch           | The **encoded body** as `flow.Blobs`, with the resolved format                                          | Lossless (strict encoding already succeeded), and the original items are released after encoding (keeping them for this case would undo that memory optimization) |

Saving the outgoing request instead would persist the sender's configured headers on disk, including future auth
tokens (roadmap item 2). `AGENTS.md` already forbids even logging HTTP headers.

However, the original messages carry credentials too: received requests keep the client's headers (e.g.,
`Authorization`, `Cookie`, `Proxy-Authorization`), and collected responses keep `Set-Cookie`, and `encodeHTTPRequest()`
/ `encodeHTTPResponse()` write all of them into `.http_req` / `.http_resp` files today. This PR routes many more
messages to the DLQ, so it should also redact sensitive headers in these dumps (e.g., reusing the hop-by-hop and
sensitive header sets in `pkg/http/sender.go`, moved to a shared package), or document this explicitly as a known risk.

The structured case relies on the new DLQ file extensions: the encoded body is saved as `<name>.<format>`, so its
content type is still discoverable.

### 3. Structured data reaches the fallback in the sender's resolved format

The `format` argument passed to a sender's fallback needs a single meaning. For structured data, it's the format in
which the sender would deliver the data (`Sender.outputFormat()`), i.e. what the destination expects:

- Batches lose the upstream format of each item: batchers are keyed by the resolved format, and when the sender has
  an explicit format, items from sources with different formats share a batch. So rejections of batches that wait
  for a concurrency slot can only use the resolved format (as they do today).
- Encoded bodies from delivery failures (see the table above) are in the resolved format by definition.
- For batches that fail to encode, the format is irrelevant: the DLQ's strict encoding fails the same way, so it
  falls back to `.lossy_ndjson`.
- `Send()` calls rejected during shutdown (already implemented): `Send()` resolves the output format once, before
  deciding whether to accept or reject the data, so its batching and its rejections can't diverge.

This also refines decision 2: for structured data, the DLQ saves the sender's input (or the encoded output) in the
sender's output format. As a result, any DLQ file with structured data can be replayed directly to the sender's
destination, regardless of the rejection reason.

Trade-off: once fallback senders are configurable, sender A's format override propagates to its fallback sender B,
unless B has its own explicit format (see the open questions).

### 4. One rejection callback per operation, for both failure points

Each send path already builds a `reject` callback for operations that wait for a concurrency slot. The same
callback handles delivery failures, so each data type has exactly one path to the DLQ. Structured batches are the
exception: once encoding succeeds, their callback switches from the original items to the encoded body.

### 5. Aborted requests must reach the DLQ before it closes

Today, `Sender.Close()` closes `s.stop` after its timeout and returns immediately. `main()` then closes the DLQ, so
requests that were just aborted would reach a DLQ that's already closing, and be discarded. `Close()` should wait up
to `abortTimeout` for in-progress work after closing `s.stop`, like the HTTP and SQL collectors. This changes the
maximum duration of `Sender.Close()` to `CloseTimeout + abortTimeout`.

### 6. At-least-once semantics, with uncertain outcomes flagged

A timeout or connection reset after the request was written means the destination may have processed it (especially
relevant for POST). The data still goes to the DLQ, because duplicates are preferable to data loss. Using
`httptrace.ClientTrace.WroteRequest`, the failure log gets a `delivery_uncertain` attribute, so operators can
identify potential duplicates. Storing this per file is part of the DLQ metadata sub-item of roadmap item 4. Both
transports fire this hook (`net/http`, and `quic-go`'s `http3` since at least v0.63.0), and tests should cover both.

### 7. DLQ size limits

New optional settings (names, location, and defaults are open questions, see below):

- **Warning threshold** (e.g., `warning_size`, default 1 GiB): log a warning when the DLQ's total size crosses it.
- **Maximum size** (e.g., `max_size`, default 10 GiB, `0` = unlimited): discard new DLQ files that would exceed it,
  via `dest.Discard`, so HTTP bodies are still released.

Measurement:

- At startup, `InitDeadLetterQueue()` walks the DLQ directory (`os.Root.FS()` + `fs.WalkDir`), sums the file sizes
  into an `atomic.Int64`, and logs the result at `Info` level (plus a warning if a threshold is already exceeded).
  The walk runs in a background goroutine, so large backlogs don't delay startup; writes are counted meanwhile, and
  `max_size` is enforced only after the first measurement completes.
- Each file **reserves** its size with an atomic `Add()` once, before its write attempts (in `Send()`'s goroutine, not
  per attempt), and subtracts it only if all attempts fail and no partial file remains (i.e. `Remove()` succeeded), so
  concurrent writers can't overshoot the maximum together.
- The warning is edge-triggered: logged once when the total crosses the threshold, and re-armed when it drops below.

External changes:

- **Files deleted by other processes** (e.g., an operator or a replay tool) make the counter overestimate the size,
  so the DLQ could reject files although there's space. Mitigation: before discarding a file because of `max_size`
  (and before logging a threshold warning), re-measure the directory, at most once per minute, and reset the counter.
  This corrects the common case of cleanups without periodic scans. The re-measurement runs in a single background
  goroutine (single-flight, skipped in lame-duck mode), never in a writer's goroutine, so it can't delay writes past
  `Close()`'s one-second timeout; writers decide based on the current counter, without waiting for it.
- **Files added by other processes** (e.g., multiple `versi` instances sharing a volume) make it underestimate the
  size. This plan doesn't address that case: see the open questions.

## Steps (single PR)

1. **`pkg/http/client.go`**: `sendWithRetries()` returns whether the data was delivered, and whether a failure is
   uncertain (via `httptrace`). Its final failure log gets `delivery_uncertain` and `fallback` attributes.
2. **`pkg/http/sender.go`**: each send path calls its `reject` callback when delivery fails. In `sendStructured()`,
   the callback switches to the encoded body (`flow.Blobs` with the resolved format) once encoding succeeds.
3. **`Sender.Close()`**: wait up to `abortTimeout` for in-progress work after `close(s.stop)`.
4. **`pkg/dest/datadir.go`**: DLQ size measurement, reservations, edge-triggered warnings, the maximum size, and
   rate-limited re-measurement. Parse the new settings (see open questions).
5. **Tests**:
   - Each data type for each failure outcome: exhausted retries (5xx), non-retryable 4xx, and network errors.
   - Forceful `Close()` aborts a request, and the DLQ receives its data before `Close()` returns (`synctest`).
   - A first attempt that fails during shutdown goes straight to the DLQ.
   - Successful delivery sends nothing to the DLQ.
   - A sender with an explicit format passes structured data to its fallback in that format after delivery failures
     too (already tested for rejections during shutdown and while waiting for a concurrency slot).
   - A failed structured batch is saved as its encoded body, with the right format and file extension.
   - Optionally, a `runtime.AddCleanup` test that structured items are released during retries.
   - DLQ size: startup measurement, increments, edge-triggered warnings, rejection at the maximum, re-measurement
     after external deletions, and concurrent reservations.
   - `sys_test.go`: an end-to-end case, where a sender targets a closed port and produces a DLQ file.
6. **Documentation**:
   - A new **"Dead-Letter Queue" section in `AGENTS.md`**, which replaces the scattered DLQ notes: all the entry points
     (the table in "Current State" plus delivery failures), what each data type saves, at-least-once semantics, size
     accounting, and the close order. Also update the `Sender.Close()` timeout in "Shutting Down".
   - A new user-facing **`docs/dead-letter-queue.md`**: what lands in the DLQ and why, the file layout and extensions
     (see below), the size settings, and replay guidance (at-least-once, potential duplicates, lossy files).
   - `docs/config/sender/http.md` and `http3.md`: undeliverable data goes to the DLQ.
   - `docs/roadmap.md`: mark this sub-item as done.

## DLQ File Extensions

Already implemented (separately from this plan), based on the actual content of each file:

| Content | Extension |
| :------ | :-------- |
| Structured data, encoded strictly | Its resolved format: `.json` (also the default), `.json_array`, or `.ndjson` |
| Structured data, lossy fallback (readable NDJSON) | `.lossy_ndjson` |
| Blobs | Their format (e.g., `.raw`), none for the default format |
| HTTP request / response dumps | `.http_req` / `.http_resp` |

Possible improvement: **atomic visibility**, i.e. write to a temporary name and rename it after `Sync()`, so replay
tools never read partial files. Today, files are written in place.

## Risks and Open Questions

- **Log volume** (risk, not addressed in this PR): a misconfigured sender (e.g., a wrong token causing 401s) or a
  destination outage logs a warning per failed request, and potentially per discarded DLQ file.
- **Size settings**: the DLQ is currently zero-config. Where should its settings live (e.g., a new top-level
  `[dead_letter_queue]` section, or a `[sender.dead_letter_queue]` section), with which names and defaults?
- **External writers**: should multiple processes sharing a DLQ directory be supported (requiring periodic
  re-measurement, or per-process subdirectories), or should the documentation require one DLQ directory per process?
- **Disk-full errors** (`ENOSPC`) happen regardless of `max_size` (e.g., on shared volumes). They're logged as write
  errors today; should they also trigger re-measurement or a distinct warning?
- **Format propagation in fallback chains**: with configurable fallback senders, should a fallback sender re-resolve
  structured data from the source's format, rather than inherit its primary sender's format? That would require
  tracking the upstream format per item through batching (e.g., keying batchers by both formats, at the cost of
  smaller batches).
- **Replay tooling** isn't covered by this plan, but its needs (DLQ metadata, atomic visibility) should shape the
  follow-up sub-items of roadmap item 4.
