package db

import (
	"errors"
)

// SchemaVersion is the on-disk format version of a persisted Schema. GetSchema
// refuses to load anything else so that a future format change cannot be
// silently misread.
const SchemaVersion = 1

type Schema struct {
	Version int
	Table   string
	Cols    []Column
	Indices [][]int
}

type Column struct {
	Name string
	Type CellType
}

type Row []Cell

func (schema *Schema) NewRow() Row {
	return make(Row, len(schema.Cols))
}

// Validate rejects a schema that would make the encoders panic later. It is
// applied to every schema loaded from storage, where the bytes are not
// necessarily ours, and to every schema created here.
func (schema *Schema) Validate() error {
	if schema.Version != SchemaVersion {
		return errors.New("unsupported schema version")
	}
	if schema.Table == "" || len(schema.Cols) == 0 {
		return errors.New("corrupted schema")
	}
	for _, col := range schema.Cols {
		if col.Name == "" || (col.Type != TypeI64 && col.Type != TypeStr) {
			return errors.New("corrupted schema")
		}
	}
	// the index number is one byte of every key
	if len(schema.Indices) == 0 || len(schema.Indices) > 256 {
		return errors.New("corrupted schema")
	}
	for _, index := range schema.Indices {
		if len(index) == 0 {
			return errors.New("corrupted schema")
		}
		for _, idx := range index {
			if idx < 0 || idx >= len(schema.Cols) {
				return errors.New("corrupted schema")
			}
		}
	}
	return nil
}

func (row Row) EncodeKey(schema *Schema, indexNo int) []byte {
	check(len(row) == len(schema.Cols))
	key := append([]byte(schema.Table), 0x00, byte(indexNo))
	for _, idx := range schema.Indices[indexNo] {
		cell := row[idx]
		check(cell.Type == schema.Cols[idx].Type)
		key = append(key, byte(cell.Type))
		key = cell.EncodeKey(key)
	}
	return append(key, 0x00)
}

// EncodeKeyPrefix encodes a leading subset of an index key. The prefix is
// caller supplied, so its length and cell types are validated instead of
// asserted.
func EncodeKeyPrefix(schema *Schema, indexNo int, prefix []Cell, positive bool) ([]byte, error) {
	if indexNo < 0 || indexNo >= len(schema.Indices) {
		return nil, errors.New("unknown index")
	}
	index := schema.Indices[indexNo]
	if len(prefix) > len(index) {
		return nil, errors.New("key prefix is longer than the index")
	}
	key := append([]byte(schema.Table), 0x00, byte(indexNo))
	for i, cell := range prefix {
		if cell.Type != schema.Cols[index[i]].Type {
			return nil, errors.New("key prefix type mismatch")
		}
		key = append(key, byte(cell.Type))
		key = cell.EncodeKey(key)
	}
	if positive {
		key = append(key, 0xff)
	}
	return key, nil
}

func (row Row) EncodeVal(schema *Schema) (val []byte) {
	check(len(row) == len(schema.Cols))
	pkeySet := make(map[int]struct{}, len(schema.Indices[0]))
	for _, idx := range schema.Indices[0] {
		pkeySet[idx] = struct{}{}
	}
	for idx, value := range row {
		if _, inPKey := pkeySet[idx]; !inPKey {
			check(value.Type == schema.Cols[idx].Type)
			val = row[idx].EncodeVal(val)
		}
	}
	return val
}

var ErrOutOfRange = errors.New("out of range")

func (row Row) DecodeKey(schema *Schema, indexNo int, key []byte) (err error) {
	check(len(row) == len(schema.Cols))

	if len(key) < len(schema.Table)+2 {
		return ErrOutOfRange
	}
	if string(key[:len(schema.Table)+2]) != schema.Table+"\x00"+string(byte(indexNo)) {
		return ErrOutOfRange
	}
	key = key[len(schema.Table)+2:]

	for _, idx := range schema.Indices[indexNo] {
		row[idx] = Cell{Type: schema.Cols[idx].Type}
		if len(key) == 0 || key[0] != byte(row[idx].Type) {
			return errors.New("bad key")
		}
		key = key[1:]
		if key, err = row[idx].DecodeKey(key); err != nil {
			return err
		}
	}
	if len(key) != 1 || key[0] != 0x00 {
		return errors.New("bad key")
	}
	return nil
}

func (row Row) DecodeVal(schema *Schema, val []byte) (err error) {
	check(len(row) == len(schema.Cols))
	pkeySet := make(map[int]struct{}, len(schema.Indices[0]))
	for _, idx := range schema.Indices[0] {
		pkeySet[idx] = struct{}{}
	}
	for idx, col := range schema.Cols {
		if _, inPKey := pkeySet[idx]; inPKey {
			continue
		}
		row[idx] = Cell{Type: col.Type}
		if val, err = row[idx].DecodeVal(val); err != nil {
			return err
		}
	}
	if len(val) != 0 {
		return errors.New("trailing garbage")
	}
	return nil
}
