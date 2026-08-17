package db

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"

	"github.com/bits-and-blooms/bloom/v3"
)

type SortedKV interface {
	EstimatedSize() int
	Iter() (SortedKVIter, error)
	Seek(key []byte) (SortedKVIter, error)
}

type SortedKVIter interface {
	Valid() bool
	Key() []byte
	Val() []byte
	Deleted() bool
	Next() error
	Prev() error
}

type SortedFile struct {
	FileName    string
	fp          *os.File
	nkeys       int
	offsetIndex []byte
	bloom       *bloom.BloomFilter
}

func (file *SortedFile) Close() error {
	if file.fp == nil {
		return nil
	}
	err := file.fp.Close()
	file.fp = nil
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func (file *SortedFile) Open() (err error) {
	file.fp, err = os.OpenFile(file.FileName, os.O_RDONLY, 0o644)
	if err != nil {
		return err
	}
	if err = file.openExisting(); err != nil {
		_ = file.Close()
	}
	return err
}

const maxNKeys = 1 << 26 // ~67 million keys

const sortedFileBloomFalsePositiveRate = 0.01
const sortedFileBloomFooterSize = 16
const sortedFileMaxOffsetIndexBytes = 64 << 20

var sortedFileBloomMagic = [8]byte{'P', 'D', 'B', 'B', 'L', 'M', '1', '\n'}

func newSortedFileBloom(expectedKeys int) *bloom.BloomFilter {
	if expectedKeys < 1 {
		expectedKeys = 1
	}
	return bloom.NewWithEstimates(uint(expectedKeys), sortedFileBloomFalsePositiveRate)
}

func (file *SortedFile) openExisting() error {
	var buf [8]byte
	if _, err := file.fp.ReadAt(buf[:8], 0); err != nil {
		return err
	}
	nkeys := int(binary.LittleEndian.Uint64(buf[:8]))
	if nkeys < 0 || nkeys > maxNKeys {
		return errors.New("corrupted file: nkeys out of range")
	}
	file.nkeys = nkeys
	if err := file.loadOffsetIndex(); err != nil {
		return err
	}
	return file.loadBloomFilter()
}

func (file *SortedFile) loadOffsetIndex() error {
	nbytes := file.nkeys * 8
	if nbytes == 0 {
		file.offsetIndex = nil
		return nil
	}
	if nbytes < 0 || nbytes > sortedFileMaxOffsetIndexBytes {
		file.offsetIndex = nil
		return nil
	}

	file.offsetIndex = make([]byte, nbytes)
	if _, err := file.fp.ReadAt(file.offsetIndex, 8); err != nil {
		file.offsetIndex = nil
		return err
	}
	return nil
}

func (file *SortedFile) loadBloomFilter() error {
	st, err := file.fp.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	if size < sortedFileBloomFooterSize {
		return nil
	}

	var footer [sortedFileBloomFooterSize]byte
	if _, err = file.fp.ReadAt(footer[:], size-sortedFileBloomFooterSize); err != nil {
		return err
	}
	if !bytes.Equal(footer[8:], sortedFileBloomMagic[:]) {
		return nil
	}

	bloomLen := binary.LittleEndian.Uint64(footer[:8])
	if bloomLen == 0 || bloomLen > uint64(size-sortedFileBloomFooterSize) {
		return errors.New("corrupted file: bloom filter out of range")
	}
	dataStart := size - sortedFileBloomFooterSize - int64(bloomLen)
	data := make([]byte, int(bloomLen))
	if _, err = file.fp.ReadAt(data, dataStart); err != nil {
		return err
	}

	filter := &bloom.BloomFilter{}
	if err = filter.UnmarshalBinary(data); err != nil {
		return err
	}
	file.bloom = filter
	return nil
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
	var buf [4 + 4 + 1]byte
	nkeys := 0
	offset := 8 + 8*kv.EstimatedSize()
	filter := newSortedFileBloom(kv.EstimatedSize())
	if indexBytes := 8 * kv.EstimatedSize(); indexBytes >= 0 && indexBytes <= sortedFileMaxOffsetIndexBytes {
		file.offsetIndex = make([]byte, indexBytes)
	} else {
		file.offsetIndex = nil
	}
	iter, err := kv.Iter()
	for ; err == nil && iter.Valid(); err = iter.Next() {
		key, val := iter.Key(), iter.Val()

		binary.LittleEndian.PutUint64(buf[:8], uint64(offset))
		if len(file.offsetIndex) >= 8*(nkeys+1) {
			copy(file.offsetIndex[8*nkeys:8*(nkeys+1)], buf[:8])
		}
		if _, err = file.fp.WriteAt(buf[:8], int64(8+8*nkeys)); err != nil {
			return err
		}

		binary.LittleEndian.PutUint32(buf[0:4], uint32(len(key)))
		binary.LittleEndian.PutUint32(buf[4:8], uint32(len(val)))
		if iter.Deleted() {
			buf[8] = 1
		} else {
			buf[8] = 0
		}
		if _, err = file.fp.WriteAt(buf[:4+4+1], int64(offset)); err != nil {
			return err
		}
		offset += 4 + 4 + 1
		if _, err = file.fp.WriteAt(key, int64(offset)); err != nil {
			return err
		}
		offset += len(key)
		if _, err = file.fp.WriteAt(val, int64(offset)); err != nil {
			return err
		}
		offset += len(val)

		filter.Add(key)
		nkeys++
	}
	if err != nil {
		return err
	}

	check(nkeys <= kv.EstimatedSize())
	file.nkeys = nkeys
	if len(file.offsetIndex) >= 8*nkeys {
		file.offsetIndex = file.offsetIndex[:8*nkeys]
	} else {
		file.offsetIndex = nil
	}
	binary.LittleEndian.PutUint64(buf[:8], uint64(nkeys))
	if _, err = file.fp.WriteAt(buf[:8], 0); err != nil {
		return err
	}

	bloomData, err := filter.MarshalBinary()
	if err != nil {
		return err
	}
	if _, err = file.fp.WriteAt(bloomData, int64(offset)); err != nil {
		return err
	}
	offset += len(bloomData)

	var footer [sortedFileBloomFooterSize]byte
	binary.LittleEndian.PutUint64(footer[:8], uint64(len(bloomData)))
	copy(footer[8:], sortedFileBloomMagic[:])
	if _, err = file.fp.WriteAt(footer[:], int64(offset)); err != nil {
		return err
	}
	file.bloom = filter

	return file.fp.Sync()
}

type SortedFileIter struct {
	file    *SortedFile
	pos     int
	key     []byte
	val     []byte
	deleted bool
}

func (iter *SortedFileIter) Valid() bool {
	return 0 <= iter.pos && iter.pos < iter.file.nkeys
}
func (iter *SortedFileIter) Key() []byte   { return iter.key }
func (iter *SortedFileIter) Val() []byte   { return iter.val }
func (iter *SortedFileIter) Deleted() bool { return iter.deleted }

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
		iter.key, iter.val, iter.deleted, err = iter.file.index(iter.pos)
	}
	return err
}

