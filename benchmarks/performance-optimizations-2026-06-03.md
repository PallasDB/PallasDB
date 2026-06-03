# PallasDB Performance Optimizations — 2026-06-03

## Scope

Two storage-engine hot paths were optimized after reviewing the existing M4 benchmark results and the `db/` implementation:

1. SSTable point-lookups now compare keys during binary search without reading and allocating the associated value at every search step.
2. Ordered write batches now append directly to the memtable when no other snapshot is active, while preserving the existing merge path for snapshot isolation and overlapping updates.

## Code paths changed

- `db/sorted_file.go`
  - `findPos` now uses a key-only comparison helper for SSTable binary search.
  - Large values are no longer read during intermediate binary-search comparisons.
  - Small keys use a stack buffer during comparison to avoid heap allocation on the common fixed-width benchmark key path.
- `db/kv.go`
  - `updateMem` appends sorted, non-overlapping batch updates directly into `kv.mem` when there is only one active transaction.
  - The original merged-iterator path remains the fallback when snapshots are active or the update range overlaps existing memtable keys.

## Directional benchmark

Command used before and after the optimization:

```sh
go run ./cmd/pallasdb local benchmark \
  --data-dir /tmp/pallasdb-perf-baseline \
  --reset --keys 5000 --value-size 16384 --batch-size 500 \
  --read-ops 10000 --scan-limit 5000 --compact --format text
```

The after run used `/tmp/pallasdb-perf-after2` as the data directory to avoid reusing the baseline data.

Environment from the benchmark runner:

- `goos`: `darwin`
- `goarch`: `arm64`
- `go_version`: `go1.26.3`
- `gomaxprocs`: `10`
- `num_cpu`: `10`

### 5k keys, 16 KiB values, 10k random reads

| Phase metric | Before | After |
|---|---:|---:|
| random_read ns/op | 32,442.98 | 17,044.86 |
| random_read ops/sec | 30,823.31 | 58,668.70 |
| random_read mallocs | 173,721 | 60,005 |
| random_read total allocated bytes | 2,282,052,872 | 188,000,376 |
| random_read GC count | 775 | 56 |

Correctness counters were unchanged: both runs reported `found: 10000`, `missing: 0`, and `errors: 0` for `random_read`.

## Post-change write workload check

Command:

```sh
go run ./cmd/pallasdb local benchmark \
  --data-dir /tmp/pallasdb-write-after \
  --reset --keys 200000 --value-size 128 --batch-size 1000 \
  --read-ops 50000 --compact --format text
```

Observed after the memtable append optimization:

| Phase | ns/op | ops/sec | Correctness |
|---|---:|---:|---|
| populate | 6,618.61 | 151,089.11 | `updated: 200000`, `errors: 0` |
| random_read | 16,951.78 | 58,990.87 | `found: 50000`, `missing: 0`, `errors: 0` |
| iterate_values | 994.66 | 1,005,369.72 | `operations: 200000`, `errors: 0` |

## Verification

```sh
go test ./db ./cmd/pallasdb
go test ./...
```

Both commands passed after the optimization.
