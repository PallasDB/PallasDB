# Cluster and Raft

[`cluster`](../cluster/) provides leader-backed replicated writes using [HashiCorp Raft](https://github.com/hashicorp/raft). Member discovery uses [Serf](https://github.com/hashicorp/serf) gossip through [`discovery/`](discovery/).

In-repo docs: [Architecture](../docs/architecture.md) covers the replication
model; [Durability model](../docs/durability.md) covers what a committed write
guarantees.

## Runtime model

- Mutating gRPC operations are encoded as Raft commands before they reach the storage engine.
- The leader applies committed commands to the FSM, which wraps [`db.KV`](../db/).
- Reads can be served from local FSM state; writes require Raft consensus.
- Snapshots stream all key-value pairs and restore by atomically swapping the storage directory.

## Start a cluster

Bootstrap the first node with the [`pallasdb` CLI](../cmd/pallasdb/):

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

Join explicitly through gRPC when gossip is disabled:

```sh
go run ./cmd/pallasdb cluster start \
  --node-id node-2 \
  --grpc-addr :50052 \
  --raft-addr :7002 \
  --serf-enabled=false \
  --join localhost:50051 \
  --data-dir ./data/node-2 \
  --raft-dir ./raft/node-2
```

## Important files

| File | Role |
|---|---|
| [`node.go`](node.go) | Raft node lifecycle, bootstrap, join, shutdown, discovery wiring. |
| [`fsm.go`](fsm.go) | Raft FSM applying `Put` and `Delete` commands to `db.KV`. |
| [`fsm_snapshot.go`](fsm_snapshot.go) | Raft snapshot persist and restore logic. |
| [`command.go`](command.go) | Command encoding shared by cluster gRPC and FSM. |
| [`discovery/serf.go`](discovery/serf.go) | Serf membership, metadata, and discovery events. |

## Related folders

- [`../grpc/`](../grpc/) exposes cluster membership and leader APIs.
- [`../db/`](../db/) stores FSM state.
- [`../cmd/pallasdb/`](../cmd/pallasdb/) owns cluster startup flags and config loading.

## Verification

```sh
go test -race ./cluster ./cluster/discovery
go test ./cmd/pallasdb -run 'Cluster|Serf'
```

The `cluster` tests cover FSM apply and snapshot/restore, membership lifecycle
(bootstrap, non-voter promotion, leave, failure eviction), and node shutdown.
`cluster/discovery` covers Serf membership events and metadata. The
`cmd/pallasdb` filter covers cluster flag parsing and config loading, which is
where a cluster is actually assembled from user input.
