package db

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/dgraph-io/ristretto/v2"
)

const (
	defaultLogThreshold = 1000
	defaultGrowthFactor = 2.0
	sstablePrefix       = "sstable_"
)

func sstableName(version uint64) string {
	return sstablePrefix + strconv.FormatUint(version, 10)
}

type KVOptions struct {
	Dirpath string
	// LSM-Tree
	// LogShreshold is kept for compatibility. Prefer LogThreshold.
	LogShreshold int
	LogThreshold int
	GrowthFactor float32
	AutoCompact  bool
	// Cache (zero value = disabled)
	CacheEnabled     bool
	CacheMaxCost     int64
	CacheNumCounters int64
	// OnError receives errors raised on background goroutines. Optional.
	OnError func(error)
}

type KVOption func(*KVOptions) error

func WithLogThreshold(n int) KVOption {
	return func(opts *KVOptions) error {
		if n <= 0 {
			return errors.New("log threshold must be positive")
		}
		opts.LogThreshold = n
		return nil
	}
}

func WithGrowthFactor(f float32) KVOption {
	return func(opts *KVOptions) error {
		if f < 2.0 {
			return errors.New("growth factor must be at least 2")
		}
		opts.GrowthFactor = f
		return nil
	}
}

func WithAutoCompact(enabled bool) KVOption {
	return func(opts *KVOptions) error {
		opts.AutoCompact = enabled
		return nil
	}
}

// WithErrorLogger installs a sink for errors raised on background goroutines,
// which have no caller to return them to (today: the auto-compaction thread).
// The db package intentionally carries no logging dependency, so embedders plug
// in their own. The callback may be invoked from any goroutine and must not
// call back into the KV.
func WithErrorLogger(fn func(error)) KVOption {
	return func(opts *KVOptions) error {
		if fn == nil {
			return errors.New("error logger must not be nil")
		}
		opts.OnError = fn
		return nil
	}
}

func WithCache(maxCostBytes, numCounters int64) KVOption {
	return func(opts *KVOptions) error {
		if maxCostBytes <= 0 {
			return errors.New("cache max cost must be positive")
		}
		if numCounters <= 0 {
			return errors.New("cache num counters must be positive")
		}
		opts.CacheEnabled = true
		opts.CacheMaxCost = maxCostBytes
		opts.CacheNumCounters = numCounters
		return nil
	}
}

func NewKV(dirpath string, opts ...KVOption) (*KV, error) {
	options := KVOptions{Dirpath: dirpath}
	for _, opt := range opts {
		if err := opt(&options); err != nil {
			return nil, err
		}
	}
	kv := &KV{Options: options}
	return kv, kv.Open()
}

// CacheKVOpts returns a WithCache option if caching is configured, or nil otherwise.
// Useful for passing cache settings to subsystems that create their own KV stores.
func (opts *KVOptions) CacheKVOpts() []KVOption {
	if !opts.CacheEnabled || opts.CacheMaxCost == 0 {
		return nil
	}
	return []KVOption{WithCache(opts.CacheMaxCost, opts.CacheNumCounters)}
}

func (opts *KVOptions) setDefaults() {
	if opts.LogThreshold <= 0 {
		opts.LogThreshold = opts.LogShreshold
	}
	if opts.LogThreshold <= 0 {
		opts.LogThreshold = defaultLogThreshold
	}
	opts.LogShreshold = opts.LogThreshold
	if opts.GrowthFactor < 2.0 {
		opts.GrowthFactor = defaultGrowthFactor
	}
}

