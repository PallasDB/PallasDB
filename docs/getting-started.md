# Getting started

## Install

**From source** (Go 1.25 or later):

```sh
git clone https://github.com/teddymalhan/pallasdb.git
cd pallasdb
make build      # -> bin/pallasdb, with version/commit/date stamped in
./bin/pallasdb version
```

**From a release archive** — download the `darwin`/`linux` `amd64`/`arm64`
tarball from the [releases
page](https://github.com/teddymalhan/pallasdb/releases), verify it against
`checksums.txt`, and put `pallasdb` on your `PATH`.

**Container**:

```sh
docker run --rm -v pallasdb-data:/data -p 50051:50051 \
  ghcr.io/teddymalhan/pallasdb:latest
```

The image is a static binary on `scratch` running as uid 65532, with `/data` as
the default data directory and `:50051` as the default gRPC address.

## Operate a local data directory

No server involved — these commands open the directory directly, so nothing
else may have it open at the same time.

```sh
pallasdb local put hello world --data-dir ./data
pallasdb local get hello --data-dir ./data
pallasdb local range --start a --end z --data-dir ./data
pallasdb local delete hello --data-dir ./data
pallasdb local compact --data-dir ./data
```

`pallasdb local benchmark` runs the disk-backed benchmark; see
[`benchmarks/README.md`](../benchmarks/README.md).

## Run a server

```sh
pallasdb serve grpc --addr :50051 --data-dir ./data
```

That serves `KVService`, `SQLService`, and the standard gRPC health service on
one port. Talk to it with the Go client in [`client/`](../client/), with
`pallasdb kv ...` / `pallasdb sql ...`, or with any gRPC tool pointed at
[`proto/pallasdb/v1/kv.proto`](../proto/pallasdb/v1/kv.proto).

## Run SQL

```sh
pallasdb sql "CREATE TABLE t (a string, b int64, PRIMARY KEY (b));"
pallasdb sql "INSERT INTO t VALUES ('hi', 1);"
pallasdb sql "SELECT * FROM t WHERE b = 1;"
pallasdb sql            # interactive REPL
```

The implemented SQL subset is documented in [SQL surface](sql.md). It is small
and honest about its limits: no joins, no aggregates, no `ORDER BY`.

## Run a cluster

```sh
pallasdb cluster start \
  --node-id node-1 --grpc-addr :50051 --raft-addr :7001 --serf-addr :7946 \
  --data-dir ./data/node-1 --raft-dir ./raft/node-1
```

Full membership, discovery, TLS, and bootstrap options are in
[`cluster/README.md`](../cluster/README.md).

## Configuration

Every flag can come from a config file or the environment instead.
[`pallasdb.example.yaml`](../pallasdb.example.yaml) is the annotated reference:

```sh
pallasdb --config ./pallasdb.yaml serve grpc
```

Configuration is decoded into a typed struct and validated, so an unknown key or
an unparseable value fails at startup rather than silently falling back to a
default.

## Next

- [Architecture](architecture.md) — how the layers fit together.
- [Durability model](durability.md) — what survives a crash, per platform.
- [`CONTRIBUTING.md`](../CONTRIBUTING.md) — build, test, lint, and release.
