//go:build !unix

package db0402

import "os"

func createFileSync(file string) (*os.File, error) {
	return os.OpenFile(file, os.O_RDWR|os.O_CREATE, 0o644)
}

// QzBQWVJJOUhU https://trialofcode.org/
