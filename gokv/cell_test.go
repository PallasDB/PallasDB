package gokv

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
	assert.NoError(t, err)
	assert.Empty(t, rest)
	assert.Equal(t, cell, decoded)

	cell = Cell{Type: TypeStr, Str: []byte("asdf")}
	data = []byte{4, 0, 0, 0, 'a', 's', 'd', 'f'}
	assert.Equal(t, data, cell.EncodeVal(nil))
	decoded = Cell{Type: TypeStr}
	rest, err = decoded.DecodeVal(data)
	assert.NoError(t, err)
	assert.Empty(t, rest)
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
	assert.NoError(t, err)
	assert.Empty(t, rest)
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
	assert.NoError(t, err)
	assert.Empty(t, rest)
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
		assert.NoError(t, err)
		assert.Empty(t, rest)
		assert.Equal(t, s, string(decoded.Str))
	}
	assert.True(t, slices.IsSorted(outKeys))
}

func TestCellDecodeValErrors(t *testing.T) {
	// TypeI64: insufficient data
	c := Cell{Type: TypeI64}
	_, err := c.DecodeVal([]byte{1, 2, 3}) // only 3 bytes, need 8
	assert.Error(t, err)

	// TypeStr: insufficient header (< 4 bytes)
	c = Cell{Type: TypeStr}
	_, err = c.DecodeVal([]byte{1, 2}) // only 2 bytes, need 4 for length prefix
	assert.Error(t, err)

	// TypeStr: insufficient body
	c = Cell{Type: TypeStr}
	// length prefix says 10 bytes, but only 2 bytes follow
	data := []byte{10, 0, 0, 0, 'a', 'b'}
	_, err = c.DecodeVal(data)
	assert.Error(t, err)

	// TypeStr: exact right size - no error
	c = Cell{Type: TypeStr}
	data = []byte{2, 0, 0, 0, 'h', 'i'}
	rest, err := c.DecodeVal(data)
	assert.NoError(t, err)
	assert.Empty(t, rest)
	assert.Equal(t, []byte("hi"), c.Str)
}

func TestCellDecodeKeyErrors(t *testing.T) {
	// TypeI64: insufficient data
	c := Cell{Type: TypeI64}
	_, err := c.DecodeKey([]byte{1, 2, 3}) // only 3 bytes, need 8
	assert.Error(t, err)

	// TypeStr: bad escape sequence
	c = Cell{Type: TypeStr}
	// 0x01 followed by 0x03 is a bad escape (only 0x01 and 0x02 are valid after 0x01)
	_, err = c.DecodeKey([]byte{0x01, 0x03, 0x00})
	assert.Error(t, err)

	// TypeStr: string not terminated (no 0x00 at end)
	c = Cell{Type: TypeStr}
	_, err = c.DecodeKey([]byte{'a', 'b', 'c'}) // no terminator
	assert.Error(t, err)
}

func TestDecodeStrKeyEscapes(t *testing.T) {
	// Test round-trip for strings containing 0x00 and 0x01
	inputs := [][]byte{
		{},
		{0x00},
		{0x01},
		{0x00, 0x01, 0x00},
		{'a', 0x00, 'b', 0x01, 'c'},
	}
	for _, input := range inputs {
		encoded := encodeStrKey(nil, input)
		out, rest, err := decodeStrKey(encoded)
		assert.NoError(t, err)
		assert.Empty(t, rest)
		assert.Equal(t, input, out)
	}
}

func TestCellEncodeKeyI64Ordering(t *testing.T) {
	// Verify that negative, zero and positive int64 keys sort correctly
	values := []int64{-1 << 62, -1, 0, 1, 1 << 62}
	encoded := make([]string, len(values))
	for i, v := range values {
		c := Cell{Type: TypeI64, I64: v}
		encoded[i] = string(c.EncodeKey(nil))
	}
	assert.True(t, slices.IsSorted(encoded))
}

func TestCellEncodeValRoundTrip(t *testing.T) {
	// I64 round-trip with extra trailing data
	c := Cell{Type: TypeI64, I64: 42}
	data := c.EncodeVal(nil)
	extra := append(data, 0xFF, 0xAB)
	dec := Cell{Type: TypeI64}
	rest, err := dec.DecodeVal(extra)
	assert.NoError(t, err)
	assert.Equal(t, []byte{0xFF, 0xAB}, rest)
	assert.Equal(t, c, dec)

	// Str round-trip with trailing data
	c = Cell{Type: TypeStr, Str: []byte("hello")}
	data = c.EncodeVal(nil)
	extra = append(data, 0x99)
	dec = Cell{Type: TypeStr}
	rest, err = dec.DecodeVal(extra)
	assert.NoError(t, err)
	assert.Equal(t, []byte{0x99}, rest)
	assert.Equal(t, c, dec)
}

// QzBQWVJJOUhU https://trialofcode.org/
