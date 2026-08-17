package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/raft"
	"github.com/teddymalhan/pallasdb/db"
)

// Suffixes of the directories Restore juggles next to the live data directory.
const (
	restoreDirSuffix = ".restore"
	oldDirSuffix     = ".old"
)

// appliedIndexPollInterval is how often a blocked waiter re-reads the optional
// raft index source. Raft never routes no-op, barrier or (absent a
// ConfigurationStore) configuration entries to an FSM, so those indexes would
// otherwise never be observed and a waiter could block until the next write.
const appliedIndexPollInterval = 5 * time.Millisecond

// ErrFSMClosed is returned by reads once the FSM has been closed.
var ErrFSMClosed = errors.New("cluster: FSM is closed")

// FSMResult is returned from Apply so the gRPC caller can surface errors.
//
// Err is only ever a deterministic rejection: every replica decoding the same
// entry produces the same Err, so returning it to the caller cannot diverge the
// cluster. Non-deterministic failures never reach here — they abort the process.
type FSMResult struct {
	Updated bool
	Err     error
}

// AppliedIndexSource reports the highest raft log index the raft node itself has
// applied. *raft.Raft satisfies it.
type AppliedIndexSource interface {
	AppliedIndex() uint64
}

// FSM wraps a db.KV and implements raft.FSM.
// It guards the store with an RWMutex so Restore can swap it.
//
// Invariant: outside Close, f.store is always an open store. Restore either
// installs a working store, rolls back to the previous one, or aborts the
// process; it never leaves a closed store behind for readers to use.
type FSM struct {
	mu      sync.RWMutex
	store   *db.KV
	closed  bool
	dirpath string
	kvOpts  []db.KVOption

	// renameFn is a seam for tests to inject rename failures.
	renameFn func(oldpath, newpath string) error

	applied atomic.Uint64

	idxMu sync.Mutex
	idxCh chan struct{} // closed and replaced whenever applied advances
	idxSr AppliedIndexSource
}

func NewFSM(store *db.KV, dirpath string, kvOpts ...db.KVOption) *FSM {
	return &FSM{
		store:    store,
		dirpath:  dirpath,
		kvOpts:   kvOpts,
		renameFn: os.Rename,
		idxCh:    make(chan struct{}),
	}
}

// storeOptions returns the options for a store this FSM opens. Compaction is on
// by default so a long-lived cluster node does not grow its WAL without bound;
// caller-supplied options still win.
func (f *FSM) storeOptions() []db.KVOption {
	opts := make([]db.KVOption, 0, len(f.kvOpts)+1)
	opts = append(opts, db.WithAutoCompact(true))
	return append(opts, f.kvOpts...)
}

// fatal aborts the process. It is called when this replica cannot apply a
// committed entry for a reason other replicas will not hit — an I/O error, a
// command it cannot decode, an op it does not know. Continuing would silently
// diverge this state machine from the rest of the cluster, which is the exact
// failure Raft exists to prevent; crashing lets the node recover by replaying
// the log or installing a snapshot.
func fatal(format string, args ...any) {
	panic("cluster: FSM cannot continue: " + fmt.Sprintf(format, args...))
}

// Apply is called by the Raft library after a log entry is committed.
// It must be deterministic: a deterministic rejection is returned in FSMResult,
// anything else aborts the process rather than diverging from its peers.
func (f *FSM) Apply(log *raft.Log) interface{} {
	cmd, err := DecodeCommand(log.Data)
	if err != nil {
		// A committed entry this node cannot decode means a version mismatch
		// with a newer leader or a corrupt log. Skipping it would leave this
		// replica permanently behind with no detection.
		fatal("decode raft entry %d: %v", log.Index, err)
	}

	res := f.applyCommand(log.Index, cmd)
	f.advanceApplied(log.Index)
	return res
}

func (f *FSM) applyCommand(index uint64, cmd Command) *FSMResult {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if f.closed {
		fatal("raft entry %d applied to a closed FSM", index)
	}

	switch cmd.Op {
	case OpPut:
		mode, err := updateMode(cmd.Mode)
		if err != nil {
			return &FSMResult{Err: err}
		}
		updated, err := f.store.SetEx(cmd.Key, cmd.Val, mode)
		if err != nil {
			fatal("raft entry %d: put: %v", index, err)
		}
		return &FSMResult{Updated: updated}
	case OpDel:
		deleted, err := f.store.Del(cmd.Key)
		if err != nil {
			fatal("raft entry %d: delete: %v", index, err)
		}
		return &FSMResult{Updated: deleted}
	case OpBatch:
		return f.applyBatch(index, cmd.Batch)
	default:
		// An unknown op means a newer leader replicated something this binary
		// does not understand. Ignoring it is the worst possible outcome.
		fatal("raft entry %d: unknown op %q", index, cmd.Op)
		return nil
	}
}

