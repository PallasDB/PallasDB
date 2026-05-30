package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseName(t *testing.T) {
	p := NewParser(" a b0 _0_ 123 ")
	name, ok := p.tryName()
	assert.True(t, ok && name == "a")
	name, ok = p.tryName()
	assert.True(t, ok && name == "b0")
	name, ok = p.tryName()
	assert.True(t, ok && name == "_0_")
	_, ok = p.tryName()
	assert.False(t, ok)
}

func TestParseKeyword(t *testing.T) {
	p := NewParser(" select  HELLO ")
	assert.False(t, p.tryKeyword("sel"))
	assert.True(t, p.tryKeyword("SELECT"))
	assert.True(t, p.tryKeyword("hello") && p.isEnd())

	p = NewParser(" select  HELLO ")
	assert.False(t, p.tryKeyword("select", "hi"))
	assert.True(t, p.tryKeyword("select", "hello") && p.isEnd())
}

func testParseValue(t *testing.T, s string, ref Cell) {
	t.Helper()
	p := NewParser(s)
	out := Cell{}
	err := p.parseValue(&out)
	assert.Nil(t, err)
	assert.True(t, p.isEnd())
	assert.Equal(t, ref, out)
}

func TestParseValue(t *testing.T) {
	testParseValue(t, " -123 ", Cell{Type: TypeI64, I64: -123})
	testParseValue(t, ` 'abc\'\"d' `, Cell{Type: TypeStr, Str: []byte("abc'\"d")})
	testParseValue(t, ` "abc\'\"d" `, Cell{Type: TypeStr, Str: []byte("abc'\"d")})
}

func testParseStmt(t *testing.T, s string, ref any) {
	t.Helper()
	p := NewParser(s)
	out, err := p.parseStmt()
	assert.Nil(t, err)
	assert.True(t, p.isEnd())
	assert.Equal(t, ref, out)
}

func TestParseStmt(t *testing.T) {
	var stmt any
	s := "select a from t where c=1;"
	stmt = &StmtSelect{
		table: "t",
		cols:  []any{"a"},
		cond:  &ExprBinOp{op: OP_EQ, left: "c", right: &Cell{Type: TypeI64, I64: 1}},
	}
	testParseStmt(t, s, stmt)

	s = "select a,b_02 from T where c=1 and d='e';"
	stmt = &StmtSelect{
		table: "T",
		cols:  []any{"a", "b_02"},
		cond: &ExprBinOp{op: OP_AND,
			left:  &ExprBinOp{op: OP_EQ, left: "c", right: &Cell{Type: TypeI64, I64: 1}},
			right: &ExprBinOp{op: OP_EQ, left: "d", right: &Cell{Type: TypeStr, Str: []byte("e")}},
		},
	}
	testParseStmt(t, s, stmt)

	s = "select a, b_02 from T where c = 1 and d = 'e' ; "
	testParseStmt(t, s, stmt)

	s = "create table t (a string, b int64, primary key (b));"
	stmt = &StmtCreatTable{
		table: "t",
		cols:  []Column{{"a", TypeStr}, {"b", TypeI64}},
		pkey:  []string{"b"},
	}
	testParseStmt(t, s, stmt)

	s = "insert into t values (1, 'hi');"
	stmt = &StmtInsert{
		table: "t",
		value: []Cell{{Type: TypeI64, I64: 1}, {Type: TypeStr, Str: []byte("hi")}},
	}
	testParseStmt(t, s, stmt)

	s = "update t set a = 1, b = 2 where c = 3 and d = 4;"
	stmt = &StmtUpdate{
		table: "t",
		value: []ExprAssign{{"a", &Cell{Type: TypeI64, I64: 1}}, {"b", &Cell{Type: TypeI64, I64: 2}}},
		cond: &ExprBinOp{op: OP_AND,
			left:  &ExprBinOp{op: OP_EQ, left: "c", right: &Cell{Type: TypeI64, I64: 3}},
			right: &ExprBinOp{op: OP_EQ, left: "d", right: &Cell{Type: TypeI64, I64: 4}},
		},
	}
	testParseStmt(t, s, stmt)

	s = "delete from t where c = 3 and d = 4;"
	stmt = &StmtDelete{
		table: "t",
		cond: &ExprBinOp{op: OP_AND,
			left:  &ExprBinOp{op: OP_EQ, left: "c", right: &Cell{Type: TypeI64, I64: 3}},
			right: &ExprBinOp{op: OP_EQ, left: "d", right: &Cell{Type: TypeI64, I64: 4}},
		},
	}
	testParseStmt(t, s, stmt)

	s = "delete from t where (c, d) >= (3, 4);"
	stmt = &StmtDelete{
		table: "t",
		cond: &ExprBinOp{op: OP_GE,
			left:  &ExprTuple{kids: []any{"c", "d"}},
			right: &ExprTuple{kids: []any{&Cell{Type: TypeI64, I64: 3}, &Cell{Type: TypeI64, I64: 4}}},
		},
	}
	testParseStmt(t, s, stmt)
}