// KV is an LSM-tree key/value store: a write-ahead log, an in-memory sorted
// memtable and a stack of immutable on-disk SSTables (kv.main, newest first).
//
// # The memtable invariant
//
// NewTX publishes kv.mem to a transaction *by value*: the transaction keeps the
// slice headers that were live at that moment and reads through them for its
// whole lifetime. kv.mem may therefore only be mutated in ways that leave the
// entries in [0, len) untouched:
//
//   - appending (see appendSortedArray) — a snapshot's shorter len hides the new
//     entries and no existing element is rewritten; or
//   - wholesale replacement by a freshly built SortedArray.
//
// In-place edits (SortedArray.Set/Del over an existing index, or truncating and
// re-pushing) are only legal on an array that was never published, i.e.
// tx.updates. That is why SortedArray.Clear drops its backing arrays instead of
// reslicing them, and why flushing swaps in a brand new kv.mem rather than
// clearing the old one. updateMem asserts the invariant on every commit.
//
// # SSTable lifetime
//
// kv.main holds *SortedFile pointers, never values: a SortedFile owns an fd and
// is reference counted. kv.main holds one reference per table and every
// transaction snapshot holds one more, so compaction can retire a table while
// older snapshots keep reading it — the fd is closed and the file unlinked only
// once the last reference goes away.
type KV struct {
	Options KVOptions
	// metadata
	meta KVMetaStore
	// version names the next SSTable; guarded by mu, only advanced by a
	// compaction (which is serialised by the compact mutex).
	version uint64
	// data
	log Log
	// mem is the live memtable. imm is a memtable frozen by an in-flight flush;
	// it stays visible to readers, between mem and main, until its SSTable is
	// installed. Both are guarded by mu.
	mem  SortedArray
	imm  *SortedArray
	main []*SortedFile
	// cache is installed by Open and torn down by Close, after every
	// transaction has finished; readers may access it without a lock.
	cache *ristretto.Cache[string, []byte]
	// transactions
	snapshot uint64
	history  []UpdatedKey
	ongoing  []*KVTX
	// synchronization
	mu         sync.Mutex
	commit     sync.Mutex
	compact    sync.Mutex // at most one compaction at a time
	compactErr error      // guarded by mu
	closed     bool       // guarded by mu
	updated    chan struct{}
	closing    chan struct{}
	threads    sync.WaitGroup
}

// ErrKVClosed is returned by operations attempted on a closed store.
var ErrKVClosed = errors.New("KV is closed")

// ErrTXDone is returned by a transaction that has already been committed or
// aborted; its snapshot no longer exists.
var ErrTXDone = errors.New("TX has already finished")

type UpdatedKey struct {
	snapshot uint64
	key      []byte
}

type KVTX struct {
	snapshot uint64
	target   interface {
		applyTX(*KVTX) error
		abortTX(*KVTX)
	}
	updates  SortedArray
	levels   MergedSortedKV
	mainSnap []*SortedFile
	// err is non-nil when the transaction is unusable: it was never started
	// (ErrKVClosed) or it has already finished (ErrTXDone).
	err error
}

// Err reports why the transaction is unusable, or nil while it is live.
func (tx *KVTX) Err() error { return tx.err }

func (kv *KV) NewTX() *KVTX {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	if kv.closed {
		// target is still set so Abort/Commit stay safe; untrackTXSync ignores a
		// transaction that was never tracked.
		return &KVTX{target: kv, err: ErrKVClosed}
	}

	tx := &KVTX{snapshot: kv.snapshot, target: kv}
	mem := kv.mem // copy! see "the memtable invariant" on KV
	tx.mainSnap = make([]*SortedFile, len(kv.main))
	copy(tx.mainSnap, kv.main)
	tx.levels = make(MergedSortedKV, 0, len(tx.mainSnap)+3)
	tx.levels = append(tx.levels, &tx.updates, &mem)
	if kv.imm != nil {
		// A flush is in flight: the frozen memtable is older than mem and newer
		// than every SSTable.
		tx.levels = append(tx.levels, kv.imm)
	}
	for _, file := range tx.mainSnap {
		// Safe under kv.mu: a table listed in kv.main has at least kv.main's own
		// reference, so it cannot reach zero while we add ours.
		file.acquire()
		tx.levels = append(tx.levels, file)
	}
	kv.ongoing = append(kv.ongoing, tx)
	kv.threads.Add(1)
	return tx
}

func (tx *KVTX) Abort() { tx.target.abortTX(tx) }

func (kv *KV) abortTX(tx *KVTX) { kv.untrackTXSync(tx) }

