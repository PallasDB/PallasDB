package db

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTableByPKey(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = t.TempDir()
	err := db.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	schema := &Schema{
		Table: "link",
		Cols: []Column{
			{Name: "time", Type: TypeI64},
			{Name: "src", Type: TypeStr},
			{Name: "dst", Type: TypeStr},
		},
		Indices: [][]int{{1, 2}}, // (src, dst)
	}

	row := Row{
		Cell{Type: TypeI64, I64: 123},
		Cell{Type: TypeStr, Str: []byte("a")},
		Cell{Type: TypeStr, Str: []byte("b")},
	}
	ok, err := db.Select(schema, row)
	require.NoError(t, err)
	assert.False(t, ok)

	updated, err := db.Insert(schema, row)
	require.NoError(t, err)
	assert.True(t, updated)

	out := Row{
		Cell{},
		Cell{Type: TypeStr, Str: []byte("a")},
		Cell{Type: TypeStr, Str: []byte("b")},
	}
	ok, err = db.Select(schema, out)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, row, out)

	row[0].I64 = 456
	updated, err = db.Update(schema, row)
	require.NoError(t, err)
	assert.True(t, updated)

	ok, err = db.Select(schema, out)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, row, out)

	deleted, err := db.Delete(schema, row)
	require.NoError(t, err)
	assert.True(t, deleted)

	ok, err = db.Select(schema, row)
	require.NoError(t, err)
	assert.False(t, ok)
}

func parseStmt(t *testing.T, s string) any {
	t.Helper()
	p := NewParser(s)
	stmt, err := p.parseStmt()
	require.Nil(t, err)
	return stmt
}

func TestSQLByPKey(t *testing.T) {
	db := DB{}
	dirpath := t.TempDir()
	db.KV.Options.Dirpath = dirpath
	err := db.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	s := "create table link (time int64, src string, dst string, primary key (src, dst));"
	_, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)

	s = "insert into link values (123, 'bob', 'alice');"
	r, err := db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, 1, r.Updated)

	s = "select time from link where dst = 'alice' and src = 'bob';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{{Cell{Type: TypeI64, I64: 123}}}, r.Values)

	s = "update link set time = 456 where dst = 'alice' and src = 'bob';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, 1, r.Updated)

	s = "select time from link where dst = 'alice' and src = 'bob';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{{Cell{Type: TypeI64, I64: 456}}}, r.Values)

	s = "insert into link values (123, 'cde', 'fgh');"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, 1, r.Updated)

	s = "select time from link where src >= 'b';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{{makeCell(456)}, {makeCell(123)}}, r.Values)

	s = "select time from link where 'b' <= src;"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{{makeCell(456)}, {makeCell(123)}}, r.Values)

	s = "select time from link where src <= 'z';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{{makeCell(123)}, {makeCell(456)}}, r.Values)

	s = "select time from link where 'cde' > src;"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{{makeCell(456)}}, r.Values)

	s = "select time from link where (src, dst) >= ('bob', 'alice');"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{{makeCell(456)}, {makeCell(123)}}, r.Values)

	s = "select time from link where (src, dst) >= ('bob', 'alicf');"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{{makeCell(123)}}, r.Values)

	// reopen
	err = db.Close()
	require.Nil(t, err)
	db = DB{}
	db.KV.Options.Dirpath = dirpath
	err = db.Open()
	require.Nil(t, err)

	s = "delete from link where src = 'bob' and dst = 'alice';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, 1, r.Updated)

	s = "select time from link where dst = 'alice' and src = 'bob';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, 0, len(r.Values))
}

