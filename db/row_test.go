package db

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRowEncode(t *testing.T) {
	schema := &Schema{
		Table: "link",
		Cols: []Column{
			{Name: "time", Type: TypeI64},
			{Name: "src", Type: TypeStr},
			{Name: "dst", Type: TypeStr},
		},
		Indices: [][]int{{2, 1}}, // (dst, src)
	}

	row := Row{
		Cell{Type: TypeI64, I64: 123},
		Cell{Type: TypeStr, Str: []byte("a")},
		Cell{Type: TypeStr, Str: []byte("b")},
	}
	key := []byte{'l', 'i', 'n', 'k', 0, 0, byte(TypeStr), 'b', 0, byte(TypeStr), 'a', 0, 0}
	val := []byte{123, 0, 0, 0, 0, 0, 0, 0}
	assert.Equal(t, key, row.EncodeKey(schema, 0))
	assert.Equal(t, val, row.EncodeVal(schema))

	decoded := schema.NewRow()
	err := decoded.DecodeKey(schema, 0, key)
	assert.Nil(t, err)
	err = decoded.DecodeVal(schema, val)
	assert.Nil(t, err)
	assert.Equal(t, row, decoded)

	rows := []Row{
		{
			Cell{Type: TypeI64, I64: 123},
			Cell{Type: TypeStr, Str: []byte("ba")},
			Cell{Type: TypeStr, Str: []byte("b")},
		},
		{
			Cell{Type: TypeI64, I64: 123},
			Cell{Type: TypeStr, Str: []byte("a")},
			Cell{Type: TypeStr, Str: []byte("bb")},
		},
		{
			Cell{Type: TypeI64, I64: 123},
			Cell{Type: TypeStr, Str: []byte("a")},
			Cell{Type: TypeStr, Str: []byte("bba")},
		},
	}
	keys := []string{}
	for _, row = range rows {
		key = row.EncodeKey(schema, 0)
		keys = append(keys, string(key))

		decoded = schema.NewRow()
		err = decoded.DecodeKey(schema, 0, key)
		assert.Nil(t, err)
		err = decoded.DecodeVal(schema, val)
		assert.Nil(t, err)
		assert.Equal(t, row, decoded)
	}
	assert.True(t, slices.IsSorted(keys))
}

func TestRowDecodeKeyErrors(t *testing.T) {
	schema := &Schema{
		Table: "link",
		Cols: []Column{
			{Name: "src", Type: TypeStr},
			{Name: "dst", Type: TypeStr},
		},
		Indices: [][]int{{0, 1}},
	}

	row := schema.NewRow()

	// Key too short
	err := row.DecodeKey(schema, 0, []byte("li"))
	assert.ErrorIs(t, err, ErrOutOfRange)

	// Wrong table name prefix
	err = row.DecodeKey(schema, 0, []byte("other\x00\x00..."))
	assert.ErrorIs(t, err, ErrOutOfRange)

	// Wrong index byte
	err = row.DecodeKey(schema, 0, []byte("link\x00\x01"))
	assert.ErrorIs(t, err, ErrOutOfRange)

	// Valid prefix but wrong type byte in column
	key := append([]byte("link\x00\x00"), 0xFF) // 0xFF is not a valid CellType
	err = row.DecodeKey(schema, 0, key)
	assert.Error(t, err)
}

func TestRowDecodeValErrors(t *testing.T) {
	schema := &Schema{
		Table: "t",
		Cols: []Column{
			{Name: "k", Type: TypeI64},
			{Name: "v", Type: TypeI64},
		},
		Indices: [][]int{{0}}, // k is primary key - only v is in val
	}

	row := schema.NewRow()

	// Insufficient val data
	err := row.DecodeVal(schema, []byte{1}) // need 8 bytes for int64
	assert.Error(t, err)

	// Trailing garbage
	valData := make([]byte, 8+1) // 8 for int64 + 1 extra
	err = row.DecodeVal(schema, valData)
	assert.Error(t, err)
}

