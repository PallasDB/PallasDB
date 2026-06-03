<img width="1280" height="320" alt="PallasDB-banner" src="https://github.com/user-attachments/assets/9628a214-8a86-40b2-a0e6-77afd49de060" />

<h1 align="center">PallasDB</h1>

<p align="center">
A key-value database in Go using LSM trees, SSTables, write-ahead logging, concurrent access, gRPC, and Raft replication.
</p>

<p align="center">
  <a href="https://github.com/teddymalhan/PallasDB/actions/workflows/lint.yml">
  <img src="https://img.shields.io/github/actions/workflow/status/teddymalhan/PallasDB/lint.yml?branch=main&style=for-the-badge&label=Lint" alt="CI status" />
</a>
  <a href="https://github.com/teddymalhan/PallasDB/actions/workflows/test.yml">
  <img src="https://img.shields.io/github/actions/workflow/status/teddymalhan/PallasDB/test.yml?branch=main&style=for-the-badge&label=Tests" alt="Tests status" />
</a>
  <a href="https://github.com/teddymalhan/PallasDB/actions/workflows/security.yml">
  <img src="https://img.shields.io/github/actions/workflow/status/teddymalhan/PallasDB/security.yml?branch=main&style=for-the-badge&label=Security" alt="Security status" />
</a>
  <a href="https://github.com/teddymalhan/PallasDB/blob/main/LICENSE">
    <img src="https://img.shields.io/github/license/teddymalhan/PallasDB?style=for-the-badge" alt="MIT license" />
  </a>
  <a href="https://github.com/teddymalhan/PallasDB/blob/main/package.json">
    <img src="https://img.shields.io/badge/Go-00ADD8?logo=Go&logoColor=white&style=for-the-badge" alt="Go" />
  </a>
</p>


## Background

This project builds a small database engine from first principles: raw byte encoding, crash-safe persistence, sorted string tables, multi-level merging, a SQL parser, expression evaluation, and a replicated Raft-backed server mode.

The storage engine lives in `db/`. Cluster/Raft glue lives in `cluster/`. Public KV APIs and cluster-management APIs live in the `grpc/` protobuf/gRPC transport. Generated protobuf code lives in `pb/`.

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

### Configuration

PallasDB can read configuration from CLI flags, environment variables, and a YAML config file. Precedence is:

```text
flags > environment variables > config file > defaults
```

Use an explicit config file:

```sh
go run ./cmd/pallasdb --config ./pallasdb.example.yaml serve grpc
```

Environment variables use the `PALLASDB_` prefix and replace dots/dashes with underscores:

```sh
PALLASDB_LOG_FORMAT=json \
PALLASDB_SERVE_GRPC_ADDR=:50052 \
go run ./cmd/pallasdb serve grpc
```

Common keys:

| Config key | Environment variable |
|---|---|
| `log.format` | `PALLASDB_LOG_FORMAT` |
| `shutdown.timeout` | `PALLASDB_SHUTDOWN_TIMEOUT` |
| `local.data_dir` | `PALLASDB_LOCAL_DATA_DIR` |
| `serve.grpc.addr` | `PALLASDB_SERVE_GRPC_ADDR` |
| `serve.grpc.data_dir` | `PALLASDB_SERVE_GRPC_DATA_DIR` |
| `cluster.grpc_addr` | `PALLASDB_CLUSTER_GRPC_ADDR` |
| `cluster.data_dir` | `PALLASDB_CLUSTER_DATA_DIR` |
| `cluster.raft_addr` | `PALLASDB_CLUSTER_RAFT_ADDR` |
| `cluster.raft_dir` | `PALLASDB_CLUSTER_RAFT_DIR` |
| `cluster.node_id` | `PALLASDB_CLUSTER_NODE_ID` |
| `cluster.join` | `PALLASDB_CLUSTER_JOIN` |
| `cluster.apply_timeout` | `PALLASDB_CLUSTER_APPLY_TIMEOUT` |
| `cluster.serf.enabled` | `PALLASDB_CLUSTER_SERF_ENABLED` |
| `cluster.serf.addr` | `PALLASDB_CLUSTER_SERF_ADDR` |
| `cluster.serf.advertise_addr` | `PALLASDB_CLUSTER_SERF_ADVERTISE_ADDR` |
| `cluster.serf.join` | `PALLASDB_CLUSTER_SERF_JOIN` |
| `cluster.serf.event_buffer` | `PALLASDB_CLUSTER_SERF_EVENT_BUFFER` |

See [`pallasdb.example.yaml`](pallasdb.example.yaml) for a full example.

### Local key-value operations

