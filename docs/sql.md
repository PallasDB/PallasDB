# SQL surface

PallasDB implements a deliberately small SQL subset over its table layer. This
page states what the parser and evaluator accept. Anything not listed here is
not implemented — not "coming soon", not silently ignored: it is a parse or
evaluation error.

## Statements

```sql
CREATE TABLE t (a string, b int64, PRIMARY KEY (b));
INSERT INTO t VALUES (1, 'hi');
SELECT a, b FROM t WHERE b = 1;
SELECT * FROM t WHERE b >= 1 LIMIT 10 OFFSET 5;
UPDATE t SET a = 'x', b = 2 WHERE b = 3;
DELETE FROM t WHERE b = 3;
DROP TABLE t;
```

Keywords are case-insensitive; identifiers are not. Statements are terminated
with `;`.

## Types

Two column types: `int64` and `string`. There is no `NULL`, no floating point,
no date/time type, and no type coercion between the two.

## Expressions

`WHERE` accepts:

- comparison: `=`, `!=`, `<`, `<=`, `>`, `>=`
- boolean: `AND`, `OR`, `NOT`
- arithmetic: `+`, `-`, `*`, `/`, unary `-`
- tuple comparison against a composite key or index:
  `WHERE (c, d) >= (3, 4)`

Tuple comparison is what makes a composite-index range scan expressible. A
comparison whose left side is the primary key or an indexed prefix is planned as
an index range scan; anything else is a scan plus a filter. Correctness does not
depend on which plan is chosen, only speed.

Expression nesting depth is bounded, so a pathologically nested input is
rejected rather than overflowing the stack.

## Not implemented

No joins. No subqueries. No aggregates or `GROUP BY` / `HAVING`. No `ORDER BY`
(rows come back in index order). No `ALTER TABLE`. No views, triggers, or
stored procedures. No `BEGIN`/`COMMIT` in SQL — use `db.KV.NewTX` from Go for
multi-statement atomicity. No prepared statements or bind parameters: build
statements from trusted input, or use the key-value API.

## How to run a statement

Three ways, same parser and evaluator underneath:

**In-process, from Go** — see [`db/README.md`](../db/README.md):

```go
sqlDB, err := db.NewDB("path/to/data")
if err != nil {
    log.Fatal(err)
}
defer sqlDB.Close()

r, err := sqlDB.Query("SELECT a, b FROM t WHERE b = 1;")
if err != nil {
    log.Fatal(err)
}
defer r.Close() // mandatory: a SELECT holds a read transaction until Close

for _, c := range r.Columns() {
    fmt.Println(c.Name, c.Type)
}
for r.Next() {
    row := r.Row() // valid until the next Next(); clone it to keep it
    fmt.Println(row)
}
if err := r.Err(); err != nil {
    log.Fatal(err)
}
```

`Next` returns false both at the end of the result and on error, so `Err` is
what tells the two apart — `Close` reports only the failure to release the
transaction, never the query error. `RowsAffected` carries the count for
`INSERT`/`UPDATE`/`DELETE`; `Rows` drains everything into a slice for the cases
where streaming is not worth the ceremony, and still requires a `Close`.
`DBTX` has the same `Query`/`ExecStmt` pair, so a statement can run inside an
existing transaction. `ParseStmt` exposes the parser on its own.

**From the CLI**, against a local directory or a remote server:

```sh
pallasdb sql "SELECT * FROM t WHERE b = 1"
```

**Over gRPC**, via `SQLService.Query`. The stream is: one header message with
`columns` and no `values`, then one message per row with `values`, then — for
non-`SELECT` statements only — a final message with `rows_affected`. Rows are
streamed as they are produced, not materialised first, so a large `SELECT` does
not have to fit in the server's memory. See [`grpc/README.md`](../grpc/README.md).

## Schema storage

Table schemas live in a reserved, versioned metadata keyspace. A raw key-value
client talking to the same store cannot overwrite them by writing an ordinary
key.
