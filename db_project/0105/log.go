package db0105

import (
	"os"
)

type Log struct {
	FileName string
	fp       *os.File
}

func (log *Log) Open() (err error) {
	log.fp, err = createFileSync(log.FileName)
	return err
}

func (log *Log) Close() error {
	return log.fp.Close()
}

func (log *Log) Write(ent *Entry) error {
	if _, err := log.fp.Write(ent.Encode()); err != nil {
		return err
	}
	return log.fp.Sync() // fsync
}

func (log *Log) Read(ent *Entry) (eof bool, err error)

// QzBQWVJJOUhU https://trialofcode.org/
