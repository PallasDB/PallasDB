# gokv

A key-value database in Go using LSM trees, SSTables, write-ahead logging, and concurrent access.

## Background

This project builds a small database engine from first principles: raw byte encoding, crash-safe persistence, sorted string tables, multi-level merging, a SQL parser, and expression evaluation.

The implementation lives in the root package folder `gokv/`.

## Features

- **Cells and Rows**: Binary encoding of typed values (`i64`, `str`) into byte slices.
- **Write-Ahead Log (WAL)**: Crash-safe persistence with fsync and CRC32 checksums.
- **Sorted KV Store**: In-memory sorted key-value store with binary search.
- **SSTables**: Persistent sorted string tables for durable storage.
- **LSM Merging**: Multi-level merge iterators that unify sorted streams across levels.
- **Metadata management**: Double-buffered metadata files tracking SSTable versions.
- **SQL Parser**: Tokenizer and recursive-descent parser for `CREATE TABLE`, `SELECT`, `INSERT`, `UPDATE`, and `DELETE`.
- **Expression Evaluator**: Arithmetic, comparison, and logical operators for `WHERE` clause evaluation.

## Install

Requires Go 1.24 or later.

```sh
git clone https://github.com/teddymalhan/gokv.git
cd gokv
go mod download
```

## Usage

Run all tests:

```sh
go test ./...
```

Regenerate protobuf/gRPC code:

```sh
go install github.com/bufbuild/buf/cmd/buf@latest
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
buf generate
```

Validate protobuf/gRPC and Go code:

```sh
buf lint
buf generate
go test ./...
```

Run the gRPC server:

```sh
go run ./cmd/gokv-grpc -addr :50051 -data-dir ./data
```

Quick gRPC workflow:

```sh
# Regenerate protobuf/gRPC stubs
buf generate

# Validate generated code and tests
buf lint
go test ./...

# Start the server
go run ./cmd/gokv-grpc -addr :50051 -data-dir ./data
```

Import the package:

```go
import "github.com/teddymalhan/gokv/gokv"

db, err := gokv.OpenDB("path/to/data")
if err != nil {
    log.Fatal(err)
}
defer db.Close()
```

## API

| Symbol | Description |
|---|---|
| `OpenDB(path string) (*DB, error)` | Open or create a database at the given path. |
| `DB.Insert(schema, row)` | Insert a row; fails if the key already exists. |
| `DB.Upsert(schema, row)` | Insert or update a row. |
| `DB.Update(schema, row)` | Update an existing row; fails if absent. |
| `DB.Delete(schema, row)` | Delete a row by primary key. |
| `DB.Select(schema, row) (Row, error)` | Fetch a single row by primary key. |
| `DB.Seek(schema, row, cmp) RowIterator` | Open a range iterator from a given key. |
| `DB.Range(schema, key1, key2) []Row` | Return all rows between two keys. |

## Maintainers

[@teddymalhan](https://github.com/teddymalhan)

## Contributing

Questions and feedback are welcome via [GitHub Issues](https://github.com/teddymalhan/gokv/issues).

Before submitting changes, run:

```sh
go test ./...
```

## License

[AGPL-3.0](LICENSE) © Teddy Malhan
