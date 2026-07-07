# gRPC transport

[`grpc`](../grpc/) exposes the [`db`](../db/) key-value store and [`cluster`](../cluster/) management operations over Protocol Buffers/gRPC.

Detailed docs: [gRPC API](https://pallasdb.github.io/docs/grpc-api.html).

## Services

```protobuf
service KVService {
  rpc Get(GetRequest) returns (GetResponse);
  rpc Put(PutRequest) returns (PutResponse);
  rpc Delete(DeleteRequest) returns (DeleteResponse);
  rpc Range(RangeRequest) returns (stream RangeResponse);
}

service ClusterService {
  rpc Join(JoinRequest) returns (JoinResponse);
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

## Implementation notes

- `KVServer` wraps `*db.KV` and implements `KVServiceServer`.
- `ClusterServer` routes mutating operations through a Raft node when cluster mode is active.
- `Range` is server-streaming and holds a transaction snapshot for the stream lifetime.
- `NewGRPCServer` registers the standard gRPC health service.
- `LoggingUnaryInterceptor` logs unary method, status code, and duration through `log/slog`.
- `Serve(ctx, lis, srv)` uses graceful shutdown before falling back to `Stop()`.

## Related folders

- [`../db/`](../db/) implements storage operations used by `KVService`.
- [`../cluster/`](../cluster/) provides the Raft-backed write path used by `ClusterServer`.
- [`../cmd/pallasdb/`](../cmd/pallasdb/) starts standalone and cluster gRPC servers.

## Verification

```sh
go test ./grpc
buf lint
buf generate
```
