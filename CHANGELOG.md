# Changelog

All notable changes to PallasDB are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

The first release-engineered wave. The storage engine's concurrency and
durability holes are closed, the SQL layer is reachable from outside the process
for the first time, cluster membership gained a real lifecycle, and the project
acquired the release, security, and CI machinery it was missing.

### Added

- SQL gains `SELECT *`, an optional `WHERE`, `LIMIT n [OFFSET m]`, and
  `DROP TABLE`, and `SELECT` results stream row by row instead of being
  materialised.
- New `client` package: a documented Go client for `Get`, `Put`, `Delete`,
  streaming `Range`, and streaming SQL `Query`, with automatic bounded retry
  against the Raft leader when a follower answers `Unavailable`.
- `SQLService.Query` streams SQL results over gRPC: one header message carrying
  the column list, one message per row, and a trailing `rows_affected` message
  for non-`SELECT` statements.
- `pallasdb sql` runs a statement against a remote server or opens an
  interactive REPL, rendering results as an aligned table or JSON.
  `pallasdb kv get/put/delete/range` talk to a running server, and
  `pallasdb dump`/`restore` export and import the whole keyspace, locally or
  remotely.
- Read requests take a consistency level, so a caller can choose between a fast
  local read and a leader-verified read. `Range` requests take a `limit` and a
  `keys_only` flag.
- `ClusterService.Leave` removes a node from the cluster explicitly, and
  `GetLeader` returns the leader's gRPC address as well as its Raft address.
- `--bootstrap-expect N` forms exactly one cluster from a symmetric N-node
  start, replacing the auto-bootstrap that could split-brain into N single-node
  clusters.
- New nodes join as non-voters and are promoted to voters only once they have
  caught up; `--non-voter` creates permanent read-only replicas.
- Multi-key writes can be replicated as a single atomic batch entry.
- gRPC server reflection, and Prometheus metrics for RPC count, latency, and
  status codes, including Raft's own counters.
- Release engineering: a `Makefile` as the single entry point, a multi-stage
  `Dockerfile` producing a static `scratch` image, `.goreleaser.yaml` for
  darwin/linux amd64+arm64 archives, and a tag-triggered release workflow
  publishing binaries and a multi-arch image to GHCR.
- CI runs the test suite on Linux, macOS, and Windows across two Go versions, so
  the platform-specific `os_unix.go` and `os_other.go` paths are compiled and
  exercised rather than assumed.
- CI runs `buf lint`, `buf breaking` against `main`, and a `buf generate` drift
  check, enforcing the backwards-compatibility guard the repository was already
  configured for but never ran.
- A nightly fuzzing workflow with a cached corpus, plus `make fuzz`.
- `CONTRIBUTING.md` and an in-repo `docs/` tree, so documentation lives with the
  code it describes.

### Changed

- `pallasdb version` reports the real version, commit, and build date. The
  `-ldflags` that populate them were never wired up, so every build reported
  `dev / none / unknown`; the command also falls back to Go build info when link
  stamps are absent.
- Raft snapshots and replicated commands use a compact, versioned, checksummed
  binary format that streams in both directions instead of buffering the whole
  dataset in memory. Logs written by the previous release still replay.
- A replica that cannot apply a committed Raft entry (I/O error, unrecognised
  command from a newer leader, corrupt log) aborts instead of silently skipping
  it and diverging from the cluster. Deterministic rejections, such as an
  invalid write mode, are still returned to the caller.
- Oversized values are rejected when the log record is written instead of only
  when it is read back, so an over-large entry can no longer make the store
  permanently unopenable. The named limit is exported as `db.MaxEntrySize`.
- Data files are now fsynced on every platform, including Windows. Directory
  fsync remains unix-only, but the gap is exposed as `db.DirSyncSupported` and
  `db.ErrDirSyncUnsupported` instead of being silently ignored.
- Cluster shutdown transfers leadership away, leaves the configuration, and
  releases the transport and log store even when one of those steps fails.
- A linearizable read confirms leadership and waits for the local FSM to catch
  up, instead of writing a replicated barrier entry on every read. Stale reads
  can be served by any node.