func TestEncodeKeyPrefix(t *testing.T) {
	schema := &Schema{
		Table: "t",
		Cols: []Column{
			{Name: "a", Type: TypeI64},
			{Name: "b", Type: TypeStr},
		},
		Indices: [][]int{{0, 1}},
	}

	// Empty prefix (no cells) - should produce just the table header
	key, err := EncodeKeyPrefix(schema, 0, nil, false)
	require.NoError(t, err)
	assert.True(t, len(key) > 0)
	assert.Equal(t, []byte("t\x00\x00"), key)

	// With positive suffix
	key, err = EncodeKeyPrefix(schema, 0, nil, true)
	require.NoError(t, err)
	assert.Equal(t, []byte("t\x00\x00\xff"), key)

	// With one cell prefix
	cells := []Cell{{Type: TypeI64, I64: 42}}
	key, err = EncodeKeyPrefix(schema, 0, cells, false)
	require.NoError(t, err)
	assert.True(t, len(key) > len("t\x00\x00"))

	// A prefix longer than the index, an unknown index or a mistyped cell are
	// caller errors, not panics.
	_, err = EncodeKeyPrefix(schema, 0, []Cell{{Type: TypeI64}, {Type: TypeStr}, {Type: TypeI64}}, false)
	assert.Error(t, err)
	_, err = EncodeKeyPrefix(schema, 7, nil, false)
	assert.Error(t, err)
	_, err = EncodeKeyPrefix(schema, 0, []Cell{{Type: TypeStr}}, false)
	assert.Error(t, err)
}

func TestRowEncodeDecodeAllTypes(t *testing.T) {
	schema := &Schema{
		Table: "mixed",
		Cols: []Column{
			{Name: "id", Type: TypeI64},
			{Name: "name", Type: TypeStr},
			{Name: "score", Type: TypeI64},
		},
		Indices: [][]int{{0}},
	}

	rows := []Row{
		{
			Cell{Type: TypeI64, I64: 0},
			Cell{Type: TypeStr, Str: []byte("")},
			Cell{Type: TypeI64, I64: -9999},
		},
		{
			Cell{Type: TypeI64, I64: -1},
			Cell{Type: TypeStr, Str: []byte("hello world")},
			Cell{Type: TypeI64, I64: 1 << 62},
		},
	}
	for _, row := range rows {
		key := row.EncodeKey(schema, 0)
		val := row.EncodeVal(schema)

		decoded := schema.NewRow()
		err := decoded.DecodeKey(schema, 0, key)
		require.NoError(t, err)
		err = decoded.DecodeVal(schema, val)
		require.NoError(t, err)
		assert.Equal(t, row, decoded)
	}
}

func TestSchemaValidate(t *testing.T) {
	good := Schema{
		Version: SchemaVersion,
		Table:   "t",
		Cols:    []Column{{"a", TypeI64}, {"b", TypeStr}},
		Indices: [][]int{{0}, {1, 0}},
	}
	require.NoError(t, good.Validate())

	bad := func(mutate func(s *Schema)) {
		t.Helper()
		s := good
		s.Cols = slices.Clone(good.Cols)
		s.Indices = slices.Clone(good.Indices)
		mutate(&s)
		assert.Error(t, s.Validate())
	}
	bad(func(s *Schema) { s.Version = SchemaVersion + 1 })
	bad(func(s *Schema) { s.Table = "" })
	bad(func(s *Schema) { s.Cols = nil })
	bad(func(s *Schema) { s.Cols[1] = Column{"b", CellType(9)} })
	bad(func(s *Schema) { s.Cols[1] = Column{"", TypeStr} })
	bad(func(s *Schema) { s.Indices = nil })
	bad(func(s *Schema) { s.Indices[1] = nil })
	bad(func(s *Schema) { s.Indices[1] = []int{7} })
	bad(func(s *Schema) { s.Indices[1] = []int{-1} })
	bad(func(s *Schema) {
		s.Indices = make([][]int, 257)
		for i := range s.Indices {
			s.Indices[i] = []int{0}
		}
	})
}
