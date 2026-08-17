package db

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"slices"
)

type KVMetaStore struct {
	slots [2]KVMetaItem
	MultiClosers
}

type KVMetaItem struct {
	FileName string
	fp       fileLike
	data     KVMetaData
}

type KVMetaData struct {
	Version  uint64
	SSTables []string
}

var (
	// ErrMetaCorrupt reports a metadata slot whose contents are present but
	// unusable. It is deliberately distinct from an absent slot: "I cannot read
	// what is there" and "there is nothing there yet" call for opposite
	// responses, and conflating them is how a damaged store silently opens as
	// an empty one and then overwrites its own SSTables.
	ErrMetaCorrupt = errors.New("metadata slot is corrupt")
	// ErrMetaUnreadable reports that no slot carries a usable snapshot even
	// though the directory still holds data files.
	ErrMetaUnreadable = errors.New("metadata is unreadable but the store is not empty")
)

// errMetaAbsent marks an empty slot file. It never escapes this file: an absent
// slot is the normal state of a fresh database.
var errMetaAbsent = errors.New("metadata slot is absent")

// metaSSTablePrefix must match the SSTable naming used by (*KV).compactLog and
// (*KV).compactSSTable. It is what makes "the directory is not empty" a
// meaningful question: those are exactly the files a silent empty open would
// renumber and overwrite.
const metaSSTablePrefix = "sstable_"

func (meta *KVMetaStore) Open() error {
	var slotErr [2]error
	usable := false
	for i := range meta.slots {
		fp, data, err := openMetafile(meta.slots[i].FileName)
		if fp == nil {
			_ = meta.Close()
			meta.slots[0].fp, meta.slots[1].fp = nil, nil
			return fmt.Errorf("open %s: %w", meta.slots[i].FileName, err)
		}
		meta.slots[i].fp = fp
		meta.MultiClosers = append(meta.MultiClosers, fp)
		if err != nil {
			// Leave the slot's data zeroed so it can never win current() and
			// so the next Set overwrites it, healing the store.
			meta.slots[i].data = KVMetaData{}
			slotErr[i] = fmt.Errorf("%s: %w", filepath.Base(meta.slots[i].FileName), err)
			continue
		}
		meta.slots[i].data = data
		usable = true
	}
	if usable {
		return nil
	}
	if err := meta.checkNotEmpty(slotErr); err != nil {
		_ = meta.Close()
		meta.slots[0].fp, meta.slots[1].fp = nil, nil
		return err
	}
	return nil
}

// checkNotEmpty decides whether a store with no readable metadata slot is a
// fresh database or a disaster. Double buffering covers one damaged slot; when
// both are gone the only honest answer, if data files are still on disk, is to
// refuse to open. Opening anyway would report zero SSTables at version 0 and
// the next compaction would start renumbering over the surviving files.
func (meta *KVMetaStore) checkNotEmpty(slotErr [2]error) error {
	leftovers, err := countSSTables(filepath.Dir(meta.slots[0].FileName))
	if err != nil {
		return err
	}
	if leftovers == 0 {
		return nil // genuinely empty directory: a fresh database
	}
	return fmt.Errorf("%w: %d %s* file(s) present: %w; %w",
		ErrMetaUnreadable, leftovers, metaSSTablePrefix,
		orAbsent(slotErr[0]), orAbsent(slotErr[1]))
}

func orAbsent(err error) error {
	if err == nil {
		return errMetaAbsent
	}
	return err
}

func countSSTables(dirpath string) (int, error) {
	entries, err := os.ReadDir(dirpath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > len(metaSSTablePrefix) &&
			e.Name()[:len(metaSSTablePrefix)] == metaSSTablePrefix {
			n++
		}
	}
	return n, nil
}

// openMetafile returns the open file even when its contents could not be read,
// so that the caller keeps a handle it can write a fresh snapshot into. fp is
// nil only when the file itself could not be opened.
func openMetafile(filename string) (fp fileLike, data KVMetaData, err error) {
	if fp, err = openFileSync(filename); err != nil {
		return nil, KVMetaData{}, err
	}
	data, err = readMetaFile(fp)
	return fp, data, err
}

func readMetaFile(fp fileLike) (data KVMetaData, err error) {
	b, err := readAllAt(fp)
	if err != nil {
		return KVMetaData{}, err
	}

	if len(b) == 0 {
		return KVMetaData{}, errMetaAbsent
	}
	if len(b) <= 8 {
		// writeMetaFile always emits an 8 byte header plus a non-empty JSON
		// payload, so anything shorter is a torn write, not a fresh slot.
		return KVMetaData{}, fmt.Errorf("%w: short file (%d bytes)", ErrMetaCorrupt, len(b))
	}
	sum := binary.LittleEndian.Uint32(b[0:4])
	size := binary.LittleEndian.Uint32(b[4:8])
	if uint64(len(b)) < 8+uint64(size) {
		return KVMetaData{}, fmt.Errorf("%w: truncated payload: have %d bytes, header claims %d",
			ErrMetaCorrupt, len(b)-8, size)
	}
	if sum != crc32.ChecksumIEEE(b[4:8+size]) {
		return KVMetaData{}, fmt.Errorf("%w: checksum mismatch", ErrMetaCorrupt)
	}
	if err = json.Unmarshal(b[8:8+size], &data); err != nil {
		return KVMetaData{}, fmt.Errorf("%w: %v", ErrMetaCorrupt, err)
	}
	return data, nil
}

func readAllAt(fp fileLike) ([]byte, error) {
	st, err := fp.Stat()
	if err != nil {
		return nil, err
	}
	b := make([]byte, st.Size())
	n, err := fp.ReadAt(b, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return b[:n], nil
}

func writeMetaFile(fp fileLike, data KVMetaData) error {
	b, err := json.Marshal(data)
	check(err == nil)
	b = slices.Concat(make([]byte, 8), b)
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8))
	binary.LittleEndian.PutUint32(b[0:4], crc32.ChecksumIEEE(b[4:]))
	if _, err = fp.WriteAt(b, 0); err != nil {
		return err
	}
	return fp.Sync()
}

func (meta *KVMetaStore) current() int {
	if meta.slots[0].data.Version > meta.slots[1].data.Version {
		return 0
	} else {
		return 1
	}
}

func (meta *KVMetaStore) Get() KVMetaData {
	return meta.slots[meta.current()].data
}

func (meta *KVMetaStore) Set(data KVMetaData) error {
	cur := meta.current()
	if err := writeMetaFile(meta.slots[1-cur].fp, data); err != nil {
		return err
	}
	meta.slots[1-cur].data = data
	return nil
}
