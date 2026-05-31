# PallasDB M4 MacBook Air Benchmark Plan

## Request
Use the supplied Badger/RocksDB benchmark notes as inspiration to build and run a practical benchmark on the current 16GB Apple M4 MacBook Air. The benchmark should cover the same core workload classes: populate/write, random point reads, and iteration/scans, but scaled to this machine and this repository's PallasDB implementation. Save the finalized benchmark plan in a repository `benchmarks/` folder, and write all benchmark run output/results to a text file in that same folder.

## Key findings
- The storage engine lives in `db/` and exposes the public API we need: `NewKV`, `Set`, `Get`, `IterAll`, `Range`, `Compact`, and options (`WithLogThreshold`, `WithGrowthFactor`, `WithAutoCompact`, `WithCache`) in `db/kv.go`.
- PallasDB writes through a WAL with fsync on commit (`db/log.go`), then compacts sorted in-memory data into SSTables (`db/kv.go`, `db/sorted_file.go`). Benchmark populate must use configurable transaction batches; one commit per key would primarily measure fsync overhead.
- SSTable point lookups currently perform binary search by reading keys from disk and allocate key/value buffers (`db/sorted_file.go:241-320`), so random read and iteration benchmarks should report allocations/memory as well as latency/throughput.
- There are no existing benchmarks (`func Benchmark...` search returned no matches). Existing tests use Cobra command helpers in `cmd/pallasdb/root_test.go` and temp dirs in `cmd/pallasdb/local_test.go`.
- The CLI already has `pallasdb local {put,get,delete,range,compact}` (`cmd/pallasdb/local.go`). A local benchmark subcommand fits the existing user workflow and avoids overloading `go test` with long-running, persistent, disk-heavy workloads.
- Go version on this machine is `go1.26.3 darwin/arm64`.

## Recommended approach
Add a dedicated local benchmark runner under the existing CLI: `pallasdb local benchmark`.

Why this instead of only Go `testing.B` benchmarks:
- Full-dataset populate/read/iterate benchmarking is stateful and disk-backed; a CLI can reuse or reset a dataset explicitly, emit JSON, and make output stable for later comparison.
- It matches the user's Badger/RocksDB workflow (`DATADIR`, explicit value size/key count, random read and iteration phases) while staying idiomatic for this repo's Cobra CLI.
- We can still test the command with small fixtures; the real benchmark run stays outside unit tests and outside the repository tree.

## Files to modify
- `cmd/pallasdb/local.go`
  - Add `newLocalBenchmarkCommand(...)` to the local command tree.
  - Register it alongside `get`, `put`, `delete`, `range`, and `compact`.
- `cmd/pallasdb/local_benchmark.go` (new)
  - Implement benchmark options, validation, deterministic key/value generation, phase execution, metrics, and output formatting.
  - Add `--output` to write the complete benchmark report to a caller-specified text file while still printing to stdout.
  - Keep benchmark-specific flags command-local. Do not add global config keys unless implementation shows the existing CLI requires it.
- `cmd/pallasdb/local_test.go`
  - Add small command tests for benchmark validation, smoke execution, and `--output` file writing.
- `benchmarks/` (new directory)
  - Save a copy of this approved plan as `benchmarks/PallasDB-M4-Benchmark.md`.
  - Write the actual M4 benchmark run results to `benchmarks/m4-macbook-air-results.txt`.

## Command design
Proposed command:

```sh
go run ./cmd/pallasdb local benchmark \
  --data-dir "$HOME/bench-data/pallasdb-m4/v128" \
  --keys 1000000 \
  --value-size 128 \
  --batch-size 1000 \
  --read-ops 300000 \
  --scan-limit 0 \
  --log-threshold 100000 \
  --growth-factor 2 \
  --compact \
  --format text \
  --output benchmarks/m4-macbook-air-results.txt
```

Flags:
- `--keys`: number of keys to populate.
- `--value-size`: bytes per value.
- `--key-size`: fixed-width key payload size, default enough for deterministic numeric keys.
- `--batch-size`: number of writes per transaction commit.
- `--read-ops`: random point reads to execute after populate/reopen.
- `--scan-limit`: max keys per iteration phase; `0` means all keys.
- `--log-threshold`: PallasDB memtable flush threshold.
- `--growth-factor`: compaction merge factor.
- `--auto-compact`: enable background compaction during populate; default off for repeatability.
- `--compact`: run foreground compaction after populate before read/scan phases.
- `--cache-bytes` and `--cache-counters`: enable `WithCache` for a separate cached-read run when non-zero.
- `--reset`: remove only the selected benchmark data dir before populate. Default false to avoid accidental data loss.
- `--format`: `text` or `json`.
- `--output`: optional path for writing the complete benchmark report; for the requested run use `benchmarks/m4-macbook-air-results.txt`.

