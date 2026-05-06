package db0201

import (
	"encoding/binary"
	"fmt"
)

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

func (cell *Cell) Encode(toAppend []byte) []byte {
	switch cell.Type {
	case TypeI64:
		toAppend = binary.LittleEndian.AppendUint64(toAppend, uint64(cell.I64))
	case TypeStr:
		toAppend = binary.LittleEndian.AppendUint32(toAppend, uint32(len(cell.Str)))
		toAppend = append(toAppend, cell.Str...)
	}
	return toAppend
}

func (cell *Cell) Decode(data []byte) (rest []byte, err error) {
	switch cell.Type {
	case TypeI64:
		if len(data) < 8 {
			return nil, fmt.Errorf("not enough bytes for int64")
		}
		cell.I64 = int64(binary.LittleEndian.Uint64(data[:8]))
		cell.Str = nil
		return data[8:], nil
	case TypeStr:
		if len(data) < 4 {
			return nil, fmt.Errorf("not enough bytes for string")
		}
		cell.I64 = 0

		length := binary.LittleEndian.Uint32(data[:4])
		n := int(length)

		if len(data) < 4+n {
			return nil, fmt.Errorf("not enough bytes for string data")
		}

		cell.Str = data[4 : 4+n]
		return data[4+n:], nil
	}
	return nil, fmt.Errorf("unknown cell type")
}

// QzBQWVJJOUhU https://trialofcode.org/
