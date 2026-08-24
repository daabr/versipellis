# Versipellis - Data Flow Shape Shifter

[![Code Wiki](https://img.shields.io/badge/Code_Wiki-gold?logo=googlegemini)](https://codewiki.google/github.com/daabr/versipellis)
[![Go Reference](https://pkg.go.dev/badge/github.com/daabr/versipellis.svg)](https://pkg.go.dev/github.com/daabr/versipellis)
[![Codecov](https://codecov.io/gh/daabr/versipellis/graph/badge.svg?token=IZFQXL47EM)](https://codecov.io/gh/daabr/versipellis)

Versipellis is a versatile, scalable tool for transferring and transforming data reliably across diverse media, protocols, and formats, without altering the data itself.

It is not a data pipeline, but rather a powerful yet easy-to-use conduit for pipeline inputs and outputs.

In other words: ["Let's take all this data, and push it somewhere else."](https://knowyourmeme.com/memes/push-it-somewhere-else-patrick)

![Let's take all this data, and push it somewhere else](./images/patrick_meme.png)

## Getting Started

Choose any of the options below:

### Install Option 1: Docker Image

<https://github.com/daabr/versipellis/pkgs/container/versipellis>

Command line example:

```shell
docker run -d --name my-versi-container \
       -v $HOME/versi/config:/app/config -v $HOME/versi/data:/app/data \
       ghcr.io/daabr/versipellis:latest
```

- Platforms: Linux (amd64/arm64)
- Volumes / bind mounts:
  - `/app/config` - for `.toml` files, etc.
  - `/app/data` - for runtime storage of input data

### Install Option 2: Precompiled Executable Binary

<https://github.com/daabr/versipellis/releases/latest>

- Platforms:
  - Linux (amd64/arm64)
  - macOS (amd64/arm64)
  - Windows (amd64 only)

### Install Option 3: Build From Source

Required Go version: [1.27](https://go.dev/dl/)

Command line:

```shell
CGO_ENABLED=0 go build ./cmd/versi

./versi -h
```

## Supported Inputs

### SQL Input

Supported drivers for SQL-based relational databases:

- CockroachDB
- MariaDB
- Microsoft SQL Server
- MySQL
- PostgreSQL
- SAP HANA
- Snowflake
- SQLite