Phases:
1. `populate`: write deterministic keys and values in batches through `KVTX` and `Commit`.
2. `reopen`: close and reopen the store so reads measure persisted state rather than only the active memtable.
3. `random_read`: deterministic pseudo-random key sequence; verify found values have the expected size.
4. `iterate_keys`: iterate all or `--scan-limit` keys without touching values beyond the API-required key path.
5. `iterate_values`: iterate all or `--scan-limit` keys and touch `Val()` length/checksum so value loading is measured.
6. Optional `cached_random_read`: reopen with `WithCache`, warm once, then run the same read sequence.

Metrics to emit per phase:
- elapsed duration
- operations
- ns/op
- ops/sec
- logical bytes processed where meaningful
- bytes/sec where meaningful
- found/missing/error counts
- Go memory stats delta (`Mallocs`, `TotalAlloc`, `HeapAlloc`, `NumGC`)
- final data directory size, computed recursively
- benchmark parameters and host/runtime metadata (`GOOS`, `GOARCH`, Go version, `GOMAXPROCS`, CPU count)

## Dataset and artifact plan for this 16GB M4 MacBook Air
- Store database benchmark data under `$HOME/bench-data/pallasdb-m4` rather than a repository-local directory.
- Store benchmark documentation and run output under repository `benchmarks/`.
- Save the approved plan to `benchmarks/PallasDB-M4-Benchmark.md`.
- Append or rewrite the full benchmark run transcript/results into `benchmarks/m4-macbook-air-results.txt` using the CLI `--output` flag and include the exact commands in the file.

Run sequence after implementation:

1. Correctness smoke run:
```sh
go run ./cmd/pallasdb local benchmark \
  --data-dir "$HOME/bench-data/pallasdb-m4/smoke" \
  --reset --keys 20000 --value-size 128 --batch-size 500 \
  --read-ops 20000 --scan-limit 20000 --compact \
  --format text --output benchmarks/m4-macbook-air-results.txt
```

2. Scaled local benchmark runs:
```sh
# Small values, more keys.
go run ./cmd/pallasdb local benchmark \
  --data-dir "$HOME/bench-data/pallasdb-m4/v128" \
  --reset --keys 2000000 --value-size 128 --batch-size 1000 \
  --read-ops 500000 --compact \
  --format text --output benchmarks/m4-macbook-air-results.txt

# 1KiB values, moderate key count.
go run ./cmd/pallasdb local benchmark \
  --data-dir "$HOME/bench-data/pallasdb-m4/v1024" \
  --reset --keys 500000 --value-size 1024 --batch-size 1000 \
  --read-ops 300000 --compact \
  --format text --output benchmarks/m4-macbook-air-results.txt

# 16KiB values, reduced key count to keep disk and RAM pressure suitable for 16GB.
go run ./cmd/pallasdb local benchmark \
  --data-dir "$HOME/bench-data/pallasdb-m4/v16384" \
  --reset --keys 50000 --value-size 16384 --batch-size 250 \
  --read-ops 100000 --compact \
  --format text --output benchmarks/m4-macbook-air-results.txt
```

If the smoke run exposes a correctness bug or the scaled runs are clearly dominated by an implementation flaw, stop the large sequence, fix the source issue, and rerun the affected benchmark.

## Verification
Before running the real benchmark:
- `go test ./cmd/pallasdb -run 'TestLocalBenchmark'`
- `go test ./db ./cmd/pallasdb`
- Execute the smoke benchmark command above and verify:
  - populated count equals `--keys`
  - random read found count equals `--read-ops`
  - iteration count equals `min(keys, scan-limit)` when `scan-limit > 0`
  - stdout and `benchmarks/m4-macbook-air-results.txt` contain complete phase metrics and environment metadata
- Verify `benchmarks/PallasDB-M4-Benchmark.md` exists and contains the approved plan.

Final output to user after implementation/run:
- exact command lines used
- path to `benchmarks/PallasDB-M4-Benchmark.md`
- path to `benchmarks/m4-macbook-air-results.txt`
- summarized table for each value size
- data directory size for each run
- any observed correctness failures or runtime limits, grounded in command output