func (kv *KV) untrackTXSync(tx *KVTX) {
	kv.mu.Lock()
	idx := slices.Index(kv.ongoing, tx)
	if idx < 0 {
		// Never tracked (KV was closed) or already finished; releasing the
		// snapshot twice would corrupt the SSTable reference counts.
		kv.mu.Unlock()
		return
	}
	kv.ongoing = slices.Delete(kv.ongoing, idx, idx+1)
	if len(kv.ongoing) > 0 {
		oldest := kv.ongoing[0].snapshot
		kv.history = slices.DeleteFunc(kv.history, func(h UpdatedKey) bool {
			return h.snapshot < oldest
		})
	} else {
		kv.history = kv.history[:0]
	}
	snap := tx.mainSnap
	tx.mainSnap, tx.levels, tx.err = nil, nil, ErrTXDone
	kv.mu.Unlock()

	for _, file := range snap {
		if err := file.release(); err != nil {
			kv.recordError(fmt.Errorf("release sstable %s: %w", file.FileName, err))
		}
	}
	// Last: Close waits on this counter and must not observe zero before the
	// snapshot references above are gone.
	kv.threads.Add(-1)
}

func (tx *KVTX) Commit() error { return tx.target.applyTX(tx) }

var ErrTXConflict = errors.New("TX is conflict with another TX")

func (kv *KV) applyTXSync(tx *KVTX) error {
	if tx.err != nil {
		return tx.err
	}

	kv.commit.Lock()
	defer kv.commit.Unlock()
	defer kv.untrackTXSync(tx)

	if tx.updates.Size() == 0 {
		return nil
	}
	if kv.checkTXConflict(tx) {
		return ErrTXConflict
	}
	if err := kv.updateLog(tx); err != nil {
		return err
	}

	kv.mu.Lock()
	kv.updateMem(tx)
	kv.updateHistory(tx)
	kv.mu.Unlock()

	kv.invalidateCache(tx)
	return nil
}

// invalidateCache drops every key the transaction touched from the read cache.
// Without it a KVTX commit (the path db.DB takes for every row write) would
// leave kv.Get serving the pre-commit value until the entry happened to be
// evicted.
func (kv *KV) invalidateCache(tx *KVTX) {
	if kv.cache == nil {
		return
	}
	iter, err := tx.updates.Iter()
	for ; err == nil && iter.Valid(); err = iter.Next() {
		kv.cache.Del(string(iter.Key()))
	}
	check(err == nil)
}

func (kv *KV) applyTX(tx *KVTX) error {
	if err := kv.applyTXSync(tx); err != nil {
		return err
	}
	if kv.Options.AutoCompact && kv.closing != nil {
		// kv.closing is created by Open and never reset to nil, so once the
		// store is closing this select falls through instead of blocking on a
		// compactor that has already exited.
		select {
		case kv.updated <- struct{}{}:
		case <-kv.closing:
		}
	}
	return nil
}

func (kv *KV) checkTXConflict(tx *KVTX) bool {
	kv.mu.Lock()
	if len(kv.history) == 0 {
		kv.mu.Unlock()
		return false
	}
	history := make([]UpdatedKey, len(kv.history))
	copy(history, kv.history)
	kv.mu.Unlock()

	iter, err := tx.updates.Iter()
	for ; err == nil && iter.Valid(); err = iter.Next() {
		key := iter.Key()
		for _, other := range history {
			if other.snapshot > tx.snapshot && bytes.Equal(other.key, key) {
				return true
			}
		}
	}
	check(err == nil)
	return false
}

func (kv *KV) updateLog(tx *KVTX) error {
	defer kv.log.ResetTX()
	iter, err := tx.updates.Iter()
	for ; err == nil && iter.Valid(); err = iter.Next() {
		op := EntryAdd
		if iter.Deleted() {
			op = EntryDel
		}
		err = kv.log.Write(&Entry{key: iter.Key(), val: iter.Val(), op: op})
		if err != nil {
			return err
		}
	}
	check(err == nil)
	return kv.log.Commit()
}

// updateMem folds the transaction's writes into the live memtable. Caller must
// hold kv.mu. See "the memtable invariant" on KV: the fast path only appends,
// and the slow path installs a freshly built array, so entries already visible
// to an open snapshot are never rewritten.
func (kv *KV) updateMem(tx *KVTX) {
	before := kv.mem
	if appendSortedArray(&kv.mem, &tx.updates) {
		check(!kv.mem.sharesBacking(&before) || kv.mem.Size() >= before.Size())
		return
	}

	merged := SortedArray{}
	iter, err := MergedSortedKV{&tx.updates, &kv.mem}.Iter()
	for ; err == nil && iter.Valid(); err = iter.Next() {
		merged.Push(iter.Key(), iter.Val(), iter.Deleted())
	}
	check(err == nil)
	kv.mem = merged
	check(!kv.mem.sharesBacking(&before))
}

