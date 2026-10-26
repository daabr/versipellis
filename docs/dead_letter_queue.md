# Dead-Letter Queue (DLQ)

The DLQ is a local directory where Versipellis saves data that it accepted but couldn't deliver, to prevent data loss: `data` in the process's current working directory, or `/app/data` in the Docker image.

The DLQ doesn't require any configuration, but you may [limit its size](#size-limits).

## What Lands in the DLQ

| Where           | Why                    | Details                                                                            |
| :-------------- | :--------------------- | :--------------------------------------------------------------------------------- |
| HTTP collectors | Invalid content/format | The entire HTTP message, if its body can't be parsed in the configured `format`    |
| HTTP receivers  | Invalid content/format | Same as HTTP collectors, but receivers also respond with a `422` status code       |
| HTTP senders    | Invalid content/format | Structured data that can't be encoded in the sender's configured `format`          |
| HTTP senders    | Delivery failure       | Non-retryable errors, retries exhausted, or unhandled redirects                    |
| All components  | Shutdown               | Throttled or late data that wasn't sent yet, or was aborted, when shutdown started |

## Directory Tree

Each file has a unique and chronological (i.e. ["k-sortable"](https://en.wikipedia.org/wiki/K-sorted_sequence)) path:

```text
data/
├── YYYY-MM-DD__HH/
│   ├── nanosecond_timestamp __ random_suffix . data_type
│   └── ...
│
└── YYYY-MM-DD__HH/
    └── ...
```

Subdirectory names and timestamp prefixes in filenames are set in UTC, so they’re unaffected by daylight saving time changes.

Random suffixes in filenames are random numbers between 0 and $2^{63}$, encoded in base 36 (i.e. up to 13 characters: 0-9 and a-z). This is used to ensure that even identical payloads written at the exact same time won't overwrite each other.

File extensions reflect the file's content:

| Content                                             | Extension                                                    |
| :-------------------------------------------------- | :----------------------------------------------------------- |
| Structured data                                     | Based on format: `.json`, `.json_array`, or `.ndjson`        |
| Structured data that can't be encoded in its format | `.lossy_ndjson` (readable NDJSON, without unencodable items) |
| HTTP requests and responses                         | `.http_req` and `.http_resp`                                 |
| Opaque byte payloads                                | `.raw`                                                       |
| Writing in progress / interrupted / rename failed   | `.incomplete`                                                |

## Size Limits

When Versipellis starts, it measures in the background the total size of all the files in the DLQ directory. From then on, it counts its own writes, and re-measures the directory from time to time to notice external changes (e.g., files that you deleted): 7 minutes after changes and new writes, and gradually less often (up to about once an hour) while nothing changes.

By default, it logs a warning when the size reaches 100 MiB, but it doesn't limit the size. See the [configuration reference](./config/dead_letter_queue.md) for details.

When the DLQ is full, new files are discarded, and each one is logged. If you limit the size, new files wait for the initial measurement to end before they're written, so a DLQ that's already full can't exceed the limit while it's being measured.

## Replaying Data

- **Duplicates**: the DLQ provides at-least-once semantics. A request that fails after it was sent (e.g., a timeout while waiting for a response) may have been processed by the destination anyway, so replaying it may duplicate it. In the future, Versipellis will annotate such failures with `delivery_uncertain=true`.
- **Credentials**: redacted values in `.http_req` and `.http_resp` files must be replaced before replaying them.
- **Lossy files**: `.lossy_ndjson` files are readable, but they may differ from the original data (e.g., invalid UTF-8 characters), and they skip items that can't be encoded at all.
- **Cleanup**: delete files after replaying them. Versipellis notices the deletions when it re-measures the total size of its directory.
- **Metadata**: the DLQ doesn't record why each file was written, or where its data came from and was going to - yet (see the [roadmap](./roadmap.md)).

## Shutdown

During a shutdown, Versipellis closes all collectors and receivers first, then all the senders, and finally the DLQ - so it can still save data that other senders fail to deliver while they close, instead of losing it.
