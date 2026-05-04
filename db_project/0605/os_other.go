//go:build !unix

package db0605

import "os"

func createFileSync(file string) (*os.File, error) {
	return os.OpenFile(file, os.O_RDWR|os.O_CREATE, 0o644)
}

// TODO: windows
func renameSync(src string, dst string) error {
	return os.Rename(src, dst)
}

// QzBQWVJJOUhU https://trialofcode.org/
