# Durability model

What survives a crash, stated precisely. The short version — "crash-safe
persistence with fsync" — was true on Linux and macOS and quietly false
elsewhere, which is exactly the kind of claim this page exists to prevent.

## The write path

1. A write is appended to the write-ahead log as a record carrying its payload,
   a CRC32 checksum, and a sequence number.
2. The log file is fsynced.
3. The write is applied to the in-memory memtable and becomes visible to
   readers.

A crash after step 2 is recoverable: replay reads records in order, verifies
each checksum, and stops at the first record that fails. A crash before step 2
loses the write, and the caller never saw success.

Sequence numbers are checked against a durable floor on replay, so a log reset
that did not itself reach disk cannot resurrect deleted keys or superseded
values from a stale region of the file.

## Record size limit

A single entry is capped at `db.MaxEntrySize`. The limit is enforced when the
record is written, not only when it is read back — an over-large entry written
by an older build is what used to make a store permanently unopenable.

## Metadata

The set of live SSTables is tracked in two metadata files written alternately,
so a crash mid-update always leaves one intact copy. Each copy is checksummed.

- One copy damaged: the other is used, and recovery is transparent.
- Both copies damaged: opening **fails** with a distinct error. It does not
  succeed as an empty database. That distinction matters — the old behaviour
  opened a destroyed store as if it were a fresh one and then overwrote the data
  files that were still on disk.

## Platform differences

| Guarantee | unix (Linux, macOS, BSD) | Windows and other non-unix |
|---|---|---|
| `fsync` of data and log files | yes | yes (`FlushFileBuffers`) |
| `fsync` of the containing directory | yes | **no** |

Directory fsync is what makes a *file creation* durable, as opposed to a file's
*contents*. Without it, a crash immediately after a new SSTable or log file is
created can leave the directory entry missing even though the file's bytes were
flushed.

This gap is not hidden. It is reported programmatically:

- `db.DirSyncSupported` — `true` on unix, `false` elsewhere.
- `db.ErrDirSyncUnsupported` — returned where a caller asked for a guarantee the
  platform cannot provide.

Both platform paths are compiled and tested in CI: the test matrix runs on
Linux, macOS, and Windows, because `db/os_unix.go` and `db/os_other.go` are
selected by build tag and a Linux-only run never compiles the second one.

## What is not guaranteed

- **Disk lies.** If the drive or its cache acknowledges an fsync it did not
  honour, nothing above helps.
- **Torn writes below the record level.** The checksum detects them; it does not
  repair them. The affected record and everything after it in that log is
  dropped on replay.
- **Cluster durability is a separate question.** A write acknowledged by the
  Raft leader is durable once a quorum has committed it; see
  [Architecture](architecture.md) and [`cluster/README.md`](../cluster/README.md).
