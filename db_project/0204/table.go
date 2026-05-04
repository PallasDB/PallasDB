package db0204

type DB struct {
	KV KV
}

func (db *DB) Open() error  { return db.KV.Open() }
func (db *DB) Close() error { return db.KV.Close() }

func (db *DB) Select(schema *Schema, row Row) (ok bool, err error)

func (db *DB) Insert(schema *Schema, row Row) (updated bool, err error)

func (db *DB) Upsert(schema *Schema, row Row) (updated bool, err error)

func (db *DB) Update(schema *Schema, row Row) (updated bool, err error)

func (db *DB) Delete(schema *Schema, row Row) (deleted bool, err error)

// QzBQWVJJOUhU https://trialofcode.org/
