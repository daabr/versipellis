![Versipellis - The Data Flow Shape Shifter](./images/banner.jpg)

# Versipellis - The Data Flow Shape Shifter

[![Code Wiki](https://img.shields.io/badge/Gemini-code_wiki-007d9c?logo=googlegemini)](https://codewiki.google/github.com/daabr/versipellis)
[![DeepWiki](https://img.shields.io/badge/DeepWiki-gray.svg?logo=data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAACwAAAAyCAYAAAAnWDnqAAAAAXNSR0IArs4c6QAAA05JREFUaEPtmUtyEzEQhtWTQyQLHNak2AB7ZnyXZMEjXMGeK/AIi+QuHrMnbChYY7MIh8g01fJoopFb0uhhEqqcbWTp06/uv1saEDv4O3n3dV60RfP947Mm9/SQc0ICFQgzfc4CYZoTPAswgSJCCUJUnAAoRHOAUOcATwbmVLWdGoH//PB8mnKqScAhsD0kYP3j/Yt5LPQe2KvcXmGvRHcDnpxfL2zOYJ1mFwrryWTz0advv1Ut4CJgf5uhDuDj5eUcAUoahrdY/56ebRWeraTjMt/00Sh3UDtjgHtQNHwcRGOC98BJEAEymycmYcWwOprTgcB6VZ5JK5TAJ+fXGLBm3FDAmn6oPPjR4rKCAoJCal2eAiQp2x0vxTPB3ALO2CRkwmDy5WohzBDwSEFKRwPbknEggCPB/imwrycgxX2NzoMCHhPkDwqYMr9tRcP5qNrMZHkVnOjRMWwLCcr8ohBVb1OMjxLwGCvjTikrsBOiA6fNyCrm8V1rP93iVPpwaE+gO0SsWmPiXB+jikdf6SizrT5qKasx5j8ABbHpFTx+vFXp9EnYQmLx02h1QTTrl6eDqxLnGjporxl3NL3agEvXdT0WmEost648sQOYAeJS9Q7bfUVoMGnjo4AZdUMQku50McDcMWcBPvr0SzbTAFDfvJqwLzgxwATnCgnp4wDl6Aa+Ax283gghmj+vj7feE2KBBRMW3FzOpLOADl0Isb5587h/U4gGvkt5v60Z1VLG8BhYjbzRwyQZemwAd6cCR5/XFWLYZRIMpX39AR0tjaGGiGzLVyhse5C9RKC6ai42ppWPKiBagOvaYk8lO7DajerabOZP46Lby5wKjw1HCRx7p9sVMOWGzb/vA1hwiWc6jm3MvQDTogQkiqIhJV0nBQBTU+3okKCFDy9WwferkHjtxib7t3xIUQtHxnIwtx4mpg26/HfwVNVDb4oI9RHmx5WGelRVlrtiw43zboCLaxv46AZeB3IlTkwouebTr1y2NjSpHz68WNFjHvupy3q8TFn3Hos2IAk4Ju5dCo8B3wP7VPr/FGaKiG+T+v+TQqIrOqMTL1VdWV1DdmcbO8KXBz6esmYWYKPwDL5b5FA1a0hwapHiom0r/cKaoqr+27/XcrS5UwSMbQAAAABJRU5ErkJggg==)](https://deepwiki.com/daabr/versipellis)
[![Go Reference](https://pkg.go.dev/badge/github.com/daabr/versipellis.svg)](https://pkg.go.dev/github.com/daabr/versipellis)
[![Codecov](https://codecov.io/gh/daabr/versipellis/graph/badge.svg?token=IZFQXL47EM)](https://codecov.io/gh/daabr/versipellis)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/14316/badge)](https://www.bestpractices.dev/projects/14316)
[![OpenSSF Scorecard](https://img.shields.io/badge/dynamic/json?url=https%3A%2F%2Fapi.scorecard.dev%2Fprojects%2Fgithub.com%2Fdaabr%2Fversipellis&query=%24.score&label=openssf%20scorecard&color=007d9c)](https://scorecard.dev/viewer/?uri=github.com/daabr/versipellis)
<!--
https://api.scorecard.dev/projects/github.com/daabr/versipellis/badge
still renders "invalid repo path" instead of the actual score,
using a workaround until OpenSSF indexes this repo correctly.
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
