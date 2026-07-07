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

PallasDB is a small database engine built from first principles: raw byte encoding, crash-safe persistence, sorted string tables, multi-level merging, a SQL parser, expression evaluation, a gRPC API, and Raft-backed server mode.

## Quick start

Requires Go 1.25 or later.

```sh
git clone https://github.com/teddymalhan/pallasdb.git
cd pallasdb
go mod download
go build -o pallasdb ./cmd/pallasdb
./pallasdb --help
```

Run tests before submitting changes:

```sh
go test ./...
```

## Repository map

| Area | README | Detailed docs |
|---|---|---|
| CLI, configuration, local commands | [`cmd/pallasdb/`](cmd/pallasdb/) | [Getting Started](https://pallasdb.github.io/docs/getting-started.html) |
| Storage engine, SQL layer, Go API | [`db/`](db/) | [Storage Engine](https://pallasdb.github.io/docs/storage/index.html) and [Go API](https://pallasdb.github.io/docs/go-api.html) |
| gRPC server implementation | [`grpc/`](grpc/) | [gRPC API](https://pallasdb.github.io/docs/grpc-api.html) |
| Raft cluster and Serf discovery | [`cluster/`](cluster/) | [Cluster & Raft](https://pallasdb.github.io/docs/cluster.html) |
| Protobuf definitions and code generation | [`proto/`](proto/) | [gRPC API](https://pallasdb.github.io/docs/grpc-api.html) |
| Benchmark artifacts and commands | [`benchmarks/`](benchmarks/) | [Benchmarks](https://pallasdb.github.io/docs/benchmarks.html) |

## Maintainers

[@teddymalhan](https://github.com/teddymalhan)

## Contributing

Questions and feedback are welcome via [GitHub Issues](https://github.com/teddymalhan/pallasdb/issues). Start with [`cmd/pallasdb/README.md`](cmd/pallasdb/README.md) for local setup and CLI usage.

## License

[Apache-2](LICENSE) © Teddy Malhan
