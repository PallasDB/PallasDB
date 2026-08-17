# Storage engine and Go API

[`db`](../db/) contains the embedded storage engine and higher-level table database used by the [`pallasdb` CLI](../cmd/pallasdb/), [`grpc`](../grpc/) server, and [`cluster`](../cluster/) FSM.

In-repo docs: [Architecture](../docs/architecture.md), [Durability
model](../docs/durability.md), and [SQL surface](../docs/sql.md).

## Features owned here

- **Cells and rows**: binary encoding of typed values (`i64`, `str`) into byte slices.
- **Write-ahead log**: sequence-numbered, CRC32-checksummed records, fsynced
  before a write is acknowledged. The containing directory is also fsynced on
  unix; on other platforms it is not, and that gap is reported through
  `DirSyncSupported` and `ErrDirSyncUnsupported` rather than assumed away. See
  [Durability model](../docs/durability.md) for the precise guarantee.
- **Sorted memtable**: in-memory sorted key-value store with binary search.
- **SSTables**: persistent sorted string tables for durable storage.
- **LSM merging**: multi-level merge iterators that unify sorted streams across levels.
- **Metadata management**: double-buffered metadata files tracking SSTable versions.
- **Transactions**: snapshots, staged writes, conflict detection, and range scans.
- **SQL layer**: tokenizer, recursive-descent parser, expression evaluation,
  row encoding, and table operations for a small SQL dialect — `CREATE TABLE`,
  `INSERT`, `SELECT`, `UPDATE`, `DELETE`, `DROP TABLE`, with `WHERE`,
  `LIMIT`/`OFFSET`, and tuple comparisons. No joins, no aggregates, no
  `ORDER BY`; see [SQL surface](../docs/sql.md). Reachable in-process through
  `NewDB`/`ExecStmt`, from the CLI via `pallasdb sql`, and over the wire via
  `SQLService.Query`.

## Local key-value API

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
| `DB.Query(statement) (*SQLResult, error)` | Parse and execute one SQL statement. `DBTX` has the same method. |
| `DB.ExecStmt(stmt) (*SQLResult, error)` | Execute an already-parsed statement from `ParseStmt`. |
| `SQLResult.Columns() / Next() / Row() / Err()` | Stream a `SELECT`. `Close()` is mandatory — a `SELECT` holds a read transaction until then. |
| `SQLResult.RowsAffected()` | Row count for `INSERT`/`UPDATE`/`DELETE`. |

The SQL dialect and the full result-streaming contract are in
[SQL surface](../docs/sql.md).

## Related folders

- [`../cmd/pallasdb/`](../cmd/pallasdb/) exposes local storage operations and benchmarks through the CLI.
- [`../grpc/`](../grpc/) wraps `*db.KV` in gRPC services.
- [`../cluster/`](../cluster/) applies Raft log entries into `*db.KV` through the FSM.
- [`../benchmarks/`](../benchmarks/) stores recorded storage-engine benchmark artifacts.

## Verification

```sh
go test -race ./db          # engine, transactions, table layer, SQL parser
go test -list '^Fuzz' ./db  # fuzz targets, run nightly by CI
```

CI runs this package on Linux, macOS, and Windows: `os_unix.go` and
`os_other.go` are selected by build tag, so a single-platform run silently skips
one of them.
