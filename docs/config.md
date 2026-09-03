# Versipellis Configuration Reference

The default configuration file path is `config/versi.toml`.

You may use the `--config` (or `-c`) CLI flag to specify a different file path.

For information about the TOML configuration language, refer to: https://toml.io/

Versipellis recognizes the following section **types**. They are all optional, and each one is described in a separate page:

- [`Collector`](./config/collector.md)
- 🚧 **Coming soon:** Receiver
- 🚧 **Coming soon:** Sender

What does a section "type" mean? Versipellis supports 0 or **more** instances of each section listed above.

- If you want to have just one instance of a section, simply name it by its type name:

  ```toml
  [collector]
  key = "value"
  ```

- If you want to have multiple instances of a section type, name each one with a unique namespace prefix:

  ```toml
  [larry.collector]
  key = "value 1"

  [lucian.collector]
  key = "value 2"

  [remus.lupin.collector]
  key = "value 3"
  ```

- You can also define an "anonymous list" of sections (TOML array-of-tables) instead of namespace prefixes; entries are implicitly numbered (e.g., `collector[1]`, `collector[2]`, ...):

  ```toml
  [[collector]]
  key = "value 1"

  [[collector]]
  key = "value 2"

  [[collector]]
  key = "value 3"
  ```
