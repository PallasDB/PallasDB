# pallasdb CLI

[`pallasdb`](../../README.md) is the single binary for local key-value operations, standalone gRPC serving, Raft cluster nodes, shell completions, and version output.

Detailed user documentation lives in [Getting Started](https://pallasdb.github.io/docs/getting-started.html).

## Build

Requires Go 1.25 or later.

```sh
go build -o pallasdb ./cmd/pallasdb
./pallasdb --help
```

## Configuration

Configuration can come from CLI flags, environment variables, or a YAML config file. Precedence is:

```text
flags > environment variables > config file > defaults
```

Use an explicit config file:

```sh
go run ./cmd/pallasdb --config ./pallasdb.example.yaml serve grpc
```

Environment variables use the `PALLASDB_` prefix and replace dots and dashes with underscores:

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

See [`../../pallasdb.example.yaml`](../../pallasdb.example.yaml) for a full example.

## Local key-value operations

Local commands operate directly on the embedded [`db`](../../db/) storage engine.

```sh
go run ./cmd/pallasdb local put hello world --data-dir ./data
go run ./cmd/pallasdb local get hello --data-dir ./data
go run ./cmd/pallasdb local range a z --data-dir ./data
go run ./cmd/pallasdb local delete hello --data-dir ./data
go run ./cmd/pallasdb local compact --data-dir ./data
```

## Local benchmark

The CLI includes a disk-backed benchmark runner. Benchmark artifacts and recorded results are in [`../../benchmarks/`](../../benchmarks/).

```sh
go run ./cmd/pallasdb local benchmark \
  --data-dir "$HOME/bench-data/pallasdb-m4/v128" \
  --reset --keys 2000000 --value-size 128 --batch-size 1000 \
  --read-ops 500000 --compact \
  --format text --output benchmarks/m4-macbook-air-results.txt
```

Detailed benchmark docs: [Benchmarks](https://pallasdb.github.io/docs/benchmarks.html).

## Standalone gRPC server

```sh
go run ./cmd/pallasdb serve grpc --addr :50051 --data-dir ./data
```

Server implementation details are in [`../../grpc/`](../../grpc/) and [gRPC API](https://pallasdb.github.io/docs/grpc-api.html).

## Raft-backed cluster node

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

The explicit gRPC join path is still available with `--join localhost:50051`; use `--serf-enabled=false` if the node should not bind a Serf gossip port.

Cluster implementation details are in [`../../cluster/`](../../cluster/) and [Cluster & Raft](https://pallasdb.github.io/docs/cluster.html).

## Shell completions

```sh
go run ./cmd/pallasdb completion bash
go run ./cmd/pallasdb completion zsh
go run ./cmd/pallasdb completion fish
go run ./cmd/pallasdb completion powershell
```

## Verification

```sh
go test ./cmd/pallasdb
```
