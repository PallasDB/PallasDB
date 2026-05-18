# go-database

A step-by-step implementation of a key-value database in Go using LSM trees, SSTables, and concurrent access.

This project follows the "Database in 45 Steps" curriculum, building a full database engine from first principles — starting with raw byte encoding and progressing through write-ahead logging, sorted string tables, multi-level merging, a SQL parser, and an expression evaluator. Each numbered directory (`db_project/01xx` through `db_project/09xx`) is a complete, cumulative iteration of the implementation.

> Note: The Go module is named `example/hello` rather than `go-database`. This is a carry-over from the scaffolded module name used at project initialization and does not affect functionality.

## Table of Contents

- [Background](#background)
- [Install](#install)
- [Usage](#usage)
- [API](#api)
- [Maintainers](#maintainers)
- [Contributing](#contributing)
- [License](#license)

## Background

Understanding how databases work internally — how data is stored, indexed, and retrieved efficiently — is difficult to learn from production codebases alone. This project provides a guided, incremental path through those concepts.

The implementation covers:

- **Cells and Rows**: Binary encoding of typed values (`i64`, `str`) into byte slices.
- **Write-Ahead Log (WAL)**: Crash-safe persistence with fsync and CRC32 checksums.
- **Sorted KV Store**: In-memory sorted key-value store with binary search.
- **SSTables**: Persistent sorted string tables for durable storage.
- **LSM Merging**: Multi-level merge iterators that unify sorted streams across levels.
- **Metadata management**: Double-buffered metadata files tracking SSTable versions.
- **SQL Parser**: Tokenizer and recursive-descent parser for `CREATE TABLE`, `SELECT`, `INSERT`, `UPDATE`, and `DELETE`.
- **Expression Evaluator**: Arithmetic, comparison, and logical operators for `WHERE` clause evaluation.

See the reference material: _db-in-45-steps-go.pdf_ (included in the repository root).

## Install

Requires Go 1.21 or later.

```sh
git clone https://github.com/teddymalhan/go-database.git
cd go-database
go mod download
```

## Usage

This project is structured as a library and test suite, not a standalone binary. Each implementation step lives under `db_project/<step>/` and has its own tests.

Run tests for the latest complete implementation:

```sh
go test ./db_project/0904/...
```

Run tests for a specific step:

```sh
go test ./db_project/0401/...
```

Run all tests across all steps:

```sh
go test ./...
```

### Importing a step as a package

```go
import "example/hello/db_project/0904"

db, err := hello.OpenDB("path/to/data")
if err != nil {
    log.Fatal(err)
}
defer db.Close()
```

## API

Each step exposes a progressively richer surface. The final implementation (`db_project/0904`) exports:

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

Earlier steps (`01xx`–`08xx`) expose subsets of this API and are useful for studying individual components in isolation.

## Maintainers

[@teddymalhan](https://github.com/teddymalhan)

## Contributing

Questions and feedback are welcome via [GitHub Issues](https://github.com/teddymalhan/go-database/issues).

Pull requests are accepted. Please ensure all existing tests pass before submitting:

```sh
go test ./...
```

There is no formal contributor license agreement, but by submitting a PR you agree that your contribution may be distributed under the project's license.

## License

[AGPL-3.0](LICENSE) © Teddy Malhan
