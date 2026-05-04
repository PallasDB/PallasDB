package db0603

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
)

type SortedKV interface {
	Size() int
	Iter() (SortedKVIter, error)
}

type SortedKVIter interface {
	Valid() bool
	Key() []byte
	Val() []byte
	Next() error
	Prev() error
}

type SortedFile struct {
	FileName string
	fp       *os.File
	nkeys    int
}

func (file *SortedFile) Close() error {
	return file.fp.Close()
}

func (file *SortedFile) CreateFromSorted(kv SortedKV) (err error) {
	if file.fp, err = createFileSync(file.FileName); err != nil {
		return err
	}
	if err = file.writeSortedFile(kv); err != nil {
		_ = file.Close()
	}
	return err
}

func (file *SortedFile) writeSortedFile(kv SortedKV) (err error) {
	var buf [4 + 4]byte
	binary.LittleEndian.PutUint64(buf[:8], uint64(kv.Size()))
	if _, err = file.fp.WriteAt(buf[:8], 0); err != nil {
		return err
	}

	nkeys := 0
	offset := 8 + 8*kv.Size()
	iter, err := kv.Iter()
	for ; err == nil && iter.Valid(); err = iter.Next() {
		key, val := iter.Key(), iter.Val()

		binary.LittleEndian.PutUint64(buf[:8], uint64(offset))
		if _, err = file.fp.WriteAt(buf[:8], int64(8+8*nkeys)); err != nil {
			return err
		}

		binary.LittleEndian.PutUint32(buf[0:4], uint32(len(key)))
		binary.LittleEndian.PutUint32(buf[4:8], uint32(len(val)))
		if _, err = file.fp.WriteAt(buf[:4+4], int64(offset)); err != nil {
			return err
		}
		offset += 4 + 4
		if _, err = file.fp.WriteAt(key, int64(offset)); err != nil {
			return err
		}
		offset += len(key)
		if _, err = file.fp.WriteAt(val, int64(offset)); err != nil {
			return err
		}
		offset += len(val)

		nkeys++
	}
	if err != nil {
		return err
	}

	check(nkeys == kv.Size())
	file.nkeys = nkeys
	return file.fp.Sync()
}

type SortedFileIter struct {
	file *SortedFile
	pos  int
	key  []byte
	val  []byte
}

func (iter *SortedFileIter) Valid() bool {
	return 0 <= iter.pos && iter.pos < iter.file.nkeys
}
func (iter *SortedFileIter) Key() []byte { return iter.key }
func (iter *SortedFileIter) Val() []byte { return iter.val }

func (iter *SortedFileIter) Next() error {
	if iter.pos < iter.file.nkeys {
		iter.pos++
	}
	return iter.loadCurrent()
}
func (iter *SortedFileIter) Prev() error {
	if iter.pos >= 0 {
		iter.pos--
	}
	return iter.loadCurrent()
}
func (iter *SortedFileIter) loadCurrent() (err error) {
	if iter.Valid() {
		iter.key, iter.val, err = iter.file.index(iter.pos)
	}
	return err
}

func (file *SortedFile) Size() int { return file.nkeys }
func (file *SortedFile) Iter() (SortedKVIter, error) {
	iter := &SortedFileIter{file: file, pos: 0}
	if err := iter.loadCurrent(); err != nil {
		return nil, err
	}
	return iter, nil
}

func (file *SortedFile) index(pos int) (key []byte, val []byte, err error) {
	check(0 <= pos && pos < file.nkeys)
	var buf [4 + 4]byte
	if _, err = file.fp.ReadAt(buf[:], int64(8+8*pos)); err != nil {
		return nil, nil, err
	}
	offset := int64(binary.LittleEndian.Uint64(buf[:8]))
	if int64(8+8*file.nkeys) > offset {
		return nil, nil, errors.New("corrupted file")
	}

	if _, err = file.fp.ReadAt(buf[:4+4], offset); err != nil {
		return nil, nil, err
	}
	klen := binary.LittleEndian.Uint32(buf[0:4])
	vlen := binary.LittleEndian.Uint32(buf[4:8])
	data := make([]byte, klen+vlen)
	if _, err = file.fp.ReadAt(data, offset+4+4); err != nil {
		return nil, nil, err
	}
	return data[:klen], data[klen:], nil
}

func (file *SortedFile) Seek(key []byte) (SortedKVIter, error) {
	pos, err := file.findPos(key)
	if err != nil {
		return nil, err
	}
	iter := &SortedFileIter{file: file, pos: pos}
	if err = iter.loadCurrent(); err != nil {
		return nil, err
	}
	return iter, nil
}

func (file *SortedFile) findPos(target []byte) (int, error) {
	lo, hi := 0, file.nkeys
	for lo < hi {
		mid := lo + (hi-lo)/2
		key, _, err := file.index(mid)
		if err != nil {
			return -1, err
		}
		r := bytes.Compare(target, key)
		if r > 0 {
			lo = mid + 1
		} else if r < 0 {
			hi = mid
		} else {
			return mid, nil
		}
	}
	return lo, nil
}

// QzBQWVJJOUhU https://trialofcode.org/
