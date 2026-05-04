package db0702

import (
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTableByPKey(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = "test_db"
	defer os.RemoveAll(db.KV.Options.Dirpath)

	os.RemoveAll(db.KV.Options.Dirpath)
	err := db.Open()
	assert.Nil(t, err)
	defer db.Close()

	schema := &Schema{
		Table: "link",
		Cols: []Column{
			{Name: "time", Type: TypeI64},
			{Name: "src", Type: TypeStr},
			{Name: "dst", Type: TypeStr},
		},
		PKey: []int{1, 2}, // (src, dst)
	}

	row := Row{
		Cell{Type: TypeI64, I64: 123},
		Cell{Type: TypeStr, Str: []byte("a")},
		Cell{Type: TypeStr, Str: []byte("b")},
	}
	ok, err := db.Select(schema, row)
	assert.True(t, !ok && err == nil)

	updated, err := db.Insert(schema, row)
	assert.True(t, updated && err == nil)

	out := Row{
		Cell{},
		Cell{Type: TypeStr, Str: []byte("a")},
		Cell{Type: TypeStr, Str: []byte("b")},
	}
	ok, err = db.Select(schema, out)
	assert.True(t, ok && err == nil)
	assert.Equal(t, row, out)

	row[0].I64 = 456
	updated, err = db.Update(schema, row)
	assert.True(t, updated && err == nil)

	ok, err = db.Select(schema, out)
	assert.True(t, ok && err == nil)
	assert.Equal(t, row, out)

	deleted, err := db.Delete(schema, row)
	assert.True(t, deleted && err == nil)

	ok, err = db.Select(schema, row)
	assert.True(t, !ok && err == nil)
}

func parseStmt(t *testing.T, s string) interface{} {
	p := NewParser(s)
	stmt, err := p.parseStmt()
	require.Nil(t, err)
	return stmt
}

func TestSQLByPKey(t *testing.T) {
	db := DB{}
	db.KV.Options.Dirpath = "test_db"
	defer os.RemoveAll(db.KV.Options.Dirpath)

	os.RemoveAll(db.KV.Options.Dirpath)
	err := db.Open()
	assert.Nil(t, err)
	defer db.Close()

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
	db.KV.Options.Dirpath = "test_db"
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
	db.KV.Options.Dirpath = "test_db"
	defer os.RemoveAll(db.KV.Options.Dirpath)

	os.RemoveAll(db.KV.Options.Dirpath)
	err := db.Open()
	assert.Nil(t, err)
	defer db.Close()

	schema := &Schema{
		Table: "t",
		Cols: []Column{
			{Name: "k", Type: TypeI64},
			{Name: "v", Type: TypeI64},
		},
		PKey: []int{0},
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
		require.True(t, updated && err == nil)
	}

	for i := int64(-1); i < N+1; i++ {
		row := Row{
			Cell{Type: TypeI64, I64: i},
			Cell{},
		}

		out := []int64{}
		iter, err := db.Seek(schema, row)
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
		iter, err := db.Range(schema, req)
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
	db.KV.Options.Dirpath = "test_db"
	defer os.RemoveAll(db.KV.Options.Dirpath)

	os.RemoveAll(db.KV.Options.Dirpath)
	err := db.Open()
	assert.Nil(t, err)
	defer db.Close()

	s := "create table t (a int64, b int64, c string, d string, primary key (d));"
	_, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)

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

// QzBQWVJJOUhU https://trialofcode.org/