func (file *SortedFile) EstimatedSize() int { return file.nkeys }
func (file *SortedFile) Iter() (SortedKVIter, error) {
	iter := &SortedFileIter{file: file, pos: 0}
	if err := iter.loadCurrent(); err != nil {
		return nil, err
	}
	return iter, nil
}

func (file *SortedFile) index(pos int) (key []byte, val []byte, deleted bool, err error) {
	offset, err := file.entryOffset(pos)
	if err != nil {
		return nil, nil, false, err
	}

	var header [4 + 4 + 1]byte
	if _, err = file.fp.ReadAt(header[:], offset); err != nil {
		return nil, nil, false, err
	}
	klen := binary.LittleEndian.Uint32(header[0:4])
	vlen := binary.LittleEndian.Uint32(header[4:8])
	if int64(klen)+int64(vlen) > MaxEntrySize {
		return nil, nil, false, errors.New("entry too large")
	}
	data := make([]byte, int(klen)+int(vlen))
	if _, err = file.fp.ReadAt(data, offset+4+4+1); err != nil {
		return nil, nil, false, err
	}
	deleted = header[4+4] != 0
	return data[:klen], data[klen:], deleted, nil
}

func (file *SortedFile) valueAt(pos int) (val []byte, deleted bool, err error) {
	offset, err := file.entryOffset(pos)
	if err != nil {
		return nil, false, err
	}

	var header [4 + 4 + 1]byte
	if _, err = file.fp.ReadAt(header[:], offset); err != nil {
		return nil, false, err
	}
	klen := binary.LittleEndian.Uint32(header[0:4])
	vlen := binary.LittleEndian.Uint32(header[4:8])
	if int64(klen)+int64(vlen) > MaxEntrySize {
		return nil, false, errors.New("entry too large")
	}

	val = make([]byte, int(vlen))
	if vlen != 0 {
		if _, err = file.fp.ReadAt(val, offset+4+4+1+int64(klen)); err != nil {
			return nil, false, err
		}
	}
	return val, header[4+4] != 0, nil
}

