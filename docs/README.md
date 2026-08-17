# PallasDB documentation

Documentation that lives with the code it describes. Anything under this
directory is versioned and reviewed alongside the change that makes it true.

## Cross-cutting

| Page | Covers |
|---|---|
| [Getting started](getting-started.md) | Install, first key-value and SQL statements, first cluster |
| [Architecture](architecture.md) | How the layers stack and where a request goes |
| [SQL surface](sql.md) | The SQL subset PallasDB actually implements |
| [Durability model](durability.md) | What survives a crash, and what differs per platform |

## Per-package

Each package README is authoritative for its own area. These pages route to
them rather than restating them, so there is one place to update:

| Area | README |
|---|---|
| CLI, configuration, local commands | [`cmd/pallasdb/`](../cmd/pallasdb/README.md) |
| Storage engine, SQL layer, Go API | [`db/`](../db/README.md) |
| gRPC server implementation | [`grpc/`](../grpc/README.md) |
| Raft cluster and Serf discovery | [`cluster/`](../cluster/README.md) |
| Protobuf definitions and codegen | [`proto/`](../proto/README.md) |
| Benchmark artifacts and commands | [`benchmarks/`](../benchmarks/README.md) |

## Contributing to the docs

Development workflow, CI gates, and release process are in
[`CONTRIBUTING.md`](../CONTRIBUTING.md).

Two rules keep this directory from rotting:

1. **State only what the code does today.** A feature that is designed but not
   reachable does not get documented as if it ships.
2. **Do not duplicate a package README.** Link to it. If a claim belongs to one
   package, it belongs in that package's README.

Diagrams live in [`diagrams/`](diagrams/).