func testParseExpr(t *testing.T, s string, expr any) {
	t.Helper()
	p := NewParser(s)
	out, err := p.parseExpr()
	require.Nil(t, err)
	assert.Equal(t, expr, out)
	assert.True(t, p.isEnd())
}

func TestParseExpr(t *testing.T) {
	var expr any

	testParseExpr(t, "a", "a")
	testParseExpr(t, "(a)", "a")
	testParseExpr(t, "1", &Cell{Type: TypeI64, I64: 1})

	s := "a + 1"
	expr = &ExprBinOp{op: OP_ADD, left: "a", right: &Cell{Type: TypeI64, I64: 1}}
	testParseExpr(t, s, expr)

	s = "a + 1 - b"
	expr = &ExprBinOp{op: OP_SUB,
		left:  &ExprBinOp{op: OP_ADD, left: "a", right: &Cell{Type: TypeI64, I64: 1}},
		right: "b",
	}
	testParseExpr(t, s, expr)

	s = "a + b * c"
	expr = &ExprBinOp{op: OP_ADD,
		left:  "a",
		right: &ExprBinOp{op: OP_MUL, left: "b", right: "c"},
	}
	testParseExpr(t, s, expr)

	s = "(a * b)"
	expr = &ExprBinOp{op: OP_MUL, left: "a", right: "b"}
	testParseExpr(t, s, expr)

	s = "(a + b) / c"
	expr = &ExprBinOp{op: OP_DIV,
		left:  &ExprBinOp{op: OP_ADD, left: "a", right: "b"},
		right: "c",
	}
	testParseExpr(t, s, expr)

	s = "f or e and not d = a + b * -c"
	expr = &ExprBinOp{op: OP_OR,
		left: "f", right: &ExprBinOp{op: OP_AND,
			left: "e", right: &ExprUnOp{op: OP_NOT,
				kid: &ExprBinOp{op: OP_EQ,
					left: "d", right: &ExprBinOp{op: OP_ADD,
						left: "a", right: &ExprBinOp{op: OP_MUL,
							left: "b", right: &ExprUnOp{op: OP_NEG,
								kid: "c"}}}}}}}
	testParseExpr(t, s, expr)

	s = "not not - - a"
	expr = &ExprUnOp{op: OP_NOT,
		kid: &ExprUnOp{op: OP_NOT,
			kid: &ExprUnOp{op: OP_NEG,
				kid: &ExprUnOp{op: OP_NEG,
					kid: "a"}}}}
	testParseExpr(t, s, expr)
}

func TestParseValueErrors(t *testing.T) {
	// empty input
	p := NewParser("")
	err := p.parseValue(&Cell{})
	assert.Error(t, err)

	// unexpected character
	p = NewParser("@")
	err = p.parseValue(&Cell{})
	assert.Error(t, err)

	// unterminated string
	p = NewParser(`"hello`)
	err = p.parseValue(&Cell{})
	assert.Error(t, err)

	// bad escape in string
	p = NewParser(`"hello\x"`)
	err = p.parseValue(&Cell{})
	assert.Error(t, err)

	// sign only (no digits)
	p = NewParser("-")
	err = p.parseValue(&Cell{})
	assert.Error(t, err)

	// plus sign only
	p = NewParser("+")
	err = p.parseValue(&Cell{})
	assert.Error(t, err)
}

