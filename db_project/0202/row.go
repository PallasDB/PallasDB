package db0202

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

func (row Row) EncodeKey(schema *Schema) (key []byte)

func (row Row) EncodeVal(schema *Schema) (val []byte)

func (row Row) DecodeKey(schema *Schema, key []byte) (err error)

func (row Row) DecodeVal(schema *Schema, val []byte) (err error)

// QzBQWVJJOUhU https://trialofcode.org/
