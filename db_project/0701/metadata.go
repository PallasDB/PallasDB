package db0701

import (
	"os"
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
	Version uint64
	SSTable string
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

func readMetaFile(fp *os.File) (data KVMetaData, err error)

func writeMetaFile(fp *os.File, data KVMetaData) error

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