func appendSortedArray(dst, src *SortedArray) bool {
	if src.Size() == 0 {
		return true
	}
	if dst.Size() != 0 && bytes.Compare(dst.keys[len(dst.keys)-1], src.keys[0]) >= 0 {
		return false
	}
	dst.keys = append(dst.keys, src.keys...)
	dst.vals = append(dst.vals, src.vals...)
	dst.deleted = append(dst.deleted, src.deleted...)
	return true
}

func (kv *KV) updateHistory(tx *KVTX) {
	kv.snapshot++
	if len(kv.ongoing) > 1 {
		iter, err := tx.updates.Iter()
		for ; err == nil && iter.Valid(); err = iter.Next() {
			kv.history = append(kv.history, UpdatedKey{kv.snapshot, iter.Key()})
		}
		check(err == nil)
	}
}

func (tx *KVTX) NewTX() *KVTX {
	inner := &KVTX{target: tx, err: tx.err}
	inner.levels = slices.Concat(MergedSortedKV{&inner.updates}, tx.levels)
	return inner
}

func (tx *KVTX) applyTX(inner *KVTX) error {
	if inner.err != nil {
		return inner.err
	}
	iter, err := inner.updates.Iter()
	for ; err == nil && iter.Valid(); err = iter.Next() {
		if iter.Deleted() {
			_, err = tx.updates.Del(iter.Key())
		} else {
			_, err = tx.updates.Set(iter.Key(), iter.Val())
		}
		check(err == nil)
	}
	check(err == nil)
	return nil
}

func (tx *KVTX) abortTX(*KVTX) {}

func (kv *KV) openCache() error {
	if !kv.Options.CacheEnabled || kv.Options.CacheMaxCost == 0 {
		return nil
	}
	c, err := ristretto.NewCache(&ristretto.Config[string, []byte]{
		NumCounters: kv.Options.CacheNumCounters,
		MaxCost:     kv.Options.CacheMaxCost,
		BufferItems: 64,
	})
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}
	kv.cache = c
	return nil
}

func (kv *KV) Open() (err error) {
	kv.Options.setDefaults()

	kv.mu.Lock()
	kv.closed = false
	kv.compactErr = nil
	kv.imm = nil
	kv.mu.Unlock()

	if err = kv.openCache(); err != nil {
		return err
	}
	kv.closing = make(chan struct{})
	kv.updated = make(chan struct{}, 1)
	if err = kv.openAll(); err != nil {
		_ = kv.Close()
		return err
	}
	if kv.Options.AutoCompact {
		kv.startCompactThread()
	}
	return nil
}

func (kv *KV) startCompactThread() {
	kv.threads.Add(1)
	go func() {
		defer kv.threads.Done()
		for {
			select {
			case <-kv.updated:
				// Compact records the failure on the KV and hands it to the
				// configured error logger; there is no caller to return it to.
				_ = kv.Compact()
			case <-kv.closing:
				return
			}
		}
	}()
}

// Close shuts the store down and is idempotent: later calls are no-ops that
// return nil. It waits for the background compaction thread and for every
// transaction that was already open. Transactions started after Close begins
// fail with ErrKVClosed rather than racing the teardown.
func (kv *KV) Close() error {
	kv.mu.Lock()
	if kv.closed {
		kv.mu.Unlock()
		return nil
	}
	kv.closed = true
	closing := kv.closing
	kv.mu.Unlock()

	if closing != nil {
		close(closing)
	}
	kv.threads.Wait()

	// A caller-driven Compact may still be running; taking kv.compact drains it
	// and keeps a new one from starting while the SSTables are released.
	kv.compact.Lock()
	defer kv.compact.Unlock()

	kv.mu.Lock()
	main := kv.main
	kv.main, kv.imm = nil, nil
	kv.mu.Unlock()

	var reterr error
	fail := func(err error) {
		if err != nil && !errors.Is(err, os.ErrClosed) && reterr == nil {
			reterr = err
		}
	}
	for _, file := range main {
		fail(file.release())
	}
	if kv.cache != nil {
		kv.cache.Close()
		kv.cache = nil
	}
	fail(kv.log.Close())
	fail(kv.meta.Close())
	return reterr
}

