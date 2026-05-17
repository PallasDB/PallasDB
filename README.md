implementing a KV store in golang with lsm trees, sstables and concurrent access.

## System Design

```mermaid
flowchart TD
    App["Application / SQL Client"]

    subgraph SQL["SQL Layer"]
        Parser["SQL Parser\nCREATE · INSERT · SELECT · UPDATE · DELETE"]
        Executor["Query Executor\nrange scans · index lookup · expression eval"]
        Schema["Table Schema\nprimary + secondary indices\nkey encoding"]
    end

    subgraph TX["Transaction Layer (MVCC)"]
        DBTX["DBTX\ndatabase transaction"]
        KVTX["KVTX\nsnapshot · local SortedArray · conflict check"]
        History["Commit History\ntimestamp-keyed log\nfor conflict detection & GC"]
    end

    subgraph Mem["In-Memory (MemTable)"]
        SortedArray["SortedArray\nbinary-search sorted buffer\ntombstone deletes"]
    end

    subgraph Persist["Persistence"]
        WAL["Write-Ahead Log\nkv_log\nCRC32 · key/val/op entries"]
        Meta["Dual-Slot Metadata\nmeta0 · meta1\natomic version swap"]
        subgraph LSM["LSM Tree (SSTables)"]
            L0["sstable_1"]
            L1["sstable_2"]
            L2["sstable_3 …"]
        end
    end

    subgraph Compact["Compaction"]
        CompactLog["compactLog()\nflush MemTable → new SSTable\nwhen size ≥ threshold"]
        CompactSST["compactSSTable()\nmerge adjacent levels\nwhen growth factor exceeded"]
    end

    subgraph Read["Read Path (multi-level iterator)"]
        MergedIter["MergedSortedKVIter\nmerge-sort across all levels\nskip tombstones · enforce range bounds"]
    end

    App --> Parser
    Parser --> Executor
    Executor --> Schema
    Schema --> DBTX
    DBTX --> KVTX

    KVTX -->|"commit: write WAL"| WAL
    KVTX -->|"commit: update MemTable"| SortedArray
    KVTX -->|"conflict check"| History

    WAL -->|"recovery: rebuild MemTable"| SortedArray

    SortedArray --> CompactLog
    CompactLog --> L0
    L0 --> CompactSST
    CompactSST --> L1
    L1 --> CompactSST
    CompactSST --> L2
    CompactLog & CompactSST -->|"update active slot"| Meta

    KVTX -->|"read: open snapshot"| MergedIter
    MergedIter --> SortedArray
    MergedIter --> L0
    MergedIter --> L1
    MergedIter --> L2
```

### Component Summary

| Component | Role |
|---|---|
| **SQL Parser** | Tokenises and parses SQL into an AST; supports `CREATE TABLE`, `INSERT`, `SELECT`, `UPDATE`, `DELETE` with full expression grammar |
| **Query Executor** | Translates AST into ranged KV scans using schema-aware key encoding; evaluates WHERE expressions |
| **KVTX / DBTX** | Snapshot-isolated transactions; each TX writes to a local `SortedArray` and merges on commit |
| **Conflict Detection** | Timestamp-keyed history log detects write-write conflicts; returns `ErrTXConflict` on clash |
| **MemTable** | In-memory `SortedArray` (binary search, tombstone deletes); absorbed on every commit |
| **Write-Ahead Log** | Append-only log with CRC32 checksums; replayed on startup to recover the MemTable |
| **SSTables** | Immutable on-disk sorted files; index block + data block; binary search via `Seek()` |
| **LSM Compaction** | Background goroutine flushes MemTable and merges SSTable levels using a growth-factor heuristic |
| **Dual-Slot Metadata** | Two metadata files (`meta0`/`meta1`) swapped atomically by version number; no fsync needed |
| **MergedSortedKVIter** | Merge-sort iterator across all levels; lowest key wins; tombstones suppress older entries |
