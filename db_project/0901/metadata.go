package db0901

import (
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"
	"os"
	"slices"
)

type KVMetaStore struct {
	slots [2]KVMetaItem
	MultiClosers
}

type KVMetaItem struct {
	FileName string
	fp       *os.File
	data     KVMetaData
}

type KVMetaData struct {
	Version  uint64
	SSTables []string
}

func (meta *KVMetaStore) Open() error {
	for i := range meta.slots {
		fp, data, err := openMetafile(meta.slots[i].FileName)
		if err != nil {
			_ = meta.Close()
			return err
		}
		meta.slots[i].fp, meta.slots[i].data = fp, data
		meta.MultiClosers = append(meta.MultiClosers, fp)
	}
	return nil
}

func openMetafile(filename string) (fp *os.File, data KVMetaData, err error) {
	if fp, err = createFileSync(filename); err != nil {
		return nil, KVMetaData{}, err
	}
	if data, err = readMetaFile(fp); err != nil {
		_ = fp.Close()
		return nil, KVMetaData{}, err
	}
	return fp, data, nil
}

func readMetaFile(fp *os.File) (data KVMetaData, err error) {
	b, err := io.ReadAll(fp)
	if err != nil {
		return KVMetaData{}, err
	}

	if len(b) <= 8 {
		return KVMetaData{}, nil
	}
	sum := binary.LittleEndian.Uint32(b[0:4])
	size := binary.LittleEndian.Uint32(b[4:8])
	if len(b) < 8+int(size) {
		return KVMetaData{}, nil
	}
	if sum != crc32.ChecksumIEEE(b[4:8+size]) {
		return KVMetaData{}, nil
	}

	if err = json.Unmarshal(b[8:8+size], &data); err != nil {
		return KVMetaData{}, nil
	}
	return data, nil
}

func writeMetaFile(fp *os.File, data KVMetaData) error {
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

// QzBQWVJJOUhU https://trialofcode.org/
