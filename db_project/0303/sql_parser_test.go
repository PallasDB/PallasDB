package db0303

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
}

func testParseValue(t *testing.T, s string, ref Cell) {
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

func testParseSelect(t *testing.T, s string, ref StmtSelect) {
	p := NewParser(s)
	out := StmtSelect{}
	err := p.parseSelect(&out)
	assert.Nil(t, err)
	assert.True(t, p.isEnd())
	assert.Equal(t, ref, out)
}

func TestParseStmt(t *testing.T) {
	s := "select a from t where c=1;"
	stmt := StmtSelect{
		table: "t",
		cols:  []string{"a"},
		keys:  []NamedCell{{column: "c", value: Cell{Type: TypeI64, I64: 1}}},
	}
	testParseSelect(t, s, stmt)

	s = "select a,b_02 from T where c=1 and d='e';"
	stmt = StmtSelect{
		table: "T",
		cols:  []string{"a", "b_02"},
		keys: []NamedCell{
			{column: "c", value: Cell{Type: TypeI64, I64: 1}},
			{column: "d", value: Cell{Type: TypeStr, Str: []byte("e")}},
		},
	}
	testParseSelect(t, s, stmt)

	s = "select a, b_02 from T where c = 1 and d = 'e' ; "
	testParseSelect(t, s, stmt)
}

// QzBQWVJJOUhU https://trialofcode.org/