func (file *SortedFile) entryOffset(pos int) (int64, error) {
	check(0 <= pos && pos < file.nkeys)

	if len(file.offsetIndex) >= 8*(pos+1) {
		offset := int64(binary.LittleEndian.Uint64(file.offsetIndex[8*pos : 8*(pos+1)]))
		if int64(8+8*file.nkeys) > offset {
			return 0, errors.New("corrupted file")
		}
		return offset, nil
	}

	var buf [8]byte
	if _, err := file.fp.ReadAt(buf[:], int64(8+8*pos)); err != nil {
		return 0, err
	}
	offset := int64(binary.LittleEndian.Uint64(buf[:]))
	if int64(8+8*file.nkeys) > offset {
		return 0, errors.New("corrupted file")
	}
	return offset, nil
}

func (file *SortedFile) compareKeyAt(pos int, target []byte) (int, error) {
	offset, err := file.entryOffset(pos)
	if err != nil {
		return 0, err
	}

	var header [4 + 4 + 1]byte
	if _, err = file.fp.ReadAt(header[:], offset); err != nil {
		return 0, err
	}
	klen := binary.LittleEndian.Uint32(header[0:4])
	vlen := binary.LittleEndian.Uint32(header[4:8])
	if int64(klen)+int64(vlen) > MaxEntrySize {
		return 0, errors.New("entry too large")
	}

	var stackKey [256]byte
	key := stackKey[:]
	if int(klen) > len(stackKey) {
		key = make([]byte, int(klen))
	} else {
		key = key[:klen]
	}
	if _, err = file.fp.ReadAt(key, offset+4+4+1); err != nil {
		return 0, err
	}
	return bytes.Compare(target, key), nil
}

func (file *SortedFile) mayContainKey(key []byte) bool {
	return file.bloom == nil || file.bloom.Test(key)
}

func (file *SortedFile) getExact(key []byte) (val []byte, found bool, deleted bool, err error) {
	if !file.mayContainKey(key) {
		return nil, false, false, nil
	}
	pos, found, err := file.findPosExact(key)
	if err != nil || !found {
		return nil, false, false, err
	}
	val, deleted, err = file.valueAt(pos)
	if err != nil {
		return nil, false, false, err
	}
	return val, true, deleted, nil
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
		r, err := file.compareKeyAt(mid, target)
		if err != nil {
			return -1, err
		}
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

func (file *SortedFile) findPosExact(target []byte) (int, bool, error) {
	lo, hi := 0, file.nkeys
	for lo < hi {
		mid := lo + (hi-lo)/2
		r, err := file.compareKeyAt(mid, target)
		if err != nil {
			return -1, false, err
		}
		if r > 0 {
			lo = mid + 1
		} else if r < 0 {
			hi = mid
		} else {
			return mid, true, nil
		}
	}
	return lo, false, nil
}
