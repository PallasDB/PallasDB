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
	"sync"

	"github.com/dgraph-io/ristretto/v2"
)

const (
	defaultLogThreshold = 1000
	defaultGrowthFactor = 2.0
)

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

type KV struct {
	Options KVOptions
	// metadata
	meta    KVMetaStore
	version uint64
	// data
	log   Log
	mem   SortedArray
	main  []SortedFile
	cache *ristretto.Cache[string, []byte]
	// transactions
	snapshot uint64
	history  []UpdatedKey
	ongoing  []*KVTX
	// synchronization
	mu      sync.Mutex
	commit  sync.Mutex
	updated chan struct{}
	closing chan struct{}
	threads sync.WaitGroup
	MultiClosers
}

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
	mainSnap []SortedFile
}

func (kv *KV) NewTX() *KVTX {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	tx := &KVTX{snapshot: kv.snapshot, target: kv}
	mem := kv.mem // copy!
	tx.mainSnap = make([]SortedFile, len(kv.main))
	copy(tx.mainSnap, kv.main)
	tx.levels = MergedSortedKV{&tx.updates, &mem}
	for i := range tx.mainSnap {
		tx.levels = append(tx.levels, &tx.mainSnap[i])
	}
	kv.ongoing = append(kv.ongoing, tx)
	kv.threads.Add(1)
	return tx
}

func (tx *KVTX) Abort() { tx.target.abortTX(tx) }

func (kv *KV) abortTX(tx *KVTX) { kv.untrackTXSync(tx) }

func (kv *KV) untrackTXSync(tx *KVTX) {
	kv.mu.Lock()
	defer kv.mu.Unlock()

	idx := slices.Index(kv.ongoing, tx)
	if idx < 0 {
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
	kv.threads.Add(-1)
}

func (tx *KVTX) Commit() error { return tx.target.applyTX(tx) }

var ErrTXConflict = errors.New("TX is conflict with another TX")

func (kv *KV) applyTXSync(tx *KVTX) error {
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
	defer kv.mu.Unlock()
	kv.updateMem(tx)
	kv.updateHistory(tx)
	return nil
}

func (kv *KV) applyTX(tx *KVTX) error {
	if err := kv.applyTXSync(tx); err != nil {
		return err
	}
	if kv.Options.AutoCompact {
		select {
		case kv.updated <- struct{}{}:
		case <-kv.closing:
		}
	}
	return nil
}

func (kv *KV) checkTXConflict(tx *KVTX) bool {
	kv.mu.Lock()
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

func (kv *KV) updateMem(tx *KVTX) {
	merged := SortedArray{}
	iter, err := MergedSortedKV{&tx.updates, &kv.mem}.Iter()
	for ; err == nil && iter.Valid(); err = iter.Next() {
		merged.Push(iter.Key(), iter.Val(), iter.Deleted())
	}
	check(err == nil)
	kv.mem = merged
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
	inner := &KVTX{target: tx}
	inner.levels = slices.Concat(MergedSortedKV{&inner.updates}, tx.levels)
	return inner
}

func (tx *KVTX) applyTX(inner *KVTX) error {
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
				if err := kv.Compact(); err != nil {
					_ = fmt.Errorf("compact: %w", err)
				}
			case <-kv.closing:
				return
			}
		}
	}()
}

func (kv *KV) Close() error {
	if kv.closing != nil {
		close(kv.closing)
		kv.threads.Wait()
		kv.closing = nil
	} else {
		kv.threads.Wait()
	}
	if kv.cache != nil {
		kv.cache.Close()
		kv.cache = nil
	}
	return kv.MultiClosers.Close()
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
	if err := kv.meta.Open(); err != nil {
		return err
	}
	kv.MultiClosers = append(kv.MultiClosers, &kv.meta)
	return nil
}

func (kv *KV) openLog() error {
	kv.log.FileName = filepath.Join(kv.Options.Dirpath, "kv_log")
	if err := kv.log.Open(); err != nil {
		return err
	}
	kv.MultiClosers = append(kv.MultiClosers, &kv.log)

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
		sstable = filepath.Join(kv.Options.Dirpath, sstable)
		file := SortedFile{FileName: sstable}
		if err := file.Open(); err != nil {
			return err
		}
		kv.MultiClosers = append(kv.MultiClosers, &file)
		kv.main = append(kv.main, file)
	}
	return nil
}

