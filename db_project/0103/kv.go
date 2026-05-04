package db0103

type KV struct {
	log Log
	mem map[string][]byte
}

func (kv *KV) Open() error {
	if err := kv.log.Open(); err != nil {
		return err
	}
	kv.mem = map[string][]byte{}
	for {
		ent := &Entry{}
		eof, err := kv.log.Read(ent)
		if eof {
			break
		}
		if err != nil {
			return err
		}

		if ent.deleted {
			delete(kv.mem, string(ent.key))
		} else {
			kv.mem[string(ent.key)] = ent.val
		}
	}
	return nil
}

func (kv *KV) Close() error { return kv.log.Close() }

func (kv *KV) Get(key []byte) (val []byte, ok bool, err error) {
	val, ok = kv.mem[string(key)]
	return
}

func (kv *KV) Set(key []byte, val []byte) (updated bool, err error) {
	_, exists := kv.mem[string(key)]
	if !exists {
		updated = true
	}
	kv.mem[string(key)] = val
	kv.log.Write(&Entry{key: key, val: val, deleted: false})
	return updated, nil
}

func (kv *KV) Del(key []byte) (deleted bool, err error) {
	_, exists := kv.mem[string(key)]
	if exists {
		delete(kv.mem, string(key))
		deleted = true
	}
	kv.log.Write(&Entry{key: key, val: nil, deleted: true})
	return deleted, nil
}

// QzBQWVJJOUhU https://trialofcode.org/
