# Versipellis Configuration Reference

## `[dead_letter_queue]` Section

The Dead-Letter Queue (DLQ) is enabled by default and doesn't require any configuration, but it may be customized through this optional section.

Unlike collectors, receivers, and senders, there can be at most one `[dead_letter_queue]` section, at the top level of the TOML file (without a namespace prefix).

For information about the DLQ itself, see [this page](../dead_letter_queue.md).

`warning_size` - log a warning when the total byte size of the DLQ directory reaches this threshold

- Optional
- Default: `104_857_600` (100 MiB)
- Valid range: any positive integer up to `1_125_899_906_842_624` (1 PiB)
- Special case: `0` = warning is disabled
- Negative integers are normalized to `0`
- Integers greater than the maximum are normalized to the maximum

> [!NOTE]
> The warning is logged _once_ when the size reaches the threshold, and\
> again _only after the size drops back below it_ and then reaches it again.

`max_size` - maximum total size of the DLQ directory, in bytes

- Optional
- Default: `0` (unlimited)
- Valid range: any positive integer up to `1_125_899_906_842_624` (1 PiB)
- Special case: `0` = no size limit
- Negative integers are normalized to `0`
- Integers greater than the maximum are normalized to the maximum
- New content that would cause the DLQ to exceed this limit is logged and discarded

> [!NOTE]
> Initial and recurring measurements are all done in the background, so large directories won't delay any operation. However, when `max_size` is set, new files wait (in memory) until the initial measurement is done, to ensure they can't exceed it.
