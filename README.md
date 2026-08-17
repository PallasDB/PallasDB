<img width="1280" height="320" alt="PallasDB-banner (1)" src="https://github.com/user-attachments/assets/d4c80e9e-2673-4e89-b914-54a8280a3eaf" />

<h1 align="center">PallasDB</h1>

<p align="center">
A key-value database in Go using LSM trees, SSTables, write-ahead logging, concurrent access, gRPC, and Raft replication.
</p>

<p align="center">
  <a href="https://github.com/PallasDB/PallasDB/actions/workflows/lint.yml">
    <img src="https://img.shields.io/github/actions/workflow/status/PallasDB/PallasDB/lint.yml?branch=main&style=for-the-badge&label=Lint" alt="CI status" />
  </a>
  <a href="https://github.com/PallasDB/PallasDB/actions/workflows/test.yml">
    <img src="https://img.shields.io/github/actions/workflow/status/PallasDB/PallasDB/test.yml?branch=main&style=for-the-badge&label=Tests" alt="Tests status" />
  </a>
  <a href="https://github.com/PallasDB/PallasDB/actions/workflows/security.yml">
    <img src="https://img.shields.io/github/actions/workflow/status/PallasDB/PallasDB/security.yml?branch=main&style=for-the-badge&label=Security" alt="Security status" />
  </a>
  <a href="LICENSE">
    <img src="https://img.shields.io/github/license/PallasDB/PallasDB?style=for-the-badge" alt="Apache-2.0 license" />
  </a>
  <a href="go.mod">
    <img src="https://img.shields.io/badge/Go-00ADD8?logo=Go&logoColor=white&style=for-the-badge" alt="Go" />
  </a>
</p>

## Background

PallasDB is a small database engine built from first principles: raw byte
encoding, a write-ahead log, sorted string tables, multi-level merging,
transactions, a table layer with a small SQL dialect, a gRPC API, and
Raft-backed replication.

"Small SQL dialect" is meant literally — `CREATE TABLE`, `INSERT`, `SELECT`,
`UPDATE`, `DELETE`, `DROP TABLE`, with `WHERE`, `LIMIT`/`OFFSET`, and tuple
comparisons for index range scans. No joins, no aggregates, no `ORDER BY`. The
full surface is in [`docs/sql.md`](docs/sql.md).

## Quick start

Requires Go 1.25 or later.

```sh
git clone https://github.com/teddymalhan/pallasdb.git
cd pallasdb
make build
./bin/pallasdb --help
```

Or pull the container image:

```sh
docker run --rm -v pallasdb-data:/data -p 50051:50051 \
  ghcr.io/teddymalhan/pallasdb:latest
```

[`docs/getting-started.md`](docs/getting-started.md) walks through a local data
directory, a server, SQL, and a cluster.

`make` lists every target. Before submitting changes:

```sh
make check    # lint + race tests: what CI gates on
```

## Documentation

Documentation lives in this repository, next to the code it describes, so it is
reviewed with the change that makes it true.

| Page | Covers |
|---|---|
| [Getting started](docs/getting-started.md) | Install, first statements, first cluster |
| [Architecture](docs/architecture.md) | How the layers stack and where a request goes |
| [SQL surface](docs/sql.md) | The SQL subset PallasDB implements, and what it does not |
| [Durability model](docs/durability.md) | What survives a crash, and what differs per platform |

## Repository map

| Area | README |
|---|---|
| CLI, configuration, local commands | [`cmd/pallasdb/`](cmd/pallasdb/README.md) |
| Storage engine, SQL layer, Go API | [`db/`](db/README.md) |
| gRPC server implementation | [`grpc/`](grpc/README.md) |
| Raft cluster and Serf discovery | [`cluster/`](cluster/README.md) |
| Protobuf definitions and code generation | [`proto/`](proto/README.md) |
| Benchmark artifacts and commands | [`benchmarks/`](benchmarks/README.md) |

## Maintainers

[@teddymalhan](https://github.com/teddymalhan)

## Contributing

[`CONTRIBUTING.md`](CONTRIBUTING.md) covers the toolchain, the `make` targets,
what CI enforces, and the release process. Questions and feedback are welcome
via [GitHub Issues](https://github.com/teddymalhan/pallasdb/issues).

Release notes are in [`CHANGELOG.md`](CHANGELOG.md).

## License

[Apache-2](LICENSE) © Teddy Malhan