func TestIterByPKey(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = t.TempDir()
	err := db.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	schema := &Schema{
		Table: "t",
		Cols: []Column{
			{Name: "k", Type: TypeI64},
			{Name: "v", Type: TypeI64},
		},
		Indices: [][]int{{0}},
	}

	N := int64(10)
	sorted := []int64{}
	for i := int64(0); i < N; i += 2 {
		sorted = append(sorted, i)
		row := Row{
			Cell{Type: TypeI64, I64: i},
			Cell{Type: TypeI64, I64: i},
		}
		updated, err := db.Insert(schema, row)
		require.NoError(t, err)
		require.True(t, updated)
	}

	tx := db.NewTX()
	for i := int64(-1); i < N+1; i++ {
		row := Row{
			Cell{Type: TypeI64, I64: i},
			Cell{},
		}

		out := []int64{}
		iter, err := tx.Seek(schema, row)
		for ; err == nil && iter.Valid(); err = iter.Next() {
			out = append(out, iter.Row()[1].I64)
		}
		require.Nil(t, err)

		expected := []int64{}
		for j := i; j < N; j++ {
			if j >= 0 && j%2 == 0 {
				expected = append(expected, j)
			}
		}
		assert.Equal(t, expected, out)
	}

	drainIter := func(req *RangeReq) (out []int64) {
		iter, err := tx.Range(schema, req)
		for ; err == nil && iter.Valid(); err = iter.Next() {
			out = append(out, iter.Row()[1].I64)
		}
		require.Nil(t, err)
		return
	}
	testReq := func(req *RangeReq, i int64, j int64, desc bool) {
		out := drainIter(req)
		expected := rangeQuery(sorted, i, j, desc)
		require.Equal(t, expected, out)
	}
	tx.Abort()

	for i := int64(-1); i < N+1; i++ {
		for j := int64(-1); j < N+1; j++ {
			req := &RangeReq{
				StartCmp: OP_GE,
				StopCmp:  OP_LE,
				Start:    []Cell{{Type: TypeI64, I64: i}},
				Stop:     []Cell{{Type: TypeI64, I64: j}},
			}
			testReq(req, i, j, false)

			req = &RangeReq{
				StartCmp: OP_LE,
				StopCmp:  OP_GE,
				Start:    []Cell{{Type: TypeI64, I64: i}},
				Stop:     []Cell{{Type: TypeI64, I64: j}},
			}
			testReq(req, i, j, true)

			req = &RangeReq{
				StartCmp: OP_GT,
				StopCmp:  OP_LT,
				Start:    []Cell{{Type: TypeI64, I64: i}},
				Stop:     []Cell{{Type: TypeI64, I64: j}},
			}
			testReq(req, i+1, j-1, false)

			req = &RangeReq{
				StartCmp: OP_LT,
				StopCmp:  OP_GT,
				Start:    []Cell{{Type: TypeI64, I64: i}},
				Stop:     []Cell{{Type: TypeI64, I64: j}},
			}
			testReq(req, i-1, j+1, true)
		}
	}

	for i := int64(-1); i < N+1; i++ {
		req := &RangeReq{
			StartCmp: OP_GE,
			StopCmp:  OP_LE,
			Start:    []Cell{{Type: TypeI64, I64: i}},
			Stop:     nil,
		}
		testReq(req, i, N, false)

		req = &RangeReq{
			StartCmp: OP_LE,
			StopCmp:  OP_GE,
			Start:    []Cell{{Type: TypeI64, I64: i}},
			Stop:     nil,
		}
		testReq(req, i, -1, true)
	}
}

func rangeQuery(sorted []int64, start int64, stop int64, desc bool) (out []int64) {
	for _, v := range sorted {
		if !desc && start <= v && v <= stop {
			out = append(out, v)
		} else if desc && stop <= v && v <= start {
			out = append(out, v)
		}
	}
	if desc {
		slices.Reverse(out)
	}
	return out
}

func TestTableExpr(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = t.TempDir()
	err := db.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	s := `
		create table t (
			a int64, b int64, c string, d string,
			index (b),
			index (a, d),
			primary key (d)
		);
	`
	_, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)

	schema, err := db.GetSchema("t")
	require.Nil(t, err)
	expected := Schema{
		Table: "t",
		Cols:  []Column{{"a", TypeI64}, {"b", TypeI64}, {"c", TypeStr}, {"d", TypeStr}},
		Indices: [][]int{
			{3},
			{1, 3},
			{0, 3},
		},
	}
	assert.Equal(t, expected, schema)

	s = "insert into t values (1, 2, 'a', 'b');"
	r, err := db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, 1, r.Updated)

	s = "select a * 4 - b, d + c from t where d = 'b';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{makeRow(2, "ba")}, r.Values)

	s = "update t set a = a - b, b = a, c = d + c where d = 'b';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, 1, r.Updated)

	s = "select a, b, c, d from t where d = 'b';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{makeRow(-1, 1, "ba", "b")}, r.Values)
}

