# PallasDB

PallasDB is a key-value database in Go using LSM trees, SSTables, write-ahead logging, concurrent access, gRPC, and Raft replication.

The name references Pallas Athena: wisdom, strategy, and technical craft.

## Background

This project builds a small database engine from first principles: raw byte encoding, crash-safe persistence, sorted string tables, multi-level merging, a SQL parser, expression evaluation, and a replicated Raft-backed server mode.

The storage engine lives in `db/`. Cluster/Raft glue lives in `cluster/`. The public gRPC transport lives in `grpc/`. Generated protobuf code lives in `pb/`.

## Features

- **Cells and Rows**: Binary encoding of typed values (`i64`, `str`) into byte slices.
- **Write-Ahead Log (WAL)**: Crash-safe persistence with fsync and CRC32 checksums.
- **Sorted KV Store**: In-memory sorted key-value store with binary search.
- **SSTables**: Persistent sorted string tables for durable storage.
- **LSM Merging**: Multi-level merge iterators that unify sorted streams across levels.
- **Metadata management**: Double-buffered metadata files tracking SSTable versions.
- **SQL Parser**: Tokenizer and recursive-descent parser for `CREATE TABLE`, `SELECT`, `INSERT`, `UPDATE`, and `DELETE`.
- **Expression Evaluator**: Arithmetic, comparison, and logical operators for `WHERE` clause evaluation.
- **gRPC API**: Key-value `Get`, `Put`, `Delete`, and streaming `Range` RPCs.
- **Raft mode**: Leader-backed replicated writes using HashiCorp Raft.
- **Unified CLI**: A single `pallasdb` binary for local operations, server startup, cluster startup, completions, and version output.

## Install

Requires Go 1.25 or later.

```sh
git clone https://github.com/teddymalhan/pallasdb.git
cd pallasdb
go mod download
go build -o pallasdb ./cmd/pallasdb
```

## Usage

Run all tests:

```sh
go test ./...
```

Show CLI help:

```sh
go run ./cmd/pallasdb --help
```

### Local key-value operations

```sh
go run ./cmd/pallasdb local put hello world --data-dir ./data
go run ./cmd/pallasdb local get hello --data-dir ./data
go run ./cmd/pallasdb local range a z --data-dir ./data
go run ./cmd/pallasdb local delete hello --data-dir ./data
go run ./cmd/pallasdb local compact --data-dir ./data
```

### Single-node gRPC server

```sh
go run ./cmd/pallasdb serve grpc --addr :50051 --data-dir ./data
```

### Raft-backed cluster node

Bootstrap the first node:

```sh
go run ./cmd/pallasdb cluster start \
  --node-id node-1 \
  --grpc-addr :50051 \
  --raft-addr :7001 \
  --http-addr :8001 \
  --data-dir ./data/node-1 \
  --raft-dir ./raft/node-1
```

Join another node through the first node's HTTP management endpoint:

```sh
go run ./cmd/pallasdb cluster start \
  --node-id node-2 \
  --grpc-addr :50052 \
  --raft-addr :7002 \
  --http-addr :8002 \
  --data-dir ./data/node-2 \
  --raft-dir ./raft/node-2 \
  --join localhost:8001
```

### Shell completions

```sh
go run ./cmd/pallasdb completion bash
go run ./cmd/pallasdb completion zsh
go run ./cmd/pallasdb completion fish
go run ./cmd/pallasdb completion powershell
```

## Protobuf/gRPC development

Regenerate protobuf/gRPC code:

```sh
go install github.com/bufbuild/buf/cmd/buf@latest
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
buf generate
```

Validate protobuf/gRPC and Go code:

```sh
buf lint
buf generate
go test ./...
```

## Go API

```go
import "github.com/teddymalhan/pallasdb/db"

kv, err := db.NewKV("path/to/data")
if err != nil {
    log.Fatal(err)
}
defer kv.Close()

_, err = kv.Set([]byte("hello"), []byte("world"))
```

| Symbol | Description |
|---|---|
| `NewKV(path string, opts ...KVOption) (*KV, error)` | Open or create a local key-value store. |
| `KV.Get(key)` | Fetch a value by key. |
| `KV.Set(key, value)` | Insert or update a key. |
| `KV.SetEx(key, value, mode)` | Insert, update, or upsert depending on mode. |
| `KV.Del(key)` | Delete a key. |
| `KV.NewTX()` | Open a transaction/snapshot. |
| `KV.Compact()` | Run compaction once. |
| `NewDB(path string, opts ...KVOption) (*DB, error)` | Open the higher-level table database API. |

## Maintainers

[@teddymalhan](https://github.com/teddymalhan)

## Contributing

Questions and feedback are welcome via [GitHub Issues](https://github.com/teddymalhan/pallasdb/issues).

Before submitting changes, run:

```sh
go test ./...
```

## License

[AGPL-3.0](LICENSE) © Teddy Malhan
