![Versipellis - The Data Flow Shape Shifter](./images/banner.jpg)

# Versipellis - The Data Flow Shape Shifter

<!-- Waiting for Google Gemini to index this repo, so the link won't lead to a 404.
[![Code Wiki](https://img.shields.io/badge/Gemini-code_wiki-007d9c?logo=googlegemini)](https://codewiki.google/github.com/needful-tee/versipellis)
-->
[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/needful-tee/versipellis)
[![Go Reference](https://pkg.go.dev/badge/needful-t.ee/versipellis.svg)](https://pkg.go.dev/needful-t.ee/versipellis)
[![Codecov](https://codecov.io/gh/needful-tee/versipellis/graph/badge.svg?token=9kJ6rPfqMS)](https://codecov.io/gh/needful-tee/versipellis)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/14316/badge)](https://www.bestpractices.dev/projects/14316)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/needful-tee/versipellis/badge)](https://scorecard.dev/viewer/?uri=github.com/needful-tee/versipellis)
[![SLSA 3](https://slsa.dev/images/gh-badge-level3.svg)](https://slsa.dev)

Versipellis (`versi`) is a versatile, scalable tool for transferring and transforming data reliably across services, networks, protocols and formats, without altering the data itself.

It is an easy-to-use adapter and conduit around data pipelines, sources, and sinks. It can move and restructure data of any shape or size, with adjustable security in transit and at rest - while maintaining strong safeguards against corruption and loss, or latency and crashes under heavy load.

In other words: ["We should take all this data and push it somewhere else!"](https://knowyourmeme.com/memes/push-it-somewhere-else-patrick)

[![Let's take all this data, and push it somewhere else](./images/patrick_meme.png)](https://knowyourmeme.com/memes/push-it-somewhere-else-patrick)

The primary design principles of this project are: ease of use, efficiency at scale, security, low footprint, and maintainability.

## Getting Started

1. Choose any of the installation options below
2. [Quickstart demos & tutorials](./docs/tutorials/README.md)
3. [Configuration reference](./docs/config/README.md)

### Installation Option 1: Docker Image

<https://github.com/needful-tee/versipellis/pkgs/container/versipellis>

Command line example:

```shell
docker run -d --name my-versi-container \
       -v $HOME/versi/config:/app/config -v $HOME/versi/data:/app/data \
       ghcr.io/needful-tee/versipellis:latest
```

- Platforms: Linux (amd64/arm64)
- Volumes / mount points:
  - `/app/config` - app and database configuration files, TLS certificates
  - `/app/data` - [dead-letter queue](./docs/dead_letter_queue.md) (data that was received but not delivered)
- Already bundled and tested with:
  - Oracle Instant Client
  - unixODBC

### Installation Option 2: Precompiled Executable Binary

<https://github.com/needful-tee/versipellis/releases/latest>

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

### HTTP Input

- Protocols: HTTP/1.1 and HTTP/2 (TCP), HTTP/3 (QUIC/UDP)
- Directions: pull (retrieve from servers), push (receive from clients)
- Encryption: none, TLS, mTLS
- Content encoding: `identity` (none), `gzip`
- [Coming soon](./docs/roadmap.md): Authentication and authorization (pull only):  
  username + password (RFC 7617), static keys/tokens, OAuth 2, JWT

### SQL Input

Supported SQL-based databases:

- CockroachDB
- MariaDB
- Microsoft SQL Server
- MySQL
- ODBC
- Oracle Database
- PostgreSQL
- SAP HANA
- Snowflake
- SQLite

## Supported Outputs

### Zero Config

- Stdout
- Local filesystem ([dead-letter queue](./docs/dead_letter_queue.md))

### HTTP Output

- Directions: push (send to servers)
- Same feature set as [HTTP input](#http-input) above
  - [Coming soon](./docs/roadmap.md): Including authentication and authorization
  - Not including content encoding, i.e. compression (yet)
