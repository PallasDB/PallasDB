# gRPC transport

[`grpc`](../grpc/) exposes the [`db`](../db/) key-value store, the SQL layer, and [`cluster`](../cluster/) management operations over Protocol Buffers/gRPC.

Detailed docs: [gRPC API](https://pallasdb.github.io/docs/grpc-api.html).

## Services

```protobuf
service KVService {
  rpc Get(GetRequest) returns (GetResponse);
  rpc Put(PutRequest) returns (PutResponse);
  rpc Delete(DeleteRequest) returns (DeleteResponse);
  rpc Range(RangeRequest) returns (stream RangeResponse);
}

service SQLService {
  rpc Query(QueryRequest) returns (stream QueryResponse);
}

service ClusterService {
  rpc Join(JoinRequest) returns (JoinResponse);
  rpc Leave(LeaveRequest) returns (LeaveResponse);
  rpc ListMembers(ListMembersRequest) returns (ListMembersResponse);
  rpc GetLeader(GetLeaderRequest) returns (GetLeaderResponse);
}
```

The source schema is in [`../proto/pallasdb/v1/kv.proto`](../proto/pallasdb/v1/kv.proto). Generated Go code lives in [`../pb/v1/`](../pb/v1/). Protobuf generation commands are in [`../proto/README.md`](../proto/README.md).

## Standalone server

Run through the [`pallasdb` CLI](../cmd/pallasdb/):

```sh
go run ./cmd/pallasdb serve grpc --addr :50051 --data-dir ./data
```

## Options

Both `NewGRPCServer(store, opts...)` and `NewClusterGRPCServer(node, applyTimeout, opts...)` accept PallasDB `Option` values and plain `grpc.ServerOption` values in the same list. Everything below is off by default except recovery and the transport limits, so a local single-node server needs no configuration.

| Option | Effect |
| --- | --- |
| `WithTLS(TLSConfig)` | Server TLS; add `ClientCAFile` + `RequireClientCert` for mTLS. Minimum TLS 1.2. A configuration that cannot be loaded fails closed: every handshake is rejected, never downgraded to plaintext. |
| `WithCredentials(creds)` | Transport credentials sourced elsewhere. |
| `WithAuthToken(secret)` | Gates `ClusterService` on `authorization: Bearer <secret>`. |
| `WithMTLSAuth(names...)` | Gates `ClusterService` on a verified client certificate; an empty list accepts any certificate the client CA vouches for. |
| `WithLogger(logger)` | Unary **and** stream logging through `log/slog`. |
| `WithMetrics(m)` | Prometheus RPC counters, latency and in-flight gauges. |
| `WithSQL(exec)` | Registers `SQLService`. |
| `WithReflection()` | Server reflection for `grpcurl`/`evans`. |
| `WithRecovery(bool)` | Panic recovery, on by default. |
| `WithMessageSize(recv, send)`, `WithMaxConcurrentStreams(n)`, `WithKeepalivePolicy(...)` | Transport limits. |
| `WithRangeBounds(limit, maxDuration)` | Caps rows per `Range` stream and how long it may hold a read transaction. |
| `WithContext(ctx)` | Bounds background goroutines (the cluster health watcher). |

## Semantics

- **Recovery.** Unary and stream interceptors convert a handler panic into `Internal` and log the stack. The storage layer asserts with panics on hot paths, and on the Raft apply path a panic is deterministic across every replica, so an uncaught one is a whole-cluster crash loop.
- **`Delete` is idempotent.** Deleting an absent key returns `deleted=false` instead of `NotFound`, so a retried delete succeeds.
- **`Range`.** An empty `start` scans from the first key and an empty `stop` scans to the last; both bounds are inclusive. `limit` caps the row count (the server default caps it further) and `keys_only` omits values. Truncation at the limit is silent — resume by re-issuing the scan with `start` set to the last key received. Exceeding the scan deadline ends the stream with `DeadlineExceeded`. A descending scan of the whole keyspace needs a start key: the storage layer has no seek-to-end.
- **Consistency (cluster server).** `LINEARIZABLE` (the default) confirms leadership with `VerifyLeader` and waits for the FSM to reach the current commit index — no log write, no fsync. `STALE` reads the local FSM and is servable by any node. Until the FSM exposes its applied index, `LINEARIZABLE` falls back to a Raft barrier.
- **Leader redirects.** A non-leader answers `Unavailable` naming the leader's **gRPC** address, both in the message and as an `ErrorInfo` detail with `reason=NOT_LEADER`. Use `LeaderFromError(err)` to read it. The Raft transport address is carried only as a hint; it does not speak gRPC.
- **Error mapping.** `ErrNotLeader`, `ErrLeadershipLost`, `ErrRaftShutdown` → `Unavailable` (a lost leadership may or may not have committed the write, so retries must be idempotent); `ErrEnqueueTimeout` → `DeadlineExceeded`; `ErrAbortedByRestore` → `Aborted`. A membership change that fails while no leader is known is `Unavailable` too: there is nobody to route it to, so retrying is the right move. Applies honour the client's context deadline, not just the server-wide apply timeout.
- **Health.** Status is registered per service name. In cluster mode a `LeaderCh` watcher drives `KVService` and `SQLService` to `NOT_SERVING` while the node has no leader; `ClusterService` stays serving because that is what a client calls to find the new leader.
- **SQL streaming contract.** `Query` sends exactly one header message carrying `columns` and no values, then one message per row carrying only `values`, then for a non-SELECT statement a final message carrying `rows_affected`.
- **Metrics.** `Metrics.Registry()` is a Prometheus registry the CLI can serve; `Metrics.CollectRaftMetrics(name)` folds Raft's go-metrics output into it (once per process).

## Implementation notes

- `KVServer` wraps `*db.KV`; `ClusterKVServer` routes writes through Raft and reads from the local FSM.
- `SQLServer` talks to an `SQLExecutor`/`SQLCursor` pair so the `db` package never imports protobuf; `sql_db.go` is the only file that touches db's SQL execution API, and `cellValue`/`columnDescriptors` are the only `db.Cell` to `pbv1.Value` conversion.
- A few `cluster.Node`/`cluster.FSM` accessors are probed through interfaces (`cluster_node.go`) so this package builds and behaves correctly whether or not the cluster-side accessors are present; each probe falls back to today's behaviour.
- `Serve(ctx, lis, srv)` uses graceful shutdown before falling back to `Stop()`.

## Related folders

- [`../db/`](../db/) implements storage operations used by `KVService` and `SQLService`.
- [`../cluster/`](../cluster/) provides the Raft-backed write path used by `ClusterKVServer`.
- [`../cmd/pallasdb/`](../cmd/pallasdb/) starts standalone and cluster gRPC servers.

## Verification

```sh
go test -race ./grpc
buf lint
buf generate
```
