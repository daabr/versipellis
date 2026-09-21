# Versipellis Configuration Reference

## `[receiver]` Sections

`type` - what kind of data to receive / how to receive it

- Required
- Options (case insensitive):
  - `"http"` (HTTP/1.1 + HTTP/2)
  - `"http3"`

`destination` - where to send the data to

- Optional
- Default: `"discard"` or `"none"`
- Options (case sensitive!):
  - `"discard"` or `"none"`
  - `"stdout"`
  - `"dead_letter_queue"`
  - The full name of any sender which is configured in another section in the TOML file,\
    e.g., `sender.http`, `remus.lupin.sender.http`, `sender.http3[1]`, `sender.http3[2]`, etc.

## `[receiver.http]` (HTTP/1.1 + HTTP/2) Sub-Section

[See this page](./http.md)

## `[receiver.http3]` Sub-Section

[See this page](./http3.md)
