package db0506

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testEval(t *testing.T, schema *Schema, row Row, s string, expected Cell) {
	p := NewParser(s)
	expr, err := p.parseExpr()
	require.Nil(t, err)
	require.True(t, p.isEnd())

	out, err := evalExpr(schema, row, expr)
	require.Nil(t, err)
	assert.Equal(t, expected, *out)
}

func makeCell(v interface{}) Cell {
	switch val := v.(type) {
	case int:
		return Cell{Type: TypeI64, I64: int64(val)}
	case string:
		return Cell{Type: TypeStr, Str: []byte(val)}
	default:
		panic("unreachable")
	}
}

func makeRow(vs ...interface{}) (row Row) {
	for _, v := range vs {
		row = append(row, makeCell(v))
	}
	return row
}

func TestEval(t *testing.T) {
	schema := &Schema{
		Table: "t",
		Cols: []Column{
			{"a", TypeStr},
			{"b", TypeStr},
			{"c", TypeI64},
			{"d", TypeI64},
		},
		PKey: []int{0},
	}

	row := makeRow("A", "B", 3, 4)
	testEval(t, schema, row, "a + b", makeCell("AB"))
	testEval(t, schema, row, "c - d", makeCell(-1))
	testEval(t, schema, row, "c * d - d * c + d", makeCell(4))
	testEval(t, schema, row, "d or c and not d = c", makeCell(1))
}

// QzBQWVJJOUhU https://trialofcode.org/