func TestParseStmtErrors(t *testing.T) {
	bad := []string{
		// unknown statement
		"FOOBAR;",
		// SELECT: missing column list
		"select from t where a=1;",
		// SELECT: missing FROM
		"select a,b;",
		// SELECT: missing table name
		"select a from where a=1;",
		// INSERT: missing VALUES
		"insert into t (1, 2);",
		// INSERT: missing semicolon
		"insert into t values (1, 2)",
		// CREATE TABLE: missing semicolon
		"create table t (a int64, primary key (a))",
		// CREATE TABLE: unknown column type
		"create table t (a float64, primary key (a));",
		// UPDATE: missing SET
		"update t where a=1;",
		// DELETE: missing WHERE
		"delete from t;",
	}
	for _, s := range bad {
		t.Run(s, func(t *testing.T) {
			p := NewParser(s)
			_, err := p.parseStmt()
			assert.Error(t, err, "expected parse error for: %s", s)
		})
	}
}

func TestParsePunctuation(t *testing.T) {
	p := NewParser("  (  )  ")
	assert.True(t, p.tryPunctuation("("))
	assert.True(t, p.tryPunctuation(")"))
	assert.True(t, p.isEnd())

	// punctuation not present
	p = NewParser("a")
	assert.False(t, p.tryPunctuation("("))
}

func TestParseKeywordEdgeCases(t *testing.T) {
	// keyword at end of buffer with no trailing separator
	p := NewParser("FROM")
	assert.True(t, p.tryKeyword("FROM"))
	assert.True(t, p.isEnd())

	// keyword followed by non-separator (should not match)
	p = NewParser("FROMtable")
	assert.False(t, p.tryKeyword("FROM"))

	// multiple keywords - first matches, second doesn't
	p = NewParser("SELECT INSERT")
	assert.False(t, p.tryKeyword("SELECT", "FROM"))
	assert.True(t, p.tryKeyword("SELECT", "INSERT"))
	assert.True(t, p.isEnd())
}

func TestParseTuple(t *testing.T) {
	// single element tuple - returns the expression itself
	p := NewParser("(a)")
	expr, err := p.parseTuple()
	require.NoError(t, err)
	assert.Equal(t, "a", expr)

	// multi-element tuple
	p = NewParser("(a, b, c)")
	expr, err = p.parseTuple()
	require.NoError(t, err)
	assert.Equal(t, &ExprTuple{kids: []any{"a", "b", "c"}}, expr)

	// empty tuple - error
	p = NewParser("()")
	_, err = p.parseTuple()
	assert.Error(t, err)
}

func TestParseSelectWithIndex(t *testing.T) {
	// SELECT with index-based range query
	s := "select time from link where (src, dst) >= ('bob', 'alice');"
	p := NewParser(s)
	stmt, err := p.parseStmt()
	require.NoError(t, err)
	require.True(t, p.isEnd())
	sel, ok := stmt.(*StmtSelect)
	require.True(t, ok)
	assert.Equal(t, "link", sel.table)
}

func TestParseCreateTableWithIndex(t *testing.T) {
	s := "create table t (a int64, b string, primary key (a), index (b));"
	p := NewParser(s)
	stmt, err := p.parseStmt()
	require.NoError(t, err)
	ct, ok := stmt.(*StmtCreatTable)
	require.True(t, ok)
	assert.Equal(t, "t", ct.table)
	assert.Equal(t, [][]string{{"b"}}, ct.indices)
	assert.Equal(t, []string{"a"}, ct.pkey)
}

func TestParseStringEscapes(t *testing.T) {
	// escaped single quote inside double-quoted string
	p := NewParser(`"it\'s"`)
	c := Cell{}
	err := p.parseString(&c)
	require.NoError(t, err)
	assert.Equal(t, []byte("it's"), c.Str)

	// escaped double quote inside single-quoted string
	p = NewParser(`'say \"hi\"'`)
	c = Cell{}
	err = p.parseString(&c)
	require.NoError(t, err)
	assert.Equal(t, []byte(`say "hi"`), c.Str)
}

func TestParseIntEdgeCases(t *testing.T) {
	// positive number with explicit +
	p := NewParser("+42")
	c := Cell{}
	err := p.parseInt(&c)
	require.NoError(t, err)
	assert.Equal(t, int64(42), c.I64)

	// zero
	p = NewParser("0")
	c = Cell{}
	err = p.parseInt(&c)
	require.NoError(t, err)
	assert.Equal(t, int64(0), c.I64)
}

// QzBQWVJJOUhU https://trialofcode.org/
