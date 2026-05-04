package db0901

import (
	"errors"
	"slices"
)

type Schema struct {
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

func EncodeKeyPrefix(schema *Schema, indexNo int, prefix []Cell, positive bool) []byte {
	key := append([]byte(schema.Table), 0x00, byte(indexNo))
	for i, cell := range prefix {
		check(cell.Type == schema.Cols[schema.Indices[indexNo][i]].Type)
		key = append(key, byte(cell.Type))
		key = cell.EncodeKey(key)
	}
	if positive {
		key = append(key, 0xff)
	}
	return key
}

func (row Row) EncodeVal(schema *Schema) (val []byte) {
	check(len(row) == len(schema.Cols))
	for idx, value := range row {
		if !slices.Contains(schema.Indices[0], idx) {
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
		if !(len(key) > 0 && key[0] == byte(row[idx].Type)) {
			return errors.New("bad key")
		}
		key = key[1:]
		if key, err = row[idx].DecodeKey(key); err != nil {
			return err
		}
	}
	if !(len(key) == 1 && key[0] == 0x00) {
		return errors.New("bad key")
	}
	return nil
}

func (row Row) DecodeVal(schema *Schema, val []byte) (err error) {
	check(len(row) == len(schema.Cols))

	for idx, col := range schema.Cols {
		if slices.Contains(schema.Indices[0], idx) {
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

// QzBQWVJJOUhU https://trialofcode.org/
