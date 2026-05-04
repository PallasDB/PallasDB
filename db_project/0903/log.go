package db0903

import (
	"io"
	"os"
)

type Log struct {
	FileName string
	fp       *os.File
	reader   OffsetReader
	writer   struct {
		offset    int64
		committed int64
	}
}

func (log *Log) Open() (err error) {
	log.fp, err = createFileSync(log.FileName)
	log.reader = OffsetReader{log.fp, 0}
	log.writer.offset = 0
	log.writer.committed = 0
	return err
}

func (log *Log) Close() error {
	return log.fp.Close()
}

func (log *Log) Write(ent *Entry) error {
	data := ent.Encode()
	if _, err := log.fp.WriteAt(data, log.writer.offset); err != nil {
		return err
	}
	log.writer.offset += int64(len(data))
	return nil
}

func (log *Log) Commit() error {
	if err := log.Write(&Entry{op: EntryCommit}); err != nil {
		return err
	}
	if err := log.fp.Sync(); err != nil {
		return err
	}
	log.writer.committed = log.writer.offset
	return nil
}

func (log *Log) ResetTX() {
	log.writer.offset = log.writer.committed
}

func (log *Log) Read(ent *Entry) (eof bool, err error) {
	err = ent.Decode(&log.reader)
	if err == io.EOF || err == io.ErrUnexpectedEOF || err == ErrBadSum {
		return true, nil
	} else if err != nil {
		return false, err
	} else {
		if ent.op == EntryCommit {
			log.writer.offset = log.reader.offset
			log.writer.committed = log.reader.offset
		}
		return false, nil
	}
}

func (log *Log) Truncate() error {
	log.writer.offset = 0
	log.writer.committed = 0
	return log.fp.Truncate(0)
}

type OffsetReader struct {
	inner  io.ReaderAt
	offset int64
}

func (rd *OffsetReader) Read(buf []byte) (n int, err error) {
	n, err = rd.inner.ReadAt(buf, rd.offset)
	if n > 0 {
		err = nil
	}
	rd.offset += int64(n)
	return n, err
}

// QzBQWVJJOUhU https://trialofcode.org/