func TestTableIndices(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = t.TempDir()
	err := db.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	s := `
		create table t (
			a int64, b int64,
			index (b),
			primary key (a)
		);
	`
	_, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)

	schema, err := db.GetSchema("t")
	require.Nil(t, err)
	expected := Schema{
		Table: "t",
		Cols:  []Column{{"a", TypeI64}, {"b", TypeI64}},
		Indices: [][]int{
			{0},
			{1, 0},
		},
	}
	assert.Equal(t, expected, schema)

	s = "insert into t values (1, 2);"
	r, err := db.ExecStmt(parseStmt(t, s))
	require.NoError(t, err)
	require.Equal(t, 1, r.Updated)
	s = "insert into t values (2, 2);"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.NoError(t, err)
	require.Equal(t, 1, r.Updated)
	s = "insert into t values (0, 3);"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.NoError(t, err)
	require.Equal(t, 1, r.Updated)
	s = "insert into t values (1, 2);"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.NoError(t, err)
	require.Equal(t, 0, r.Updated)
	// (1, 2), (2, 2), (0, 3)

	s = "select a, b from t where a >= 0;"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{makeRow(0, 3), makeRow(1, 2), makeRow(2, 2)}, r.Values)

	s = "select a, b from t where b > 2;"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{makeRow(0, 3)}, r.Values)

	s = "select a, b from t where b >= 2;"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{makeRow(1, 2), makeRow(2, 2), makeRow(0, 3)}, r.Values)

	s = "update t set b = b - a where b < 3;"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.NoError(t, err)
	require.Equal(t, 2, r.Updated)
	// (1, 1), (2, 0), (0, 3)

	s = "select a, b from t where a >= 0;"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{makeRow(0, 3), makeRow(1, 1), makeRow(2, 0)}, r.Values)

	s = "select a, b from t where b < 3;"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{makeRow(1, 1), makeRow(2, 0)}, r.Values)

	s = "delete from t where b >= 1;"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.NoError(t, err)
	require.Equal(t, 2, r.Updated)
	// (2, 0)

	s = "select a, b from t where a >= 0;"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{makeRow(2, 0)}, r.Values)

	s = "select a, b from t where b >= 0;"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{makeRow(2, 0)}, r.Values)
}

func TestExecCreateTableErrors(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = t.TempDir()
	err := db.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	s := "create table t (a int64, primary key (a));"
	_, err = db.ExecStmt(parseStmt(t, s))
	require.NoError(t, err)

	// Duplicate table name
	_, err = db.ExecStmt(parseStmt(t, s))
	assert.Error(t, err)
}

func TestExecCreateTableColumnNotFound(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = t.TempDir()
	err := db.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	// primary key column doesn't exist
	s := "create table t (a int64, primary key (z));"
	_, err = db.ExecStmt(parseStmt(t, s))
	assert.Error(t, err)

	// index column doesn't exist
	s = "create table t2 (a int64, primary key (a), index (z));"
	_, err = db.ExecStmt(parseStmt(t, s))
	assert.Error(t, err)
}

func TestGetSchemaNotFound(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = t.TempDir()
	err := db.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	_, err = db.GetSchema("nonexistent")
	assert.Error(t, err)
}

func TestExecInsertSchemaMismatch(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = t.TempDir()
	err := db.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	s := "create table t (a int64, b string, primary key (a));"
	_, err = db.ExecStmt(parseStmt(t, s))
	require.NoError(t, err)

	// Wrong number of values
	s = "insert into t values (1);"
	_, err = db.ExecStmt(parseStmt(t, s))
	assert.Error(t, err)

	// Wrong type (string where int64 expected)
	s = "insert into t values ('hello', 'world');"
	_, err = db.ExecStmt(parseStmt(t, s))
	assert.Error(t, err)
}

func TestExecInsertUnknownTable(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = t.TempDir()
	err := db.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	s := "insert into nonexistent values (1);"
	_, err = db.ExecStmt(parseStmt(t, s))
	assert.Error(t, err)
}

func TestExecSelectUnknownTable(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = t.TempDir()
	err := db.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	s := "select a from nonexistent where a = 1;"
	_, err = db.ExecStmt(parseStmt(t, s))
	assert.Error(t, err)
}

func TestExecUpdateUnknownTable(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = t.TempDir()
	err := db.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	s := "update nonexistent set a = 1 where a = 0;"
	_, err = db.ExecStmt(parseStmt(t, s))
	assert.Error(t, err)
}

func TestExecDeleteUnknownTable(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = t.TempDir()
	err := db.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	s := "delete from nonexistent where a = 1;"
	_, err = db.ExecStmt(parseStmt(t, s))
	assert.Error(t, err)
}

