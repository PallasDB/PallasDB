package db0202

import (
	"fmt"
	"slices"
)

type Schema struct {
	Table string
	Cols  []Column
	PKey  []int // indexes of primary key columns
}

type Column struct {
	Name string
	Type CellType
}

type Row []Cell

func (schema *Schema) NewRow() Row {
	return make(Row, len(schema.Cols))
}

func (row Row) EncodeKey(schema *Schema) (key []byte) {
	// append 0x to the name of the schemaTable
	byteArray := append([]byte(schema.Table), 0x00)
	for _, colIndex := range schema.PKey {
		byteArray = row[colIndex].Encode(byteArray)
	}
	return byteArray
}

func (row Row) EncodeVal(schema *Schema) (val []byte) {
	byteArray := make([]byte, 0)
	for i, _ := range schema.Cols {
		if slices.Contains(schema.PKey, i) {
			continue
		}
		byteArray = row[i].Encode(byteArray)
	}
	return byteArray
}

func (row Row) DecodeKey(schema *Schema, key []byte) (err error) {
	data := key[len(schema.Table)+1:]
	for _, colIndex := range schema.PKey {
		row[colIndex].Type = schema.Cols[colIndex].Type
		if data, err = row[colIndex].Decode(data); err != nil {
			return fmt.Errorf("error decoding data")
		}
	}
	return nil
}

func (row Row) DecodeVal(schema *Schema, val []byte) (err error) {
	for i := range schema.Cols {
		if slices.Contains(schema.PKey, i) {
			continue
		}
		row[i].Type = schema.Cols[i].Type
		if val, err = row[i].Decode(val); err != nil {
			return fmt.Errorf("error decoding data")
		}
	}
	return nil
}

// QzBQWVJJOUhU https://trialofcode.org/