func (kv *KV) openAll() error {
	err := os.Mkdir(kv.Options.Dirpath, 0o755)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}

	if err := kv.openMeta(); err != nil {
		return err
	}
	if err := kv.openLog(); err != nil {
		return err
	}
	return kv.openSSTable()
}

func (kv *KV) openMeta() error {
	kv.meta.slots[0].FileName = filepath.Join(kv.Options.Dirpath, "meta0")
	kv.meta.slots[1].FileName = filepath.Join(kv.Options.Dirpath, "meta1")
	return kv.meta.Open()
}

func (kv *KV) openLog() error {
	kv.log.FileName = filepath.Join(kv.Options.Dirpath, "kv_log")
	if err := kv.log.Open(); err != nil {
		return err
	}

	committed := 0
	entries := []Entry{}
	for {
		ent := Entry{}
		eof, err := kv.log.Read(&ent)
		if err != nil {
			return err
		} else if eof {
			break
		}
		switch ent.op {
		case EntryAdd, EntryDel:
			entries = append(entries, ent)
		case EntryCommit:
			committed = len(entries)
		default:
			return fmt.Errorf("invalid log entry op: %d", ent.op)
		}
	}
	entries = entries[:committed]

	slices.SortStableFunc(entries, func(a, b Entry) int {
		return bytes.Compare(a.key, b.key)
	})
	kv.mem.Clear()
	for _, ent := range entries {
		n := kv.mem.Size()
		if n > 0 && bytes.Equal(kv.mem.Key(n-1), ent.key) {
			kv.mem.Pop()
		}
		deleted := ent.op == EntryDel
		kv.mem.Push(ent.key, ent.val, deleted)
	}
	return nil
}

func (kv *KV) openSSTable() error {
	meta := kv.meta.Get()
	kv.version = meta.Version
	kv.main = kv.main[:0]
	for _, sstable := range meta.SSTables {
		file := &SortedFile{FileName: filepath.Join(kv.Options.Dirpath, sstable)}
		if err := file.Open(); err != nil {
			return err
		}
		file.acquire() // the reference owned by kv.main
		kv.main = append(kv.main, file)
	}
	return kv.sweepOrphanSSTables(meta.SSTables)
}