func TestFillNonPKeyErrors(t *testing.T) {
	schema := &Schema{
		Table:   "t",
		Cols:    []Column{{"k", TypeI64}, {"v", TypeI64}},
		Indices: [][]int{{0}},
	}
	row := schema.NewRow()
	row[0] = Cell{Type: TypeI64, I64: 1}
	row[1] = Cell{Type: TypeI64, I64: 2}

	// Cannot update primary key column
	err := fillNonPKey(schema, []NamedCell{{"k", Cell{Type: TypeI64, I64: 99}}}, row)
	assert.Error(t, err)

	// Unknown column name
	err = fillNonPKey(schema, []NamedCell{{"z", Cell{Type: TypeI64, I64: 99}}}, row)
	assert.Error(t, err)

	// Wrong type for column
	err = fillNonPKey(schema, []NamedCell{{"v", Cell{Type: TypeStr, Str: []byte("bad")}}}, row)
	assert.Error(t, err)
}

func TestAddPKeyToIndex(t *testing.T) {
	// pkey columns not in index should be appended
	pkey := []int{0, 1}
	index := []int{2, 3}
	result := addPKeyToIndex(index, pkey)
	assert.Equal(t, []int{2, 3, 0, 1}, result)

	// pkey columns already in index should not be duplicated
	index = []int{0, 2}
	result = addPKeyToIndex(index, pkey)
	assert.Equal(t, []int{0, 2, 1}, result)
}

func TestMatchAllEq(t *testing.T) {
	// simple equality
	cond := &ExprBinOp{op: OP_EQ, left: "k", right: &Cell{Type: TypeI64, I64: 5}}
	named, ok := matchAllEq(cond, nil)
	assert.True(t, ok)
	assert.Equal(t, []NamedCell{{"k", Cell{Type: TypeI64, I64: 5}}}, named)

	// AND of equalities
	cond2 := &ExprBinOp{op: OP_AND, left: cond, right: &ExprBinOp{
		op: OP_EQ, left: "v", right: &Cell{Type: TypeStr, Str: []byte("x")},
	}}
	named, ok = matchAllEq(cond2, nil)
	assert.True(t, ok)
	assert.Len(t, named, 2)

	// non-equality op - fails
	cond3 := &ExprBinOp{op: OP_LT, left: "k", right: &Cell{Type: TypeI64, I64: 5}}
	_, ok = matchAllEq(cond3, nil)
	assert.False(t, ok)

	// reversed (value on left, name on right)
	cond4 := &ExprBinOp{op: OP_EQ, left: &Cell{Type: TypeI64, I64: 5}, right: "k"}
	named, ok = matchAllEq(cond4, nil)
	assert.True(t, ok)
	assert.Equal(t, []NamedCell{{"k", Cell{Type: TypeI64, I64: 5}}}, named)
}

func TestAsNameListAndAsCellList(t *testing.T) {
	// string name
	names, ok := asNameList("col")
	assert.True(t, ok)
	assert.Equal(t, []string{"col"}, names)

	// tuple of names
	names, ok = asNameList(&ExprTuple{kids: []any{"a", "b"}})
	assert.True(t, ok)
	assert.Equal(t, []string{"a", "b"}, names)

	// tuple with non-string - fails
	_, ok = asNameList(&ExprTuple{kids: []any{"a", &Cell{Type: TypeI64, I64: 1}}})
	assert.False(t, ok)

	// non-string, non-tuple
	_, ok = asNameList(&Cell{Type: TypeI64, I64: 1})
	assert.False(t, ok)

	// single cell
	cells, ok := asCellList(&Cell{Type: TypeI64, I64: 5})
	assert.True(t, ok)
	assert.Equal(t, []Cell{{Type: TypeI64, I64: 5}}, cells)

	// tuple of cells
	cells, ok = asCellList(&ExprTuple{kids: []any{
		&Cell{Type: TypeI64, I64: 1},
		&Cell{Type: TypeStr, Str: []byte("x")},
	}})
	assert.True(t, ok)
	assert.Len(t, cells, 2)

	// tuple with non-cell - fails
	_, ok = asCellList(&ExprTuple{kids: []any{&Cell{Type: TypeI64, I64: 1}, "col"}})
	assert.False(t, ok)

	// non-cell, non-tuple
	_, ok = asCellList("col")
	assert.False(t, ok)
}

