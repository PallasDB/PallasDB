package db0101

type KV struct {
	mem map[string][]byte
}

func (kv *KV) Open() error {
	kv.mem = map[string][]byte{} // empty
	return nil
}

func (kv *KV) Close() error { return nil }

func (kv *KV) Get(key []byte) (val []byte, ok bool, err error)

func (kv *KV) Set(key []byte, val []byte) (updated bool, err error)

func (kv *KV) Del(key []byte) (deleted bool, err error)

// QzBQWVJJOUhU https://trialofcode.org/
