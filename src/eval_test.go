package db0904

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testEval(t *testing.T, schema *Schema, row Row, s string, expected Cell) {
	t.Helper()
	p := NewParser(s)
	expr, err := p.parseExpr()
	require.Nil(t, err)
	require.True(t, p.isEnd())

	out, err := evalExpr(schema, row, expr)
	require.Nil(t, err)
	assert.Equal(t, expected, *out)
}

func makeCell(v any) Cell {
	switch val := v.(type) {
	case int:
		return Cell{Type: TypeI64, I64: int64(val)}
	case string:
		return Cell{Type: TypeStr, Str: []byte(val)}
	default:
		panic("unreachable")
	}
}

func makeRow(vs ...any) (row Row) {
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
		Indices: [][]int{{0}},
	}

	row := makeRow("A", "B", 3, 4)
	testEval(t, schema, row, "a + b", makeCell("AB"))
	testEval(t, schema, row, "c - d", makeCell(-1))
	testEval(t, schema, row, "c * d - d * c + d", makeCell(4))
	testEval(t, schema, row, "d or c and not d = c", makeCell(1))
}

func TestEvalErrors(t *testing.T) {
	schema := &Schema{
		Table: "t",
		Cols: []Column{
			{"a", TypeI64},
			{"b", TypeStr},
		},
		Indices: [][]int{{0}},
	}
	row := makeRow(10, "hello")

	// Unknown column
	p := NewParser("zzz")
	expr, err := p.parseExpr()
	require.NoError(t, err)
	_, err = evalExpr(schema, row, expr)
	assert.Error(t, err)

	// Type mismatch in binary op (i64 + str)
	_, err = evalExpr(schema, row, &ExprBinOp{op: OP_ADD, left: "a", right: "b"})
	assert.Error(t, err)

	// Division by zero
	_, err = evalExpr(schema, row, &ExprBinOp{
		op:    OP_DIV,
		left:  &Cell{Type: TypeI64, I64: 5},
		right: &Cell{Type: TypeI64, I64: 0},
	})
	assert.Error(t, err)

	// Bad unary op on string (OP_NEG on TypeStr)
	_, err = evalExpr(schema, row, &ExprUnOp{op: OP_NEG, kid: "b"})
	assert.Error(t, err)

	// Bad unary op on string (OP_NOT on TypeStr)
	_, err = evalExpr(schema, row, &ExprUnOp{op: OP_NOT, kid: "b"})
	assert.Error(t, err)

	// ExprTuple is not implemented
	_, err = evalExpr(schema, row, &ExprTuple{kids: []any{"a", "b"}})
	assert.Error(t, err)

	// Bad binary op: string division
	_, err = evalExpr(schema, row, &ExprBinOp{
		op:    OP_DIV,
		left:  &Cell{Type: TypeStr, Str: []byte("a")},
		right: &Cell{Type: TypeStr, Str: []byte("b")},
	})
	assert.Error(t, err)

	// Bad binary op: string sub
	_, err = evalExpr(schema, row, &ExprBinOp{
		op:    OP_SUB,
		left:  &Cell{Type: TypeStr, Str: []byte("a")},
		right: &Cell{Type: TypeStr, Str: []byte("b")},
	})
	assert.Error(t, err)
}

func TestEvalComparisonOps(t *testing.T) {
	schema := &Schema{
		Table:   "t",
		Cols:    []Column{{"a", TypeI64}, {"b", TypeStr}},
		Indices: [][]int{{0}},
	}
	row := makeRow(5, "hello")

	cases := []struct {
		op  ExprOp
		l   Cell
		r   Cell
		exp int64
	}{
		{OP_EQ, makeCell(5), makeCell(5), 1},
		{OP_NE, makeCell(5), makeCell(6), 1},
		{OP_LT, makeCell(3), makeCell(5), 1},
		{OP_GT, makeCell(7), makeCell(5), 1},
		{OP_LE, makeCell(5), makeCell(5), 1},
		{OP_GE, makeCell(5), makeCell(5), 1},
		{OP_LE, makeCell(4), makeCell(5), 1},
		{OP_GE, makeCell(6), makeCell(5), 1},
		{OP_EQ, makeCell(3), makeCell(5), 0},
		// String comparisons
		{OP_EQ, makeCell("a"), makeCell("a"), 1},
		{OP_LT, makeCell("a"), makeCell("b"), 1},
		{OP_GT, makeCell("b"), makeCell("a"), 1},
	}
	for _, tc := range cases {
		out, err := evalExpr(schema, row, &ExprBinOp{op: tc.op, left: &tc.l, right: &tc.r})
		assert.NoError(t, err)
		assert.Equal(t, tc.exp, out.I64)
	}
}

