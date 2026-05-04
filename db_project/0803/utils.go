package db0803

import "io"

type MultiClosers []io.Closer

func (mc *MultiClosers) Close() (reterr error) {
	for _, item := range *mc {
		if err := item.Close(); err != nil {
			reterr = err
		}
	}
	*mc = nil
	return reterr
}

// QzBQWVJJOUhU https://trialofcode.org/
