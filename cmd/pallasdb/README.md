# pallasdb CLI

[`pallasdb`](../../README.md) is the single binary for local key-value operations, remote key-value and SQL access to a running server, whole-keyspace backup and restore, standalone gRPC serving, Raft cluster nodes, shell completions, and version output.

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
| `cache.enabled` | `PALLASDB_CACHE_ENABLED` |
| `cache.max_cost_bytes` | `PALLASDB_CACHE_MAX_COST_BYTES` |
| `cache.num_counters` | `PALLASDB_CACHE_NUM_COUNTERS` |
| `client.addr` | `PALLASDB_CLIENT_ADDR` |
| `client.timeout` | `PALLASDB_CLIENT_TIMEOUT` |
| `client.max_redirects` | `PALLASDB_CLIENT_MAX_REDIRECTS` |

See [`../../pallasdb.example.yaml`](../../pallasdb.example.yaml) for a full example.

Unknown keys are rejected rather than ignored, so a typo like `serve.grpc.adr`
fails at startup instead of silently falling back to the default. The resolved
configuration is also validated before any command runs: addresses must parse,
timeouts must be positive, `log.format` must be `text` or `json`, and
`cache.num_counters` must not exceed `cache.max_cost_bytes`.

## Local key-value operations

Local commands operate directly on the embedded [`db`](../../db/) storage engine.

```sh
go run ./cmd/pallasdb local put hello world --data-dir ./data
go run ./cmd/pallasdb local get hello --data-dir ./data
go run ./cmd/pallasdb local range a z --data-dir ./data
go run ./cmd/pallasdb local delete hello --data-dir ./data
go run ./cmd/pallasdb local compact --data-dir ./data
```

## Remote key-value operations

`kv *` mirrors `local *` but talks to a running server over gRPC through the
[`client`](../../client/) package instead of opening the data directory
in-process. Against a cluster it follows the Raft leader automatically: a
follower answers `Unavailable`, the client asks for the leader's gRPC address
and retries there, bounded by `--max-redirects`.

```sh
go run ./cmd/pallasdb kv put hello world --addr 127.0.0.1:50051
go run ./cmd/pallasdb kv put hello world --mode insert   # insert, update or upsert
go run ./cmd/pallasdb kv get hello --consistency stale   # default, linearizable or stale
go run ./cmd/pallasdb kv range a z --limit 100 --keys-only
go run ./cmd/pallasdb kv delete hello
```

A scan is capped by the server and truncated silently, so resume a partial scan
by re-running with `START` set to the last key printed. `--descending` needs an
explicit `START`, which is the upper bound it walks down from.

## SQL

`sql` runs a statement against a running server and renders the result. With no
statement it opens a REPL; `\q` quits, `\?` lists the commands, Ctrl-C abandons
the statement in progress rather than the session, and Ctrl-D ends it.

```sh
go run ./cmd/pallasdb sql "SELECT id, name FROM users" --addr 127.0.0.1:50051
go run ./cmd/pallasdb sql "SELECT id FROM users" --format json
go run ./cmd/pallasdb sql            # interactive
```

```text
+----+-------+
| id | name  |
+----+-------+
|  1 | alpha |
|  2 | bravo |
+----+-------+
(2 rows)
```

## Backup and restore

`dump` exports the whole keyspace to a length-prefixed, CRC-checked stream and
`restore` loads one back. Both work against a server with `--addr` or against a
data directory with `--data-dir`, so they double as an import/export path
between a local database and a cluster. `dump` resumes across the server's scan
cap, so the export is complete however large the keyspace is.

```sh
go run ./cmd/pallasdb dump --addr 127.0.0.1:50051 --output backup.pallas
go run ./cmd/pallasdb restore --data-dir ./restored --input backup.pallas
go run ./cmd/pallasdb dump --data-dir ./data | \
  go run ./cmd/pallasdb restore --addr 127.0.0.1:50051
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
