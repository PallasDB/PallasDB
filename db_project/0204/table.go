package db0204

type DB struct {
	KV KV
}

func (db *DB) Open() error  { return db.KV.Open() }
func (db *DB) Close() error { return db.KV.Close() }

func (db *DB) Select(schema *Schema, row Row) (ok bool, err error) {
	key := row.EncodeKey(schema)
	val, ok, err := db.KV.Get(key)
	if !ok || err != nil {
		return ok, err
	}
	if err = row.DecodeVal(schema, val); err != nil {
		return false, err
	}
	return true, nil
}

func (db *DB) Insert(schema *Schema, row Row) (updated bool, err error) {
	return db.KV.SetEx(row.EncodeKey(schema), row.EncodeVal(schema), ModeInsert)
}

func (db *DB) Upsert(schema *Schema, row Row) (updated bool, err error) {
	return db.KV.SetEx(row.EncodeKey(schema), row.EncodeVal(schema), ModeUpsert)
}

func (db *DB) Update(schema *Schema, row Row) (updated bool, err error) {
	return db.KV.SetEx(row.EncodeKey(schema), row.EncodeVal(schema), ModeUpdate)
}

func (db *DB) Delete(schema *Schema, row Row) (deleted bool, err error) {
	return db.KV.Del(row.EncodeKey(schema))
}

// QzBQWVJJOUhU https://trialofcode.org/
