package cluster

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/hashicorp/raft"
	"github.com/teddymalhan/pallasdb/pallasdb"
)

// FSMResult is returned from Apply so the gRPC caller can surface errors.
type FSMResult struct {
	Updated bool
	Err     error
}

// FSM wraps a pallasdb.KV and implements raft.FSM.
// It guards the store with an RWMutex so Restore can atomically swap it.
type FSM struct {
	mu      sync.RWMutex
	store   *pallasdb.KV
	dirpath string
}

func NewFSM(store *pallasdb.KV, dirpath string) *FSM {
	return &FSM{store: store, dirpath: dirpath}
}

// Apply is called by the Raft library after a log entry is committed.
// It must be deterministic and must not return an error (that would crash Raft).
func (f *FSM) Apply(log *raft.Log) interface{} {
	cmd, err := DecodeCommand(log.Data)
	if err != nil {
		return &FSMResult{Err: fmt.Errorf("decode command: %w", err)}
	}

	f.mu.RLock()
	defer f.mu.RUnlock()

	switch cmd.Op {
	case OpPut:
		mode := pallasdb.UpdateMode(cmd.Mode)
		if mode < pallasdb.ModeUpsert || mode > pallasdb.ModeUpdate {
			return &FSMResult{Err: fmt.Errorf("invalid update mode: %d", cmd.Mode)}
		}
		updated, err := f.store.SetEx(cmd.Key, cmd.Val, mode)
		return &FSMResult{Updated: updated, Err: err}
	case OpDel:
		deleted, err := f.store.Del(cmd.Key)
		return &FSMResult{Updated: deleted, Err: err}
	default:
		return &FSMResult{Err: fmt.Errorf("unknown op: %s", cmd.Op)}
	}
}

// Snapshot returns a consistent snapshot of the current KV state.
func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	store := f.store
	f.mu.RUnlock()

	iter, cleanup, err := store.IterAll()
	if err != nil {
		return nil, fmt.Errorf("iter all: %w", err)
	}
	return &FSMSnapshot{iter: iter, cleanup: cleanup}, nil
}

// Restore replaces the entire FSM state with the snapshot stream.
// Uses a temp directory for atomicity: write to dirpath+".restore", then rename.
func (f *FSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()

	pairs, err := readSnapshot(rc)
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}

	restoreDir := f.dirpath + ".restore"
	_ = os.RemoveAll(restoreDir)

	fresh, err := pallasdb.NewKV(restoreDir, pallasdb.WithAutoCompact(false))
	if err != nil {
		return fmt.Errorf("open restore store: %w", err)
	}

	for _, p := range pairs {
		if _, err := fresh.SetEx(p.Key, p.Val, pallasdb.ModeUpsert); err != nil {
			_ = fresh.Close()
			_ = os.RemoveAll(restoreDir)
			return fmt.Errorf("restore key: %w", err)
		}
	}

	if err := fresh.Close(); err != nil {
		_ = os.RemoveAll(restoreDir)
		return fmt.Errorf("close restore store: %w", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.store.Close(); err != nil {
		_ = os.RemoveAll(restoreDir)
		return fmt.Errorf("close existing store: %w", err)
	}
	if err := os.RemoveAll(f.dirpath); err != nil {
		return fmt.Errorf("remove old data dir: %w", err)
	}
	if err := os.Rename(restoreDir, f.dirpath); err != nil {
		return fmt.Errorf("rename restore dir: %w", err)
	}

	f.store, err = pallasdb.NewKV(f.dirpath, pallasdb.WithAutoCompact(false))
	if err != nil {
		return fmt.Errorf("reopen restored store: %w", err)
	}
	return nil
}

// Get reads a key from the store under RLock so Restore cannot race.
func (f *FSM) Get(key []byte) ([]byte, bool, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.store.Get(key)
}

// Del deletes a key directly (single-node path only; cluster path goes through Apply).
func (f *FSM) Del(key []byte) (bool, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.store.Del(key)
}

// NewTX opens a transaction and returns the transaction and a release function.
// The caller must invoke release() when done.
func (f *FSM) NewTX() (*pallasdb.KVTX, func()) {
	f.mu.RLock()
	tx := f.store.NewTX()
	return tx, func() {
		tx.Abort()
		f.mu.RUnlock()
	}
}

// Close shuts down the underlying KV store.
func (f *FSM) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.store.Close()
}
