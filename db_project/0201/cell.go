package db0201

type CellType uint8

const (
	TypeI64 CellType = 1
	TypeStr CellType = 2
)

type Cell struct {
	Type CellType
	I64  int64
	Str  []byte
}

func (cell *Cell) Encode(toAppend []byte) []byte

func (cell *Cell) Decode(data []byte) (rest []byte, err error)

// QzBQWVJJOUhU https://trialofcode.org/
