package db0601

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTableCell(t *testing.T) {
	cell := Cell{Type: TypeI64, I64: -2}
	data := []byte{0xfe, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	assert.Equal(t, data, cell.EncodeVal(nil))
	decoded := Cell{Type: TypeI64}
	rest, err := decoded.DecodeVal(data)
	assert.True(t, len(rest) == 0 && err == nil)
	assert.Equal(t, cell, decoded)

	cell = Cell{Type: TypeStr, Str: []byte("asdf")}
	data = []byte{4, 0, 0, 0, 'a', 's', 'd', 'f'}
	assert.Equal(t, data, cell.EncodeVal(nil))
	decoded = Cell{Type: TypeStr}
	rest, err = decoded.DecodeVal(data)
	assert.True(t, len(rest) == 0 && err == nil)
	assert.Equal(t, cell, decoded)
}

func randString() (out []byte) {
	sz := rand.IntN(256)
	for i := 0; i < sz; i++ {
		out = append(out, byte(rand.Uint32N(256)))
	}
	return out
}

func TestTableCellKey(t *testing.T) {
	cell := Cell{Type: TypeI64, I64: -2}
	data := []byte{0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xfe}
	assert.Equal(t, data, cell.EncodeKey(nil))
	decoded := Cell{Type: TypeI64}
	rest, err := decoded.DecodeKey(data)
	assert.True(t, len(rest) == 0 && err == nil)
	assert.Equal(t, cell, decoded)

	outKeys := []string{}
	for i := -2; i <= 2; i++ {
		cell = Cell{Type: TypeI64, I64: int64(i)}
		outKeys = append(outKeys, string(cell.EncodeKey(nil)))
	}
	assert.True(t, slices.IsSorted(outKeys))

	cell = Cell{Type: TypeStr, Str: []byte("a\x00s\x01d\x02f")}
	data = []byte{'a', 0x01, 0x01, 's', 0x01, 0x02, 'd', 0x02, 'f', 0}
	assert.Equal(t, data, cell.EncodeKey(nil))
	decoded = Cell{Type: TypeStr}
	rest, err = decoded.DecodeKey(data)
	assert.True(t, len(rest) == 0 && err == nil)
	assert.Equal(t, cell, decoded)

	strKeys := []string{}
	for i := 0; i < 10000; i++ {
		strKeys = append(strKeys, string(randString()))
	}
	slices.Sort(strKeys)

	outKeys = []string{}
	for _, s := range strKeys {
		cell := Cell{Type: TypeStr, Str: []byte(s)}
		outKeys = append(outKeys, string(cell.EncodeKey(nil)))

		decoded = Cell{Type: TypeStr}
		rest, err = decoded.DecodeKey([]byte(outKeys[len(outKeys)-1]))
		assert.True(t, len(rest) == 0 && err == nil && string(decoded.Str) == s)
	}
	assert.True(t, slices.IsSorted(outKeys))
}

// QzBQWVJJOUhU https://trialofcode.org/