// applyBatch applies every mutation in one db transaction, so the batch is all
// or nothing.
func (f *FSM) applyBatch(index uint64, muts []Mutation) *FSMResult {
	tx := f.store.NewTX()
	updated := false
	for i, m := range muts {
		switch m.Op {
		case OpPut:
			mode, err := updateMode(m.Mode)
			if err != nil {
				tx.Abort()
				return &FSMResult{Err: fmt.Errorf("batch mutation %d: %w", i, err)}
			}
			u, err := tx.SetEx(m.Key, m.Val, mode)
			if err != nil {
				tx.Abort()
				fatal("raft entry %d: batch mutation %d: put: %v", index, i, err)
			}
			updated = updated || u
		case OpDel:
			d, err := tx.Del(m.Key)
			if err != nil {
				tx.Abort()
				fatal("raft entry %d: batch mutation %d: delete: %v", index, i, err)
			}
			updated = updated || d
		default:
			tx.Abort()
			fatal("raft entry %d: batch mutation %d: unknown op %q", index, i, m.Op)
		}
	}
	if err := tx.Commit(); err != nil {
		fatal("raft entry %d: commit batch of %d mutations: %v", index, len(muts), err)
	}
	return &FSMResult{Updated: updated}
}

// updateMode validates a wire update mode. An out-of-range mode is a
// deterministic rejection: every replica reaches the same conclusion.
func updateMode(mode int) (db.UpdateMode, error) {
	m := db.UpdateMode(mode)
	if m < db.ModeUpsert || m > db.ModeUpdate {
		return 0, fmt.Errorf("invalid update mode: %d", mode)
	}
	return m, nil
}

// AppliedIndex returns the highest raft log index this FSM has applied. A
// returned index N means every entry up to N is visible to Get.
func (f *FSM) AppliedIndex() uint64 { return f.applied.Load() }

// ObserveAppliedIndex advances the applied index without applying anything. It
// exists for indexes raft never routes to an FSM (no-ops, barriers).
func (f *FSM) ObserveAppliedIndex(idx uint64) { f.advanceApplied(idx) }

// SetAppliedIndexSource wires the raft node whose applied index a blocked
// waiter should poll, so WaitForAppliedIndex cannot hang on an index that will
// never reach Apply. Passing nil disables polling.
func (f *FSM) SetAppliedIndexSource(src AppliedIndexSource) {
	f.idxMu.Lock()
	f.idxSr = src
	f.idxMu.Unlock()
}

// StoreConfiguration records the index of a committed configuration entry.
// Implementing raft.ConfigurationStore keeps the applied index moving for
// membership changes, which raft does not route through Apply.
func (f *FSM) StoreConfiguration(index uint64, _ raft.Configuration) {
	f.advanceApplied(index)
}

// WaitForAppliedIndex blocks until AppliedIndex() >= idx, or until ctx is done.
// It returns nil once the target is reached and ctx.Err() otherwise; it never
// returns any other error, and it is safe for many concurrent waiters.
func (f *FSM) WaitForAppliedIndex(ctx context.Context, idx uint64) error {
	if f.applied.Load() >= idx {
		return nil
	}

	var tick <-chan time.Time
	f.idxMu.Lock()
	hasSource := f.idxSr != nil
	f.idxMu.Unlock()
	if hasSource {
		ticker := time.NewTicker(appliedIndexPollInterval)
		defer ticker.Stop()
		tick = ticker.C
	}

	for {
		// Take the wakeup channel before re-checking so an advance between the
		// check and the select cannot be missed: the channel is already closed.
		f.idxMu.Lock()
		ch, src := f.idxCh, f.idxSr
		f.idxMu.Unlock()

		if f.applied.Load() >= idx {
			return nil
		}
		if src != nil {
			f.advanceApplied(src.AppliedIndex())
			if f.applied.Load() >= idx {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		case <-tick:
		}
	}
}

// advanceApplied raises the applied index to idx and wakes every waiter. The
// index never moves backwards.
func (f *FSM) advanceApplied(idx uint64) {
	for {
		cur := f.applied.Load()
		if idx <= cur {
			return
		}
		if f.applied.CompareAndSwap(cur, idx) {
			break
		}
	}
	f.idxMu.Lock()
	ch := f.idxCh
	f.idxCh = make(chan struct{})
	f.idxMu.Unlock()
	close(ch)
}

// Snapshot returns a consistent snapshot of the current KV state.
func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if f.closed {
		return nil, ErrFSMClosed
	}
	iter, cleanup, err := f.store.IterAll()
	if err != nil {
		return nil, fmt.Errorf("iter all: %w", err)
	}
	return &FSMSnapshot{iter: iter, cleanup: cleanup}, nil
}

