# Benchmarks

[`benchmarks`](../benchmarks/) stores the checked-in benchmark plan, raw results, and optimization notes for PallasDB's local disk-backed store.

In-repo docs: [Architecture](../docs/architecture.md) explains the storage
layers these numbers exercise. Run `make bench` for Go microbenchmarks; the
commands below drive the CLI benchmark runner against real disk.

## Artifacts

- Plan: [`PallasDB-M4-Benchmark.md`](PallasDB-M4-Benchmark.md)
- Results: [`m4-macbook-air-results.txt`](m4-macbook-air-results.txt)
- Optimization notes: [`performance-optimizations-2026-06-03.md`](performance-optimizations-2026-06-03.md)

## Run a benchmark

The benchmark runner is part of the [`pallasdb` CLI](../cmd/pallasdb/) and exercises the [`db`](../db/) storage engine on disk.

```sh
go run ./cmd/pallasdb local benchmark \
  --data-dir "$HOME/bench-data/pallasdb-m4/v128" \
  --reset --keys 2000000 --value-size 128 --batch-size 1000 \
  --read-ops 500000 --compact \
  --format text --output benchmarks/m4-macbook-air-results.txt
```

## Recorded M4 summary

| Value size | Keys | Populate ops/sec | Random read ops/sec | Iterate values ops/sec | Data dir size |
|---:|---:|---:|---:|---:|---:|
| 128 B | 2,000,000 | 19,993.37 | 26,588.23 | 974,720.76 | 324,396,366 B |
| 1 KiB | 500,000 | 43,362.86 | 33,529.22 | 732,912.96 | 529,099,166 B |
| 16 KiB | 50,000 | 12,281.01 | 5,364.19 | 141,650.58 | 820,910,006 B |

Correctness notes from the recorded runs:

- Final smoke and scaled runs reported `missing: 0` and `errors: 0`.
- Smoke run verified expected counts: populate `20000`, random read found `20000`, iteration `20000`.
- The benchmark runner now creates the benchmark data directory before opening PallasDB.

## Recent storage-engine optimization results

| Workload | Before | After | Change |
|---|---:|---:|---:|
| 16 KiB random read latency | 32,442.98 ns/op | 17,044.86 ns/op | 47.5% lower |
| 16 KiB random read throughput | 30,823.31 ops/sec | 58,668.70 ops/sec | 90.3% higher |
| 16 KiB random read total allocation | 2,282,052,872 B | 188,000,376 B | 91.8% lower |
| 16 KiB random read GC count | 775 | 56 | 92.8% lower |

The optimized code avoids reading SSTable values during binary-search comparisons and appends ordered write batches directly to the memtable when no snapshot requires the merge path. See the optimization notes for commands, environment, and verification output.

## Verification

```sh
go test ./cmd/pallasdb -run 'TestLocalBenchmark'
go test ./db ./cmd/pallasdb
```