func TestEvalBooleanOps(t *testing.T) {
	schema := &Schema{Table: "t", Cols: []Column{{"a", TypeI64}}, Indices: [][]int{{0}}}
	row := makeRow(1)

	// AND: both non-zero
	out, err := evalExpr(schema, row, &ExprBinOp{
		op:    OP_AND,
		left:  &Cell{Type: TypeI64, I64: 1},
		right: &Cell{Type: TypeI64, I64: 1},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), out.I64)

	// AND: one zero
	out, err = evalExpr(schema, row, &ExprBinOp{
		op:    OP_AND,
		left:  &Cell{Type: TypeI64, I64: 1},
		right: &Cell{Type: TypeI64, I64: 0},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), out.I64)

	// OR: both zero
	out, err = evalExpr(schema, row, &ExprBinOp{
		op:    OP_OR,
		left:  &Cell{Type: TypeI64, I64: 0},
		right: &Cell{Type: TypeI64, I64: 0},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), out.I64)

	// OR: one non-zero
	out, err = evalExpr(schema, row, &ExprBinOp{
		op:    OP_OR,
		left:  &Cell{Type: TypeI64, I64: 0},
		right: &Cell{Type: TypeI64, I64: 5},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), out.I64)

	// NOT: non-zero => 0
	out, err = evalExpr(schema, row, &ExprUnOp{op: OP_NOT, kid: &Cell{Type: TypeI64, I64: 3}})
	require.NoError(t, err)
	assert.Equal(t, int64(0), out.I64)

	// NOT: zero => 1
	out, err = evalExpr(schema, row, &ExprUnOp{op: OP_NOT, kid: &Cell{Type: TypeI64, I64: 0}})
	require.NoError(t, err)
	assert.Equal(t, int64(1), out.I64)
}

func TestCell2Str(t *testing.T) {
	assert.Equal(t, "42", cell2str(&Cell{Type: TypeI64, I64: 42}))
	assert.Equal(t, "-1", cell2str(&Cell{Type: TypeI64, I64: -1}))
	assert.Equal(t, "hello", cell2str(&Cell{Type: TypeStr, Str: []byte("hello")}))
	assert.Equal(t, "", cell2str(&Cell{Type: TypeStr, Str: nil}))
}

func TestExpr2Str(t *testing.T) {
	assert.Equal(t, "col", expr2str("col"))
	assert.Equal(t, "7", expr2str(&Cell{Type: TypeI64, I64: 7}))
	assert.Equal(t, "-col", expr2str(&ExprUnOp{op: OP_NEG, kid: "col"}))
	assert.Equal(t, "NOT col", expr2str(&ExprUnOp{op: OP_NOT, kid: "col"}))
	assert.Equal(t, "(a + b)", expr2str(&ExprBinOp{op: OP_ADD, left: "a", right: "b"}))
	assert.Equal(t, "(a, b)", expr2str(&ExprTuple{kids: []any{"a", "b"}}))
	// single-element tuple flattens to the element
	assert.Equal(t, "x", expr2str("x"))
}

func TestExprop2Str(t *testing.T) {
	cases := []struct {
		op  ExprOp
		str string
	}{
		{OP_ADD, "+"}, {OP_SUB, "-"}, {OP_MUL, "*"}, {OP_DIV, "/"},
		{OP_EQ, "="}, {OP_NE, "!="}, {OP_LE, "<="}, {OP_GE, ">="}, {OP_LT, "<"}, {OP_GT, ">"},
		{OP_AND, "AND"}, {OP_OR, "OR"}, {OP_NOT, "NOT"}, {OP_NEG, "-"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.str, exprop2str(tc.op))
	}
}

func TestExprs2Header(t *testing.T) {
	exprs := []any{"col1", &Cell{Type: TypeI64, I64: 0}, &ExprBinOp{op: OP_ADD, left: "a", right: "b"}}
	header := exprs2header(exprs)
	assert.Equal(t, []string{"col1", "0", "(a + b)"}, header)
}

// QzBQWVJJOUhU https://trialofcode.org/
