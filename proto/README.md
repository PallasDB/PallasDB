# Protobuf API definitions

[`proto`](../proto/) contains the source Protocol Buffers schema for PallasDB's gRPC transport. Runtime server code is in [`../grpc/`](../grpc/), and generated Go code is in [`../pb/v1/`](../pb/v1/).

In-repo docs: [Architecture](../docs/architecture.md).

## Schema

- [`pallasdb/v1/kv.proto`](pallasdb/v1/kv.proto) defines `KVService`,
  `SQLService`, and `ClusterService`.
- [`../buf.yaml`](../buf.yaml) configures linting and the breaking-change check.
- [`../buf.gen.yaml`](../buf.gen.yaml) configures Go and gRPC code generation.

## Regenerate Go code

`pb/` is generated. Never edit it by hand.

```sh
make tools            # pinned protoc-gen-go and protoc-gen-go-grpc
make proto            # buf lint + buf generate
make proto-breaking   # buf breaking --against '.git#branch=main'
git add proto pb
```

Install `buf` separately: <https://buf.build/docs/installation>.

## What CI enforces

[`.github/workflows/proto.yml`](../.github/workflows/proto.yml) runs `buf lint`,
`buf breaking` against `main`, and `buf generate` followed by
`git diff --exit-code`. A schema change without its regenerated Go code fails,
and so does hand-editing `pb/`.

`v1` is a published surface. A backwards-incompatible change needs a `v2`
package, not an edit to `v1`.

## Related folders

- [`../grpc/`](../grpc/) implements the generated service interfaces.
- [`../cluster/`](../cluster/) backs `ClusterService` writes and membership data.
- [`../db/`](../db/) backs `KVService` point operations and ranges.
- [`../cmd/pallasdb/`](../cmd/pallasdb/) starts the gRPC and cluster servers.
