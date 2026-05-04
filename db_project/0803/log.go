package db0803

import (
	"io"
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

func (log *Log) Read(ent *Entry) (eof bool, err error) {
	err = ent.Decode(log.fp)
	if err == io.EOF || err == io.ErrUnexpectedEOF || err == ErrBadSum {
		return true, nil
	} else if err != nil {
		return false, err
	} else {
		return false, nil
	}
}

func (log *Log) Truncate() error {
	if _, err := log.fp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return log.fp.Truncate(0)
}

// QzBQWVJJOUhU https://trialofcode.org/
