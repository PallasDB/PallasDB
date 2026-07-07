# Protobuf API definitions

[`proto`](../proto/) contains the source Protocol Buffers schema for PallasDB's gRPC transport. Runtime server code is in [`../grpc/`](../grpc/), and generated Go code is in [`../pb/v1/`](../pb/v1/).

Detailed docs: [gRPC API](https://pallasdb.github.io/docs/grpc-api.html).

## Schema

- [`pallasdb/v1/kv.proto`](pallasdb/v1/kv.proto) defines `KVService` and `ClusterService`.
- [`../buf.yaml`](../buf.yaml) configures linting.
- [`../buf.gen.yaml`](../buf.gen.yaml) configures Go and gRPC code generation.

## Regenerate Go code

```sh
go install github.com/bufbuild/buf/cmd/buf@latest
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
buf generate
```

## Validate protobuf and generated code

```sh
buf lint
buf generate
go test ./...
```

## Related folders

- [`../grpc/`](../grpc/) implements the generated service interfaces.
- [`../cluster/`](../cluster/) backs `ClusterService` writes and membership data.
- [`../db/`](../db/) backs `KVService` point operations and ranges.
- [`../cmd/pallasdb/`](../cmd/pallasdb/) starts the gRPC and cluster servers.
