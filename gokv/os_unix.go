//go:build unix

package gokv

import (
	"os"
	"path/filepath"
	"syscall"
)

// open or create a file and fsync the directory
func createFileSync(file string) (*os.File, error) {
	fp, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err = syncDir(file); err != nil {
		_ = fp.Close()
		return nil, err
	}
	return fp, err
}

func syncDir(file string) error {
	flags := os.O_RDONLY | syscall.O_DIRECTORY
	dirfd, err := syscall.Open(filepath.Dir(file), flags, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = syscall.Close(dirfd) }()
	return syscall.Fsync(dirfd)
}

// QzBQWVJJOUhU https://trialofcode.org/