// sweepOrphanSSTables deletes sstable_* files the metadata does not reference.
// They are left behind by a crash between writing an SSTable and committing the
// metadata that names it, and nothing would ever reclaim them otherwise.
func (kv *KV) sweepOrphanSSTables(referenced []string) error {
	entries, err := os.ReadDir(kv.Options.Dirpath)
	if err != nil {
		return err
	}
	keep := make(map[string]struct{}, len(referenced))
	for _, name := range referenced {
		keep[name] = struct{}{}
	}
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasPrefix(name, sstablePrefix) {
			continue
		}
		if _, ok := keep[name]; ok {
			continue
		}
		err := os.Remove(filepath.Join(kv.Options.Dirpath, name))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (tx *KVTX) Get(key []byte) (val []byte, ok bool, err error) {
	if tx.err != nil {
		return nil, false, tx.err
	}
	val, found, deleted, err := tx.levels.getExact(key)
	if err != nil || !found || deleted {
		return nil, false, err
	}
	return val, true, nil
}

func (kv *KV) Get(key []byte) (val []byte, ok bool, err error) {
	if kv.cache != nil {
		if v, found := kv.cache.Get(string(key)); found {
			return v, true, nil
		}
	}
	tx := kv.NewTX()
	defer tx.Abort()
	val, ok, err = tx.Get(key)
	if err == nil && ok && kv.cache != nil {
		kv.cache.Set(string(key), val, int64(len(val)))
	}
	return val, ok, err
}

type UpdateMode int

const (
	ModeUnknown UpdateMode = iota
	ModeUpsert             // insert or update
	ModeInsert             // insert new
	ModeUpdate             // update existing
)

func (tx *KVTX) SetEx(key []byte, val []byte, mode UpdateMode) (updated bool, err error) {
	oldVal, exist, err := tx.Get(key)
	if err != nil {
		return false, err
	}

	switch mode {
	case ModeUpsert:
		updated = !exist || !bytes.Equal(oldVal, val)
	case ModeInsert:
		updated = !exist
	case ModeUpdate:
		updated = exist && !bytes.Equal(oldVal, val)
	default:
		return false, fmt.Errorf("invalid update mode: %d", mode)
	}
	if updated {
		_, err = tx.updates.Set(key, val)
		check(err == nil)
	}
	return updated, nil
}

func (kv *KV) SetEx(key []byte, val []byte, mode UpdateMode) (updated bool, err error) {
	tx := kv.NewTX()
	updated, err = tx.SetEx(key, val, mode)
	// The commit invalidates the cache for every key it touched.
	return abortOrCommit(tx, updated, err)
}

type TXLike interface {
	Abort()
	Commit() error
}

func abortOrCommit(tx TXLike, updated bool, err error) (bool, error) {
	if err != nil {
		tx.Abort()
	} else {
		err = tx.Commit()
	}
	return err == nil && updated, err
}

func (tx *KVTX) Set(key []byte, val []byte) (updated bool, err error) {
	return tx.SetEx(key, val, ModeUpsert)
}

func (kv *KV) Set(key []byte, val []byte) (updated bool, err error) {
	return kv.SetEx(key, val, ModeUpsert)
}

func (tx *KVTX) Del(key []byte) (deleted bool, err error) {
	if _, exist, err := tx.Get(key); err != nil || !exist {
		return false, err
	}
	_, err = tx.updates.Del(key)
	check(err == nil)
	return true, nil
}

func (kv *KV) Del(key []byte) (deleted bool, err error) {
	tx := kv.NewTX()
	deleted, err = tx.Del(key)
	return abortOrCommit(tx, deleted, err)
}

// IterAll returns an iterator over all live keys in the store and a cleanup
// function the caller must invoke when done. The iterator is consistent with
// the point in time when IterAll was called.
func (kv *KV) IterAll() (SortedKVIter, func(), error) {
	tx := kv.NewTX()
	if tx.err != nil {
		return nil, nil, tx.err
	}
	iter, err := tx.levels.Iter()
	if err != nil {
		tx.Abort()
		return nil, nil, err
	}
	iter, err = filterDeleted(iter)
	if err != nil {
		tx.Abort()
		return nil, nil, err
	}
	return iter, tx.Abort, nil
}

func (tx *KVTX) Seek(key []byte) (SortedKVIter, error) {
	if tx.err != nil {
		return nil, tx.err
	}
	iter, err := tx.levels.Seek(key)
	if err != nil {
		return nil, err
	}
	return filterDeleted(iter)
}

func filterDeleted(iter SortedKVIter) (SortedKVIter, error) {
	for iter.Valid() && iter.Deleted() {
		if err := iter.Next(); err != nil {
			return nil, err
		}
	}
	return NoDeletedIter{iter}, nil
}

type NoDeletedIter struct {
	SortedKVIter
}

func (iter NoDeletedIter) Next() (err error) {
	err = iter.SortedKVIter.Next()
	for err == nil && iter.Valid() && iter.Deleted() {
		err = iter.SortedKVIter.Next()
	}
	return err
}

func (iter NoDeletedIter) Prev() (err error) {
	err = iter.SortedKVIter.Prev()
	for err == nil && iter.Valid() && iter.Deleted() {
		err = iter.SortedKVIter.Prev()
	}
	return err
}

type RangedKVIter struct {
	iter SortedKVIter
	stop []byte
	desc bool
}

func (iter *RangedKVIter) Valid() bool {
	if !iter.iter.Valid() {
		return false
	}
	r := bytes.Compare(iter.iter.Key(), iter.stop)
	if iter.desc && r < 0 {
		return false
	} else if !iter.desc && r > 0 {
		return false
	}
	return true
}

func (iter *RangedKVIter) Key() []byte {
	check(iter.Valid())
	return iter.iter.Key()
}

func (iter *RangedKVIter) Val() []byte {
	check(iter.Valid())
	return iter.iter.Val()
}

func (iter *RangedKVIter) Next() error {
	if !iter.Valid() {
		return nil
	}
	if iter.desc {
		return iter.iter.Prev()
	} else {
		return iter.iter.Next()
	}
}

func (tx *KVTX) Range(start, stop []byte, desc bool) (*RangedKVIter, error) {
	iter, err := tx.Seek(start)
	if err != nil {
		return nil, err
	}
	if desc && (!iter.Valid() || bytes.Compare(iter.Key(), start) > 0) {
		if err = iter.Prev(); err != nil {
			return nil, err
		}
	}
	return &RangedKVIter{iter: iter, stop: stop, desc: desc}, nil
}

// LastCompactError returns the most recent compaction failure, or nil if no
// compaction has failed since Open. Failures on the background compaction
// thread have no caller to return to; this is how they surface. It is sticky:
// a later successful compaction does not clear it.
func (kv *KV) LastCompactError() error {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	return kv.compactErr
}

// recordError stores err as the latest background failure and forwards it to
// the configured error logger, if any.
func (kv *KV) recordError(err error) {
	if err == nil {
		return
	}
	kv.mu.Lock()
	kv.compactErr = err
	kv.mu.Unlock()
	if kv.Options.OnError != nil {
		kv.Options.OnError(err)
	}
}

// Compact runs one compaction step: it flushes the memtable once it has grown
// past the log threshold, then merges the first adjacent SSTable pair whose
// sizes have fallen out of the growth-factor ratio.
//
// It is safe to call concurrently with itself and with the background
// compaction thread: kv.compact serialises compactions, so kv.version, kv.main
// and the metadata are only ever advanced by one goroutine at a time. Failures
// are recorded on the KV (see LastCompactError) in addition to being returned.
func (kv *KV) Compact() error {
	err := kv.compactOnce()
	if err != nil && !errors.Is(err, ErrKVClosed) {
		kv.recordError(err)
	}
	return err
}

func (kv *KV) compactOnce() error {
	kv.compact.Lock()
	defer kv.compact.Unlock()

	kv.mu.Lock()
	closed, memSize := kv.closed, kv.mem.Size()
	kv.mu.Unlock()
	if closed {
		return ErrKVClosed
	}

	if memSize >= kv.Options.LogThreshold {
		if err := kv.compactLog(); err != nil {
			return fmt.Errorf("compact: %w", err)
		}
	}

	kv.mu.Lock()
	level := -1
	for i := 0; i+1 < len(kv.main); i++ {
		if kv.shouldMergeLocked(i) {
			level = i
			break
		}
	}
	kv.mu.Unlock()
	if level < 0 {
		return nil
	}
	if err := kv.compactSSTable(level); err != nil {
		return fmt.Errorf("compact: %w", err)
	}
	return nil
}

// shouldMergeLocked reports whether level idx has grown large enough relative to
// idx+1 that the two should be merged. Caller must hold kv.mu.
func (kv *KV) shouldMergeLocked(idx int) bool {
	cur, next := kv.main[idx].EstimatedSize(), kv.main[idx+1].EstimatedSize()
	return float32(cur)*kv.Options.GrowthFactor >= float32(next)
}

// compactLog flushes the memtable into a fresh level-0 SSTable.
//
// The memtable is frozen and republished as kv.imm under kv.mu, then serialised
// and fsynced with no lock held so writers keep committing throughout. Readers
// see the frozen table between kv.mem and kv.main for the whole window, so the
// flush is invisible to them. Caller must hold kv.compact.
func (kv *KV) compactLog() error {
	kv.mu.Lock()
	check(kv.imm == nil) // serialised by kv.compact
	if kv.mem.Size() == 0 {
		kv.mu.Unlock()
		return nil
	}
	kv.version++
	version := kv.version
	frozen := kv.mem       // by-value copy: shares backing arrays with kv.mem
	kv.mem = SortedArray{} // fresh backing arrays; frozen's are now immutable
	kv.imm = &frozen
	dropTombstones := len(kv.main) == 0
	kv.mu.Unlock()

	// The memtable invariant: the live memtable must share no backing array with
	// a published snapshot, or later appends could overwrite entries a reader
	// still references.
	check(cap(kv.mem.keys) == 0 && cap(kv.mem.vals) == 0 && cap(kv.mem.deleted) == 0)

	sstable := sstableName(version)
	filename := filepath.Join(kv.Options.Dirpath, sstable)
	var src SortedKV = &frozen
	if dropTombstones {
		src = NoDeletedSortedKV{src}
	}

	file := &SortedFile{FileName: filename}
	if err := file.CreateFromSorted(src); err != nil {
		kv.unfreeze()
		_ = os.Remove(filename)
		return err
	}

	meta := kv.meta.Get()
	meta.Version = version
	meta.SSTables = slices.Insert(slices.Clone(meta.SSTables), 0, sstable)
	if err := kv.meta.Set(meta); err != nil {
		// The SSTable is unreferenced: drop it instead of leaking it on disk.
		_ = file.Close()
		_ = os.Remove(filename)
		kv.unfreeze()
		return err
	}

	// Hold the commit lock so no writer appends to the log between publishing
	// the SSTable and rewriting the log around the live memtable.
	kv.commit.Lock()
	defer kv.commit.Unlock()

	kv.mu.Lock()
	file.acquire() // the reference owned by kv.main
	kv.main = slices.Insert(kv.main, 0, file)
	kv.imm = nil
	kv.mu.Unlock()

	return kv.rewriteLog()
}

// unfreeze rolls back a failed flush by merging the frozen memtable back under
// the live one, so no committed write disappears from the read path. The merge
// builds a new array rather than mutating in place, preserving the memtable
// invariant.
func (kv *KV) unfreeze() {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if kv.imm == nil {
		return
	}
	merged := SortedArray{}
	iter, err := MergedSortedKV{&kv.mem, kv.imm}.Iter()
	for ; err == nil && iter.Valid(); err = iter.Next() {
		merged.Push(iter.Key(), iter.Val(), iter.Deleted())
	}
	check(err == nil)
	kv.mem = merged
	kv.imm = nil
}

// rewriteLog rebuilds the write-ahead log so it holds exactly the live
// memtable: everything older is now durable in an SSTable the metadata names.
// Caller must hold kv.commit and must not hold kv.mu.
func (kv *KV) rewriteLog() error {
	kv.mu.Lock()
	live := kv.mem // by-value copy; the backing arrays are append-only
	kv.mu.Unlock()

	if err := kv.log.Truncate(); err != nil {
		return err
	}
	if live.Size() == 0 {
		return nil
	}

	defer kv.log.ResetTX()
	iter, err := live.Iter()
	for ; err == nil && iter.Valid(); err = iter.Next() {
		op := EntryAdd
		if iter.Deleted() {
			op = EntryDel
		}
		if err = kv.log.Write(&Entry{key: iter.Key(), val: iter.Val(), op: op}); err != nil {
			return err
		}
	}
	check(err == nil)
	return kv.log.Commit()
}

// compactSSTable merges levels level and level+1 into a single new SSTable.
// Caller must hold kv.compact, which is what keeps the two source tables alive:
// only a compaction retires a table, and Close drains kv.compact first.
func (kv *KV) compactSSTable(level int) error {
	kv.mu.Lock()
	if level+1 >= len(kv.main) {
		kv.mu.Unlock()
		return nil
	}
	kv.version++
	version := kv.version
	old1, old2 := kv.main[level], kv.main[level+1]
	dropTombstones := len(kv.main) == level+2
	kv.mu.Unlock()

	sstable := sstableName(version)
	filename := filepath.Join(kv.Options.Dirpath, sstable)
	var src SortedKV = MergedSortedKV{old1, old2}
	if dropTombstones {
		src = NoDeletedSortedKV{src}
	}

	file := &SortedFile{FileName: filename}
	if err := file.CreateFromSorted(src); err != nil {
		_ = os.Remove(filename)
		return err
	}

	meta := kv.meta.Get()
	meta.Version = version
	meta.SSTables = slices.Replace(slices.Clone(meta.SSTables), level, level+2, sstable)
	if err := kv.meta.Set(meta); err != nil {
		// The SSTable is unreferenced: drop it instead of leaking it on disk.
		_ = file.Close()
		_ = os.Remove(filename)
		return err
	}

	kv.mu.Lock()
	check(kv.main[level] == old1 && kv.main[level+1] == old2)
	file.acquire() // the reference owned by kv.main
	kv.main = slices.Replace(kv.main, level, level+2, file)
	kv.mu.Unlock()

	// Retire the merged tables: the fds close and the files are unlinked once
	// the last snapshot that captured them releases its reference. Snapshots
	// taken before this point keep reading them meanwhile.
	old1.retire()
	old2.retire()
	return errors.Join(old1.release(), old2.release())
}

type NoDeletedSortedKV struct {
	SortedKV
}

func (kv NoDeletedSortedKV) Iter() (iter SortedKVIter, err error) {
	if iter, err = kv.SortedKV.Iter(); err != nil {
		return nil, err
	}
	return NoDeletedIter{iter}, nil
}
