package db

import (
	"errors"
	"io"
	"os"
)

type MultiClosers []io.Closer

func (mc *MultiClosers) Close() (reterr error) {
	for _, item := range *mc {
		if err := item.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			reterr = err
		}
	}
	*mc = nil
	return reterr
}

// QzBQWVJJOUhU https://trialofcode.org/