func TestMatchCmp(t *testing.T) {
	// simple cmp
	cell := &Cell{Type: TypeI64, I64: 5}
	cond := &ExprBinOp{op: OP_GE, left: "k", right: cell}
	op, names, cells, ok := matchCmp(cond)
	assert.True(t, ok)
	assert.Equal(t, OP_GE, op)
	assert.Equal(t, []string{"k"}, names)
	assert.Equal(t, []Cell{*cell}, cells)

	// reversed operands - op should be flipped
	cond = &ExprBinOp{op: OP_GE, left: cell, right: "k"}
	op, names, cells, ok = matchCmp(cond)
	assert.True(t, ok)
	assert.Equal(t, OP_LE, op) // GE flipped
	assert.Equal(t, []string{"k"}, names)
	assert.Equal(t, []Cell{*cell}, cells)

	// non-comparison op - fails
	cond = &ExprBinOp{op: OP_ADD, left: "k", right: cell}
	_, _, _, ok = matchCmp(cond)
	assert.False(t, ok)

	// not a binop - fails
	_, _, _, ok = matchCmp("col")
	assert.False(t, ok)
}

func TestUpsertAndUpdateModes(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = t.TempDir()
	err := db.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	schema := &Schema{
		Table: "t",
		Cols: []Column{
			{Name: "k", Type: TypeI64},
			{Name: "v", Type: TypeI64},
		},
		Indices: [][]int{{0}},
	}

	row := Row{
		Cell{Type: TypeI64, I64: 1},
		Cell{Type: TypeI64, I64: 10},
	}

	// Insert new row
	updated, err := db.Insert(schema, row)
	require.NoError(t, err)
	assert.True(t, updated)

	// Insert same row again - should NOT update (ModeInsert)
	updated, err = db.Insert(schema, row)
	require.NoError(t, err)
	assert.False(t, updated)

	// Upsert same row - no change, same value
	updated, err = db.Upsert(schema, row)
	require.NoError(t, err)
	assert.False(t, updated)

	// Upsert with different value - should update
	row[1] = Cell{Type: TypeI64, I64: 20}
	updated, err = db.Upsert(schema, row)
	require.NoError(t, err)
	assert.True(t, updated)

	// Update non-existing row - should not update
	row[0] = Cell{Type: TypeI64, I64: 999}
	updated, err = db.Update(schema, row)
	require.NoError(t, err)
	assert.False(t, updated)

	// Delete non-existing row
	deleted, err := db.Delete(schema, row)
	require.NoError(t, err)
	assert.False(t, deleted)
}

func TestMatchRangeByIndexUsesBothBounds(t *testing.T) {
	schema := &Schema{
		Table:   "t",
		Cols:    []Column{{Name: "k", Type: TypeI64}},
		Indices: [][]int{{0}},
	}
	cond := &ExprBinOp{
		op:    OP_AND,
		left:  &ExprBinOp{op: OP_GE, left: "k", right: &Cell{Type: TypeI64, I64: 10}},
		right: &ExprBinOp{op: OP_LE, left: "k", right: &Cell{Type: TypeI64, I64: 20}},
	}

	req, ok := matchRangeByIndex(schema, 0, cond)
	require.True(t, ok)
	assert.Equal(t, OP_GE, req.StartCmp)
	assert.Equal(t, OP_LE, req.StopCmp)
	assert.Equal(t, int64(10), req.Start[0].I64)
	assert.Equal(t, int64(20), req.Stop[0].I64)
}

func TestSQLUnimplementedWhere(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = t.TempDir()
	err := db.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	s := "create table t (a int64, b int64, primary key (a));"
	_, err = db.ExecStmt(parseStmt(t, s))
	require.NoError(t, err)

	// WHERE clause that doesn't match any index pattern (expression on both sides)
	s = "select a from t where a + 1 = b;"
	_, err = db.ExecStmt(parseStmt(t, s))
	assert.Error(t, err)
}

func TestDBNewTXNestedNewTX(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = t.TempDir()
	err := db.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, db.Close()) })

	tx := db.NewTX()
	inner := tx.NewTX()
	// inner tx operations should be isolated
	schema := &Schema{
		Table:   "t",
		Cols:    []Column{{"k", TypeI64}, {"v", TypeI64}},
		Indices: [][]int{{0}},
	}
	row := Row{
		Cell{Type: TypeI64, I64: 1},
		Cell{Type: TypeI64, I64: 100},
	}
	_, err = inner.Insert(schema, row)
	require.NoError(t, err)

	// Abort inner transaction - changes should not propagate
	inner.Abort()

	// Outer tx should not see inner's changes
	ok, err := tx.Select(schema, row)
	require.NoError(t, err)
	assert.False(t, ok)
	tx.Abort()
}

// QzBQWVJJOUhU https://trialofcode.org/