func (tx *KVTX) Get(key []byte) (val []byte, ok bool, err error) {
	iter, err := tx.Seek(key)
	ok = err == nil && iter.Valid() && bytes.Equal(iter.Key(), key)
	if ok {
		val = iter.Val()
	}
	return val, ok, err
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
	updated, err = abortOrCommit(tx, updated, err)
	if err == nil && kv.cache != nil {
		kv.cache.Del(string(key))
	}
	return updated, err
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
	deleted, err = abortOrCommit(tx, deleted, err)
	if err == nil && deleted && kv.cache != nil {
		kv.cache.Del(string(key))
	}
	return deleted, err
}

// IterAll returns an iterator over all live keys in the store and a cleanup
// function the caller must invoke when done. The iterator is consistent with
// the point in time when IterAll was called.
func (kv *KV) IterAll() (SortedKVIter, func(), error) {
	tx := kv.NewTX()
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

func (kv *KV) Compact() error {
	kv.mu.Lock()
	memSize := kv.mem.Size()
	kv.mu.Unlock()
	if memSize >= kv.Options.LogThreshold {
		if err := kv.compactLog(); err != nil {
			return err
		}
	}
	for i := 0; i < len(kv.main)-1; i++ {
		if kv.shouldMerge(i) {
			return kv.compactSSTable(i)
		}
	}
	return nil
}

func (kv *KV) shouldMerge(idx int) bool {
	cur, next := kv.main[idx].EstimatedSize(), kv.main[idx+1].EstimatedSize()
	return float32(cur)*kv.Options.GrowthFactor >= float32(next)
}

func (kv *KV) compactLog() error {
	kv.version++
	sstable := "sstable_" + strconv.FormatUint(kv.version, 10)
	filename := filepath.Join(kv.Options.Dirpath, sstable)

	kv.commit.Lock()
	defer kv.commit.Unlock()

	file := SortedFile{FileName: filename}
	m := SortedKV(&kv.mem)
	if len(kv.main) == 0 {
		m = NoDeletedSortedKV{m}
	}
	if err := file.CreateFromSorted(m); err != nil {
		_ = os.Remove(filename)
		return err
	}

	meta := kv.meta.Get()
	meta.Version = kv.version
	meta.SSTables = slices.Insert(meta.SSTables, 0, sstable)
	if err := kv.meta.Set(meta); err != nil {
		_ = file.Close()
		return err
	}

	kv.mu.Lock()
	kv.main = slices.Insert(kv.main, 0, file)
	kv.MultiClosers = append(kv.MultiClosers, &kv.main[0])
	kv.mem.Clear()
	kv.mu.Unlock()

	return kv.log.Truncate()
}

func (kv *KV) compactSSTable(level int) error {
	kv.version++
	sstable := "sstable_" + strconv.FormatUint(kv.version, 10)
	filename := filepath.Join(kv.Options.Dirpath, sstable)

	file := SortedFile{FileName: filename}
	m := SortedKV(MergedSortedKV{&kv.main[level], &kv.main[level+1]})
	if len(kv.main) == level+2 {
		m = NoDeletedSortedKV{m}
	}
	if err := file.CreateFromSorted(m); err != nil {
		_ = os.Remove(filename)
		return err
	}

	meta := kv.meta.Get()
	meta.Version = kv.version
	meta.SSTables = slices.Replace(meta.SSTables, level, level+2, sstable)
	if err := kv.meta.Set(meta); err != nil {
		_ = file.Close()
		return err
	}

	old1, old2 := &kv.main[level], &kv.main[level+1]
	old1Name, old2Name := old1.FileName, old2.FileName
	_ = old1.Close()
	_ = old2.Close()

	kv.mu.Lock()
	kv.main = slices.Replace(kv.main, level, level+2, file)
	kv.MultiClosers = append(kv.MultiClosers, &kv.main[level])
	kv.mu.Unlock()

	_ = os.Remove(old1Name)
	_ = os.Remove(old2Name)
	return nil
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

// QzBQWVJJOUhU https://trialofcode.org/
