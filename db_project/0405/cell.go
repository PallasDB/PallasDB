package db0405

import (
	"encoding/binary"
	"errors"
	"slices"
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

func (cell *Cell) EncodeVal(toAppend []byte) []byte {
	switch cell.Type {
	case TypeI64:
		return binary.LittleEndian.AppendUint64(toAppend, uint64(cell.I64))
	case TypeStr:
		toAppend = binary.LittleEndian.AppendUint32(toAppend, uint32(len(cell.Str)))
		return append(toAppend, cell.Str...)
	default:
		panic("unreachable")
	}
}

func (cell *Cell) DecodeVal(data []byte) (rest []byte, err error) {
	switch cell.Type {
	case TypeI64:
		if len(data) < 8 {
			return data, errors.New("expect more data")
		}
		cell.I64 = int64(binary.LittleEndian.Uint64(data[0:8]))
		return data[8:], nil
	case TypeStr:
		if len(data) < 4 {
			return data, errors.New("expect more data")
		}
		size := int(binary.LittleEndian.Uint32(data[0:4]))
		if len(data) < 4+size {
			return data, errors.New("expect more data")
		}
		cell.Str = slices.Clone(data[4 : 4+size])
		return data[4+size:], nil
	default:
		panic("unreachable")
	}
}

func encodeStrKey(toAppend []byte, input []byte) []byte {
	for _, ch := range input {
		if ch == 0x00 || ch == 0x01 {
			toAppend = append(toAppend, 0x01, ch+1)
		} else {
			toAppend = append(toAppend, ch)
		}
	}
	return append(toAppend, 0x00)
}

func decodeStrKey(data []byte) (out []byte, rest []byte, err error) {
	escape := false
	for i, ch := range data {
		if escape {
			if ch != 0x01 && ch != 0x02 {
				return nil, data, errors.New("bad escape")
			}
			out = append(out, ch-1)
			escape = false
		} else if ch == 0x00 {
			return out, data[i+1:], nil
		} else if ch == 0x01 {
			escape = true
		} else {
			out = append(out, ch)
		}
	}
	return nil, data, errors.New("string is not ended")
}

func (cell *Cell) EncodeKey(toAppend []byte) []byte {
	switch cell.Type {
	case TypeI64:
		return binary.BigEndian.AppendUint64(toAppend, uint64(cell.I64)^(1<<63))
	case TypeStr:
		return encodeStrKey(toAppend, cell.Str)
	default:
		panic("unreachable")
	}
}

func (cell *Cell) DecodeKey(data []byte) (rest []byte, err error) {
	switch cell.Type {
	case TypeI64:
		if len(data) < 8 {
			return data, errors.New("expect more data")
		}
		cell.I64 = int64(binary.BigEndian.Uint64(data[0:8]) ^ (1 << 63))
		return data[8:], nil
	case TypeStr:
		cell.Str, rest, err = decodeStrKey(data)
		return rest, err
	default:
		panic("unreachable")
	}
}

// QzBQWVJJOUhU https://trialofcode.org/
