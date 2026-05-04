package db0102

type KV struct {
	mem map[string][]byte
}

func (kv *KV) Open() error {
	kv.mem = map[string][]byte{} // empty
	return nil
}

func (kv *KV) Close() error { return nil }

func (kv *KV) Get(key []byte) (val []byte, ok bool, err error) {
	x, ok := kv.mem[string(key)]
	return x, ok, nil
}

func (kv *KV) Set(key []byte, val []byte) (updated bool, err error) {
	_, exists := kv.mem[string(key)]
	if !exists {
		updated = true
	}
	kv.mem[string(key)] = val
	return updated, nil
}

func (kv *KV) Del(key []byte) (deleted bool, err error) {
	_, exists := kv.mem[string(key)]
	if exists {
		delete(kv.mem, string(key))
		deleted = true
	}
	return deleted, nil
}

// QzBQWVJJOUhU https://trialofcode.org/
