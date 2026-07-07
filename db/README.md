# Storage engine and Go API

[`db`](../db/) contains the embedded storage engine and higher-level table database used by the [`pallasdb` CLI](../cmd/pallasdb/), [`grpc`](../grpc/) server, and [`cluster`](../cluster/) FSM.

Detailed docs: [Architecture Overview](https://pallasdb.github.io/docs/architecture.html), [Storage Engine](https://pallasdb.github.io/docs/storage/index.html), [Transactions](https://pallasdb.github.io/docs/transactions.html), [SQL Parser & Evaluator](https://pallasdb.github.io/docs/sql.html), and [Go API Reference](https://pallasdb.github.io/docs/go-api.html).

## Features owned here

- **Cells and rows**: binary encoding of typed values (`i64`, `str`) into byte slices.
- **Write-ahead log**: crash-safe persistence with fsync and CRC32 checksums.
- **Sorted memtable**: in-memory sorted key-value store with binary search.
- **SSTables**: persistent sorted string tables for durable storage.
- **LSM merging**: multi-level merge iterators that unify sorted streams across levels.
- **Metadata management**: double-buffered metadata files tracking SSTable versions.
- **Transactions**: snapshots, staged writes, conflict detection, and range scans.
- **SQL layer**: tokenizer, recursive-descent parser, row encoding, expression evaluation, and table operations.

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

## Related folders

- [`../cmd/pallasdb/`](../cmd/pallasdb/) exposes local storage operations and benchmarks through the CLI.
- [`../grpc/`](../grpc/) wraps `*db.KV` in gRPC services.
- [`../cluster/`](../cluster/) applies Raft log entries into `*db.KV` through the FSM.
- [`../benchmarks/`](../benchmarks/) stores recorded storage-engine benchmark artifacts.

## Verification

```sh
go test ./db
```
