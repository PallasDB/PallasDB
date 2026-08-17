package db

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// A fault-injection harness for the two files whose durability the engine
// depends on: the write-ahead log and the metadata slots. Both reach the
// filesystem through the openFileSync indirection in log.go, so wrapping that
// hook is enough to make a crash reproducible instead of hypothetical.
//
// The unit of injection is a "syscall number": calls that matter for durability
// (WriteAt, Sync, Truncate) are counted per kind, and the fault fires on the
// nth one. Three failure shapes are supported, and the third is the interesting
// one:
//
//	faultFail  - the call returns an error, as a full disk or an EIO would.
//	faultShort - a partial write followed by an error: a torn record.
//	faultDrop  - the call reports success and does nothing. This models the
//	             failure that CRC32 checks cannot see: a truncate or an fsync
//	             that the kernel acknowledged but that never reached the
//	             platter, so after a crash the old bytes are still there.

type faultOp int

const (
	opAny faultOp = iota
	opWriteAt
	opSync
	opTruncate
)

func (op faultOp) String() string {
	switch op {
	case opWriteAt:
		return "WriteAt"
	case opSync:
		return "Sync"
	case opTruncate:
		return "Truncate"
	default:
		return "any"
	}
}

type faultMode int

const (
	faultNone faultMode = iota
	faultFail
	faultShort
	faultDrop
)

// errInjected is what a faulted call returns. It is deliberately not any of the
// sentinels the engine treats as recoverable, so a test cannot pass by having
// its injected failure quietly classified as end-of-log.
var errInjected = errors.New("injected fault")

type faultSpec struct {
	// op selects which kind of syscall is counted and faulted; opAny counts all.
	op faultOp
	// at is the 1-based index, among calls matching op since the harness was
	// armed, that the fault fires on.
	at int
	// mode is the failure shape.
	mode faultMode
}

// faultFS owns the arming state. Counters live here rather than in the file
// wrapper so that a spec can target "the nth Sync of the metadata slot" across
// a close/reopen cycle.
type faultFS struct {
	mu     sync.Mutex
	match  func(name string) bool
	spec   faultSpec
	armed  bool
	fired  bool
	counts map[faultOp]int
}

// withFaultFS installs the harness for the duration of the test. It starts
// disarmed so that setup writes go through untouched.
func withFaultFS(t *testing.T, match func(name string) bool) *faultFS {
	t.Helper()
	fs := &faultFS{match: match, counts: map[faultOp]int{}}
	prev := openFileSync
	openFileSync = func(name string) (fileLike, error) {
		fp, err := prev(name)
		if err != nil || !fs.match(name) {
			return fp, err
		}
		return &faultFile{fileLike: fp, fs: fs, name: name}, nil
	}
	t.Cleanup(func() { openFileSync = prev })
	return fs
}

// arm enables the fault and resets the syscall counters.
func (fs *faultFS) arm(spec faultSpec) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.spec, fs.armed, fs.fired = spec, true, false
	fs.counts = map[faultOp]int{}
}

func (fs *faultFS) disarm() {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.armed = false
}

// fired reports whether the injected fault was actually reached. Every test
// asserts this: a crash test that silently never crashed is worse than no test.
func (fs *faultFS) hasFired() bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.fired
}

// next accounts for one syscall and reports the fault to apply to it.
func (fs *faultFS) next(op faultOp) faultMode {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !fs.armed || fs.fired {
		return faultNone
	}
	if fs.spec.op != opAny && fs.spec.op != op {
		return faultNone
	}
	fs.counts[fs.spec.op]++
	if fs.counts[fs.spec.op] != fs.spec.at {
		return faultNone
	}
	fs.fired = true
	return fs.spec.mode
}

type faultFile struct {
	fileLike
	fs   *faultFS
	name string
}

func (f *faultFile) WriteAt(b []byte, off int64) (int, error) {
	switch f.fs.next(opWriteAt) {
	case faultFail:
		return 0, fmt.Errorf("%w: WriteAt(%d bytes @ %d) on %s", errInjected, len(b), off, f.name)
	case faultShort:
		// Land half the bytes, then fail: a torn record.
		n, _ := f.fileLike.WriteAt(b[:len(b)/2], off)
		return n, fmt.Errorf("%w: short WriteAt on %s", errInjected, f.name)
	case faultDrop:
		return len(b), nil // acknowledged, never written
	}
	return f.fileLike.WriteAt(b, off)
}

func (f *faultFile) Sync() error {
	switch f.fs.next(opSync) {
	case faultFail, faultShort:
		return fmt.Errorf("%w: Sync on %s", errInjected, f.name)
	case faultDrop:
		return nil // the fsync that lied
	}
	return f.fileLike.Sync()
}

func (f *faultFile) Truncate(size int64) error {
	switch f.fs.next(opTruncate) {
	case faultFail, faultShort:
		return fmt.Errorf("%w: Truncate(%d) on %s", errInjected, size, f.name)
	case faultDrop:
		return nil // the truncate that never reached disk
	}
	return f.fileLike.Truncate(size)
}

// matchBase selects files by base name, e.g. "kv_log" or "meta0".
func matchBase(names ...string) func(string) bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(path string) bool { return set[filepath.Base(path)] }
}
