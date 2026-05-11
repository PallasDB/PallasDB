package db0305

import (
	"encoding/json"
	"errors"
)

type DB struct {
	KV     KV
	tables map[string]Schema
}

func (db *DB) Open() error {
	db.tables = map[string]Schema{}
	return db.KV.Open()
}

func (db *DB) Close() error { return db.KV.Close() }

func (db *DB) Select(schema *Schema, row Row) (ok bool, err error) {
	key := row.EncodeKey(schema)
	val, ok, err := db.KV.Get(key)
	if err != nil || !ok {
		return ok, err
	}
	if err = row.DecodeVal(schema, val); err != nil {
		return false, err
	}
	return true, nil
}

func (db *DB) Insert(schema *Schema, row Row) (updated bool, err error) {
	key := row.EncodeKey(schema)
	val := row.EncodeVal(schema)
	return db.KV.SetEx(key, val, ModeInsert)
}

func (db *DB) Upsert(schema *Schema, row Row) (updated bool, err error) {
	key := row.EncodeKey(schema)
	val := row.EncodeVal(schema)
	return db.KV.SetEx(key, val, ModeUpsert)
}

func (db *DB) Update(schema *Schema, row Row) (updated bool, err error) {
	key := row.EncodeKey(schema)
	val := row.EncodeVal(schema)
	return db.KV.SetEx(key, val, ModeUpdate)
}

func (db *DB) Delete(schema *Schema, row Row) (deleted bool, err error) {
	key := row.EncodeKey(schema)
	return db.KV.Del(key)
}

type SQLResult struct {
	Updated int
	Header  []string
	Values  []Row
}

func (db *DB) ExecStmt(stmt interface{}) (r SQLResult, err error) {
	switch ptr := stmt.(type) {
	case *StmtCreatTable:
		err = db.execCreateTable(ptr)
	case *StmtSelect:
		r.Header = ptr.cols
		r.Values, err = db.execSelect(ptr)
	case *StmtInsert:
		r.Updated, err = db.execInsert(ptr)
	case *StmtUpdate:
		r.Updated, err = db.execUpdate(ptr)
	case *StmtDelete:
		r.Updated, err = db.execDelete(ptr)
	default:
		panic("unreachable")
	}
	return
}

func colIndex(schema *Schema, name string) int {
	for i, col := range schema.Cols {
		if col.Name == name {
			return i
		}
	}
	return -1
}

func (db *DB) execCreateTable(stmt *StmtCreatTable) (err error) {
	schema := Schema{Table: stmt.table, Cols: stmt.cols}
	for _, name := range stmt.pkey {
		idx := colIndex(&schema, name)
		if idx < 0 {
			return errors.New("primary key column not found: " + name)
		}
		schema.PKey = append(schema.PKey, idx)
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	_, err = db.KV.SetEx([]byte("@schema_"+stmt.table), data, ModeUpsert)
	if err != nil {
		return err
	}
	db.tables[stmt.table] = schema
	return nil
}

func (db *DB) GetSchema(table string) (Schema, error) {
	schema, ok := db.tables[table]
	if !ok {
		val, ok, err := db.KV.Get([]byte("@schema_" + table))
		if err == nil && ok {
			err = json.Unmarshal(val, &schema)
		}
		if err != nil {
			return Schema{}, err
		}
		if !ok {
			return Schema{}, errors.New("table is not found")
		}
		db.tables[table] = schema
	}
	return schema, nil
}

func buildRowFromKeys(schema *Schema, keys []NamedCell) (Row, error) {
	row := schema.NewRow()
	for i, col := range schema.Cols {
		row[i].Type = col.Type
	}
	for _, nc := range keys {
		idx := colIndex(schema, nc.column)
		if idx < 0 {
			return nil, errors.New("unknown column: " + nc.column)
		}
		row[idx] = nc.value
	}
	return row, nil
}

func (db *DB) execSelect(stmt *StmtSelect) ([]Row, error) {
	schema, err := db.GetSchema(stmt.table)
	if err != nil {
		return nil, err
	}
	row, err := buildRowFromKeys(&schema, stmt.keys)
	if err != nil {
		return nil, err
	}
	ok, err := db.Select(&schema, row)
	if err != nil || !ok {
		return nil, err
	}
	out := Row{}
	for _, colName := range stmt.cols {
		idx := colIndex(&schema, colName)
		if idx < 0 {
			return nil, errors.New("unknown column: " + colName)
		}
		out = append(out, row[idx])
	}
	return []Row{out}, nil
}

func (db *DB) execInsert(stmt *StmtInsert) (count int, err error) {
	schema, err := db.GetSchema(stmt.table)
	if err != nil {
		return 0, err
	}
	if len(stmt.value) != len(schema.Cols) {
		return 0, errors.New("wrong number of values")
	}
	row := schema.NewRow()
	copy(row, stmt.value)
	updated, err := db.Insert(&schema, row)
	if updated {
		count = 1
	}
	return
}

func (db *DB) execUpdate(stmt *StmtUpdate) (count int, err error) {
	schema, err := db.GetSchema(stmt.table)
	if err != nil {
		return 0, err
	}
	row, err := buildRowFromKeys(&schema, stmt.keys)
	if err != nil {
		return 0, err
	}
	ok, err := db.Select(&schema, row)
	if err != nil || !ok {
		return 0, err
	}
	for _, nc := range stmt.value {
		idx := colIndex(&schema, nc.column)
		if idx < 0 {
			return 0, errors.New("unknown column: " + nc.column)
		}
		row[idx] = nc.value
	}
	updated, err := db.Update(&schema, row)
	if updated {
		count = 1
	}
	return
}

func (db *DB) execDelete(stmt *StmtDelete) (count int, err error) {
	schema, err := db.GetSchema(stmt.table)
	if err != nil {
		return 0, err
	}
	row, err := buildRowFromKeys(&schema, stmt.keys)
	if err != nil {
		return 0, err
	}
	deleted, err := db.Delete(&schema, row)
	if deleted {
		count = 1
	}
	return
}

// QzBQWVJJOUhU https://trialofcode.org/
