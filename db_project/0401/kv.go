package db0401

import (
	"bytes"
	"slices"
)

type KV struct {
	log  Log
	keys [][]byte
	vals [][]byte
}

func (kv *KV) Open() error {
	if err := kv.log.Open(); err != nil {
		return err
	}
	kv.keys = kv.keys[:0]
	kv.vals = kv.vals[:0]
	for {
		ent := Entry{}
		eof, err := kv.log.Read(&ent)
		if err != nil {
			return err
		}
		if eof {
			break
		}
		idx, exist := slices.BinarySearchFunc(kv.keys, ent.key, bytes.Compare)
		if ent.deleted {
			if exist {
				kv.keys = slices.Delete(kv.keys, idx, idx+1)
				kv.vals = slices.Delete(kv.vals, idx, idx+1)
			}
		} else if exist {
			kv.vals[idx] = ent.val
		} else {
			kv.keys = slices.Insert(kv.keys, idx, ent.key)
			kv.vals = slices.Insert(kv.vals, idx, ent.val)
		}
	}
	return nil
}

func (kv *KV) Close() error { return kv.log.Close() }

func (kv *KV) Get(key []byte) (val []byte, ok bool, err error) {
	if idx, ok := slices.BinarySearchFunc(kv.keys, key, bytes.Compare); ok {
		return kv.vals[idx], true, nil
	}
	return nil, false, nil
}

type UpdateMode int

const (
	ModeUpsert UpdateMode = 0 // insert or update
	ModeInsert UpdateMode = 1 // insert new
	ModeUpdate UpdateMode = 2 // update existing
)

func (kv *KV) SetEx(key []byte, val []byte, mode UpdateMode) (updated bool, err error) {
	idx, exist := slices.BinarySearchFunc(kv.keys, key, bytes.Compare)
	switch mode {
	case ModeUpsert:
		updated = !exist || !bytes.Equal(kv.vals[idx], val)
	case ModeInsert:
		updated = !exist
	case ModeUpdate:
		updated = exist && !bytes.Equal(kv.vals[idx], val)
	default:
		panic("unreachable")
	}
	if updated {
		if err = kv.log.Write(&Entry{key: key, val: val}); err != nil {
			return false, err
		}
		if exist {
			kv.vals[idx] = val
		} else {
			kv.keys = slices.Insert(kv.keys, idx, key)
			kv.vals = slices.Insert(kv.vals, idx, val)
		}
	}
	return updated, nil
}

func (kv *KV) Set(key []byte, val []byte) (updated bool, err error) {
	return kv.SetEx(key, val, ModeUpsert)
}

func (kv *KV) Del(key []byte) (deleted bool, err error) {
	idx, exist := slices.BinarySearchFunc(kv.keys, key, bytes.Compare)
	if !exist {
		return false, nil
	}
	if err = kv.log.Write(&Entry{key: key, deleted: true}); err != nil {
		return false, err
	}
	kv.keys = slices.Delete(kv.keys, idx, idx+1)
	kv.vals = slices.Delete(kv.vals, idx, idx+1)
	return true, nil
}

// QzBQWVJJOUhU https://trialofcode.org/
