//go:build !unix

package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DirSyncSupported reports whether syncDir can actually make a directory entry
// durable on this platform.
//
// It is false here, and that is not a detail callers may ignore. On Windows a
// directory cannot be opened as a file and handed to FlushFileBuffers, and
// neither plan9 nor the wasm targets expose a directory sync at all. What this
// build can guarantee for a newly created file is therefore:
//
//   - the file's own data and metadata are durable, because createFileSync
//     calls (*os.File).Sync, which maps to FlushFileBuffers on Windows; and
//   - nothing about the directory entry. After a crash the file may be missing
//     entirely even though Sync returned successfully, and a file that was
//     renamed or deleted may come back.
//
// In LSM terms: a freshly written SSTable is either fully present or absent,
// but its presence is not ordered against the metadata update that references
// it. Recovery must tolerate a metadata snapshot naming a file that is gone --
// it surfaces as an open error rather than as silent corruption -- and it must
// tolerate SSTable files that no snapshot names, which it already does.
//
// This constant exists so that the fact is programmatically visible instead of
// being a silent no-op behind a build tag.
const DirSyncSupported = false

// ErrDirSyncUnsupported reports that the platform cannot make a directory entry
// durable. createFileSync tolerates it -- there is no better behaviour
// available -- but it is returned rather than swallowed so that callers which
// need the distinction can see it.
var ErrDirSyncUnsupported = errors.New("directory fsync is not supported on this platform")

// createFileSync opens or creates a file and flushes it as far as the platform
// allows. See DirSyncSupported for exactly what that buys.
func createFileSync(file string) (*os.File, error) {
	fp, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	// FlushFileBuffers on Windows. This is the strongest durability primitive
	// available here and is strictly better than the previous no-op.
	if err = fp.Sync(); err != nil {
		_ = fp.Close()
		return nil, err
	}
	if err = syncDir(file); err != nil && !errors.Is(err, ErrDirSyncUnsupported) {
		_ = fp.Close()
		return nil, err
	}
	return fp, nil
}

// syncDir attempts a directory flush and reports ErrDirSyncUnsupported when the
// platform refuses, which is the expected outcome everywhere this file builds.
// It still tries: a future platform that grows the capability starts working
// without a code change, and the attempt is one failed open per file creation.
func syncDir(file string) error {
	dir, err := os.Open(filepath.Dir(file))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDirSyncUnsupported, err)
	}
	defer func() { _ = dir.Close() }()
	if err = dir.Sync(); err != nil {
		return fmt.Errorf("%w: %v", ErrDirSyncUnsupported, err)
	}
	return nil
}
