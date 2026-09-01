![Versipellis - The Data Flow Shape Shifter](./images/banner.jpg)

# Versipellis - The Data Flow Shape Shifter

[![Code Wiki](https://img.shields.io/badge/Gemini-code_wiki-007d9c?logo=googlegemini)](https://codewiki.google/github.com/daabr/versipellis)
[![Go Reference](https://pkg.go.dev/badge/github.com/daabr/versipellis.svg)](https://pkg.go.dev/github.com/daabr/versipellis)
[![Codecov](https://codecov.io/gh/daabr/versipellis/graph/badge.svg?token=IZFQXL47EM)](https://codecov.io/gh/daabr/versipellis)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/14316/badge)](https://www.bestpractices.dev/projects/14316)
<!-- Waiting for the badge to show the score instead of "invalid repo path" (the viewer link already works)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/daabr/versipellis/badge)](https://scorecard.dev/viewer/?uri=github.com/daabr/versipellis)
-->

Versipellis is a versatile, scalable tool for transferring and transforming data reliably across diverse media, protocols, and formats, without altering the data itself.

It is not a data pipeline, but rather a powerful yet easy-to-use conduit for pipeline inputs and outputs.

In other words: ["Let's take all this data, and push it somewhere else."](https://knowyourmeme.com/memes/push-it-somewhere-else-patrick)

![Let's take all this data, and push it somewhere else](./images/patrick_meme.png)

## Getting Started

1. Choose any of the installation options below
2. [Quickstart demos & tutorials](./docs/tutorials.md)
3. [Configuration reference](./docs/config.md)

### Installation Option 1: Docker Image

<https://github.com/daabr/versipellis/pkgs/container/versipellis>

Command line example:

```shell
docker run -d --name my-versi-container \
       -v $HOME/versi/config:/app/config -v $HOME/versi/data:/app/data \
       -p 4884:4884/tcp -p 4885:4885/tcp -p 3885:3885/udp \
       ghcr.io/daabr/versipellis:latest
```

- Platforms: Linux (amd64/arm64)
- Volumes / bind mount points:
  - `/app/config` - for Versipellis `.toml` files, ODBC `.ini` files (instead of `/etc`), Oracle `.ora` files, passwords and certificates, etc.
  - `/app/data` - for backup storage of runtime input data
- Already bundled and tested with:
  - Oracle Instant Client
  - unixODBC

### Installation Option 2: Precompiled Executable Binary

<https://github.com/daabr/versipellis/releases/latest>

- Platforms:
  - Linux (amd64/arm64)
  - macOS (amd64/arm64)
  - Windows (amd64 only)
- Optional runtime dependencies:
  - [Oracle Instant Client (basic lite)](https://www.oracle.com/database/technologies/instant-client.html) - needed only for Oracle Database
  - [unixODBC](https://github.com/alexbrainman/odbc/wiki) - needed only for ODBC, and only on Linux and macOS

### Installation Option 3: Build From Source

Required Go version: [1.27](https://go.dev/dl/)

Command line:

```shell
CGO_ENABLED=0 go build ./cmd/versi && ./versi -h
```

Or:

```shell
CGO_ENABLED=1 go build -tags=odbc ./cmd/versi && ./versi -h
```

- `CGO_ENABLED=1` - optional, needed only for Oracle Database and unixODBC
- `-tags=odbc` - optional, needed only for unixODBC

## Supported Inputs

### SQL Input

Supported SQL-based databases:

- CockroachDB
- MariaDB
- Microsoft SQL Server
- MySQL
- ODBC
  - Teradata
- Oracle Database
- PostgreSQL
- SAP HANA
- Snowflake
- SQLite