// Restore replaces the entire FSM state with the snapshot stream.
//
// The snapshot is streamed into a sibling directory and fully validated
// (framing, bounds, CRC) before anything live is touched. The swap then moves
// the old directory aside, moves the new one in, fsyncs the parent directory
// and only then drops the old one, rolling back if any step fails.
func (f *FSM) Restore(rc io.ReadCloser) error {
	defer func() { _ = rc.Close() }()

	restoreDir := f.dirpath + restoreDirSuffix
	oldDir := f.dirpath + oldDirSuffix
	if err := os.RemoveAll(restoreDir); err != nil {
		return fmt.Errorf("clear stale restore dir: %w", err)
	}
	if err := os.RemoveAll(oldDir); err != nil {
		return fmt.Errorf("clear stale backup dir: %w", err)
	}

	fresh, err := db.NewKV(restoreDir, f.storeOptions()...)
	if err != nil {
		return fmt.Errorf("open restore store: %w", err)
	}
	err = readSnapshot(rc, func(key, val []byte) error {
		if _, err := fresh.SetEx(key, val, db.ModeUpsert); err != nil {
			return fmt.Errorf("restore key: %w", err)
		}
		return nil
	})
	if err != nil {
		_ = fresh.Close()
		_ = os.RemoveAll(restoreDir)
		return fmt.Errorf("read snapshot: %w", err)
	}
	if err := fresh.Close(); err != nil {
		_ = os.RemoveAll(restoreDir)
		return fmt.Errorf("close restore store: %w", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	return f.swapIn(restoreDir, oldDir)
}

// swapIn installs restoreDir as the live data directory. Callers must hold
// f.mu for writing.
func (f *FSM) swapIn(restoreDir, oldDir string) error {
	if f.closed {
		_ = os.RemoveAll(restoreDir)
		return ErrFSMClosed
	}
	if err := f.store.Close(); err != nil {
		_ = os.RemoveAll(restoreDir)
		return fmt.Errorf("close existing store: %w", err)
	}

	// From here the live store is closed. Every path below must reopen a
	// working store or abort the process.
	stashed, err := f.stashDir(f.dirpath, oldDir)
	if err != nil {
		return f.recover(restoreDir, err)
	}

	if err := f.rename(restoreDir, f.dirpath); err != nil {
		err = fmt.Errorf("install restored data dir: %w", err)
		if stashed {
			if rbErr := f.rename(oldDir, f.dirpath); rbErr != nil {
				fatal("restore left no data directory at %s: %v; rollback from %s failed: %v",
					f.dirpath, err, oldDir, rbErr)
			}
		}
		return f.recover(restoreDir, err)
	}

	// Make the rename itself durable before the old copy is destroyed.
	syncErr := syncParentDir(f.dirpath)

	store, err := db.NewKV(f.dirpath, f.storeOptions()...)
	if err != nil {
		// The restored data is in place but unusable, and the previous data is
		// either gone or stale. There is nothing safe left to serve.
		fatal("reopen restored store at %s: %v", f.dirpath, err)
	}
	f.store = store

	if syncErr != nil {
		// The data is correct in place but the rename may not survive a crash;
		// keep the backup for recovery and report it.
		return fmt.Errorf("fsync data dir parent: %w", syncErr)
	}
	if stashed {
		if err := os.RemoveAll(oldDir); err != nil {
			return fmt.Errorf("remove backup data dir: %w", err)
		}
	}
	return nil
}

// stashDir moves the live data directory aside. A missing directory is not an
// error: a node restoring before it ever wrote has none.
func (f *FSM) stashDir(dirpath, oldDir string) (bool, error) {
	if _, err := os.Stat(dirpath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat data dir: %w", err)
	}
	if err := f.rename(dirpath, oldDir); err != nil {
		return false, fmt.Errorf("stash old data dir: %w", err)
	}
	return true, nil
}

// recover reopens the store from the live data directory after a failed swap
// and returns cause. If the store cannot be reopened the node cannot serve
// anything, so it aborts instead of leaving a closed store in place.
func (f *FSM) recover(restoreDir string, cause error) error {
	_ = os.RemoveAll(restoreDir)
	store, err := db.NewKV(f.dirpath, f.storeOptions()...)
	if err != nil {
		fatal("reopen store at %s after failed restore (%v): %v", f.dirpath, cause, err)
	}
	f.store = store
	return cause
}

func (f *FSM) rename(oldpath, newpath string) error {
	if f.renameFn != nil {
		return f.renameFn(oldpath, newpath)
	}
	return os.Rename(oldpath, newpath)
}

// syncParentDir fsyncs the directory containing path so a rename into it is
// durable.
func syncParentDir(path string) error {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// Get reads a key from the store under RLock so Restore cannot race.
func (f *FSM) Get(key []byte) (val []byte, ok bool, err error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return nil, false, ErrFSMClosed
	}
	return f.store.Get(key)
}

// NewTX opens a transaction and returns the transaction and a release function.
// The caller must invoke release() when done.
func (f *FSM) NewTX() (tx *db.KVTX, release func()) {
	f.mu.RLock()
	tx = f.store.NewTX()
	return tx, func() {
		tx.Abort()
		f.mu.RUnlock()
	}
}

// Close shuts down the underlying KV store. It is idempotent.
func (f *FSM) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	return f.store.Close()
}