- Health status is reported per service and follows leadership, and message
  size, concurrent stream, and keepalive limits are set. The send limit was
  previously unbounded.
- Coverage gating is real. The upload can now fail the build, which previously
  left the targets permanently unevaluated, and the project status is a ratchet
  (`auto`, 1% threshold) that fails a pull request making overall coverage
  worse, with an 80% target on the diff itself.

### Fixed

- `WHERE` on non-key columns, and `OR`, `NOT`, `!=` and arithmetic within it,
  execute as an index scan plus filter (or a full scan) instead of failing with
  "unimplemented WHERE". A tuple comparison against a shorter index no longer
  panics, deeply nested expressions are rejected instead of overflowing the
  stack, and `SET a=1,b=2` without spaces parses.
- A panic in a request handler returns `Internal` instead of killing the server
  — and, in a cluster, every replica.
- `Delete` of a missing key returns `deleted=false` instead of `NotFound`, so
  retries are safe. `Range` can scan a prefix or the whole keyspace via empty
  bounds, and honours `limit` and `keys_only`.
- A not-leader response names the leader's dialable gRPC address instead of its
  Raft transport port. Raft failures map to `Unavailable` or `DeadlineExceeded`
  rather than a blanket `Internal`, and writes honour the client's deadline.
- Reading a key while the background compactor retires an SSTable no longer
  races on, or reads from, a closed file. Retired tables are reference counted
  and deleted only once the last open transaction lets go.
- A memtable flush no longer blocks all writers for the duration of the SSTable
  write and `fsync`, and calling `Compact()` concurrently from more than one
  goroutine can no longer skip or double-install an SSTable.
- Background compaction failures are surfaced instead of silently discarded:
  recorded on the store, readable via `LastCompactError`, and forwarded to an
  optional error callback (`WithErrorLogger`). Writes committed through a
  transaction also invalidate the read cache, and operations attempted after
  `Close` return an error instead of hanging.
- A write-ahead log reset that did not reach disk could resurrect deleted keys
  and superseded values on the next recovery. Log records now carry a sequence
  number checked against a durable floor, so stale records are never replayed.
- A database whose metadata was damaged in both copies no longer opens silently
  as an empty store and overwrites its own data files; it fails to open with a
  distinct error. A single damaged copy still recovers as before.
- Installing a Raft snapshot no longer deletes a node's live data before the
  replacement is in place. The swap stages, fsyncs, and rolls back, so a failed
  restore leaves the original data intact and the node still serving.
- Departed and failed cluster nodes are removed from the Raft configuration, so
  rolling replacements no longer accumulate dead voters until the cluster loses
  quorum.
- Cluster nodes compact their write-ahead log and memtable instead of growing
  without bound.
- Configuration is decoded into a typed struct and validated, so a misspelled
  key such as `serve.grpc.adr`, an unparseable address, an inconsistent cache
  size, or a bad `--log-format` fails loudly instead of silently falling back to
  defaults. Each subcommand applies only its own configuration.

### Security

- Table schemas live in a reserved, versioned metadata keyspace, so a raw
  key-value client can no longer overwrite them.
- Optional TLS for the Raft transport and the node-to-node join client, and
  Serf gossip encryption via `--serf-encrypt-key`.
- Optional TLS and mTLS on the gRPC endpoints, with shared-secret or
  client-certificate authentication on the cluster membership RPCs.
- The `gosec` CI job is blocking. It previously ran with `-no-fail`, so no
  security finding could break a build. Accepted findings are recorded with a
  written rationale in `.github/gosec.json` instead of being ignored wholesale.

### Removed

- Stray watermark comments stripped from all 25 Go source files.

<!--
Release checklist, for whoever cuts the first tag:

1. Move the entries above into a new `## [X.Y.Z] - YYYY-MM-DD` section and leave
   an empty `## [Unreleased]`.
2. Add the comparison links at the bottom of this file.
3. `git tag -a vX.Y.Z -m 'vX.Y.Z' && git push origin vX.Y.Z`.
   The Release workflow does the rest: goreleaser publishes the archives and
   checksums, and a multi-arch image is pushed to ghcr.io.
-->

[Unreleased]: https://github.com/teddymalhan/pallasdb/commits/main