```sh
go run ./cmd/pallasdb local put hello world --data-dir ./data
go run ./cmd/pallasdb local get hello --data-dir ./data
go run ./cmd/pallasdb local range a z --data-dir ./data
go run ./cmd/pallasdb local delete hello --data-dir ./data
go run ./cmd/pallasdb local compact --data-dir ./data
```

### Local benchmark

PallasDB includes a disk-backed benchmark runner for local stores:

```sh
go run ./cmd/pallasdb local benchmark \
  --data-dir "$HOME/bench-data/pallasdb-m4/v128" \
  --reset --keys 2000000 --value-size 128 --batch-size 1000 \
  --read-ops 500000 --compact \
  --format text --output benchmarks/m4-macbook-air-results.txt
```

Benchmark artifacts from the Apple M4 MacBook Air run are checked in:

- Plan: [`benchmarks/PallasDB-M4-Benchmark.md`](benchmarks/PallasDB-M4-Benchmark.md)
- Results: [`benchmarks/m4-macbook-air-results.txt`](benchmarks/m4-macbook-air-results.txt)
- Optimization notes: [`benchmarks/performance-optimizations-2026-06-03.md`](benchmarks/performance-optimizations-2026-06-03.md)

Verification commands:

```sh
go test ./cmd/pallasdb -run 'TestLocalBenchmark'
go test ./db ./cmd/pallasdb
```

Both passed for the benchmark implementation.

Commands run for the recorded M4 benchmark:

```sh
go run ./cmd/pallasdb local benchmark --data-dir "$HOME/bench-data/pallasdb-m4/smoke" --reset --keys 20000 --value-size 128 --batch-size 500 --read-ops 20000 --scan-limit 20000 --compact --format text --output benchmarks/m4-macbook-air-results.txt

go run ./cmd/pallasdb local benchmark --data-dir "$HOME/bench-data/pallasdb-m4/v128" --reset --keys 2000000 --value-size 128 --batch-size 1000 --read-ops 500000 --compact --format text --output benchmarks/m4-macbook-air-results.txt

go run ./cmd/pallasdb local benchmark --data-dir "$HOME/bench-data/pallasdb-m4/v1024" --reset --keys 500000 --value-size 1024 --batch-size 1000 --read-ops 300000 --compact --format text --output benchmarks/m4-macbook-air-results.txt

go run ./cmd/pallasdb local benchmark --data-dir "$HOME/bench-data/pallasdb-m4/v16384" --reset --keys 50000 --value-size 16384 --batch-size 250 --read-ops 100000 --compact --format text --output benchmarks/m4-macbook-air-results.txt
```

Summary:

| Value size | Keys | Populate ops/sec | Random read ops/sec | Iterate values ops/sec | Data dir size |
|---:|---:|---:|---:|---:|---:|
| 128 B | 2,000,000 | 19,993.37 | 26,588.23 | 974,720.76 | 324,396,366 B |
| 1 KiB | 500,000 | 43,362.86 | 33,529.22 | 732,912.96 | 529,099,166 B |
| 16 KiB | 50,000 | 12,281.01 | 5,364.19 | 141,650.58 | 820,910,006 B |

Correctness notes:

- Final smoke and scaled runs reported `missing: 0` and `errors: 0`.
- Smoke run verified expected counts: populate `20000`, random read found `20000`, iteration `20000`.
- Initial smoke attempt failed because the parent benchmark data directory did not exist; the benchmark runner now creates the benchmark data directory before opening PallasDB, and the benchmark was rerun successfully.

Recent storage-engine optimization results:

| Workload | Before | After | Change |
|---|---:|---:|---:|
| 16 KiB random read latency | 32,442.98 ns/op | 17,044.86 ns/op | 47.5% lower |
| 16 KiB random read throughput | 30,823.31 ops/sec | 58,668.70 ops/sec | 90.3% higher |
| 16 KiB random read total allocation | 2,282,052,872 B | 188,000,376 B | 91.8% lower |
| 16 KiB random read GC count | 775 | 56 | 92.8% lower |

The optimized code avoids reading SSTable values during binary-search comparisons and appends ordered write batches directly to the memtable when no snapshot requires the merge path. See the optimization notes for commands, environment, and verification output.

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
  --serf-addr :7946 \
  --data-dir ./data/node-1 \
  --raft-dir ./raft/node-1
```

Join another node through Serf gossip discovery:

```sh
go run ./cmd/pallasdb cluster start \
  --node-id node-2 \
  --grpc-addr :50052 \
  --raft-addr :7002 \
  --serf-addr :7947 \
  --serf-join localhost:7946 \
  --data-dir ./data/node-2 \
  --raft-dir ./raft/node-2
```

The older explicit gRPC join path is still available with `--join localhost:50051`; use `--serf-enabled=false` if you do not want the node to bind a Serf gossip port.

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
