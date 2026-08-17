package db

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db := &DB{}
	db.KV.Options.Dirpath = t.TempDir()
	require.NoError(t, db.Open())
	t.Cleanup(func() { assert.NoError(t, db.Close()) })
	return db
}

// A tuple comparison may name more columns than the index has. The guard used
// to compare against the column count of the table, so a 3 column table with a
// 1 column primary key read past the index and panicked.
func TestIsPKeyPrefixShorterIndex(t *testing.T) {
	schema := &Schema{
		Version: SchemaVersion,
		Table:   "t",
		Cols:    []Column{{"a", TypeI64}, {"b", TypeI64}, {"c", TypeI64}},
		Indices: [][]int{{0}},
	}
	cols := []string{"a", "b"}
	cells := []Cell{{Type: TypeI64, I64: 1}, {Type: TypeI64, I64: 2}}
	assert.False(t, isPKeyPrefix(schema, 0, cols, cells))

	// an empty prefix and an unknown index are rejected too
	assert.False(t, isPKeyPrefix(schema, 0, nil, nil))
	assert.False(t, isPKeyPrefix(schema, 5, []string{"a"}, cells[:1]))
}

func TestSelectTupleLongerThanIndex(t *testing.T) {
	db := newTestDB(t)
	execSQL(t, db, "create table t (a int64, b int64, c int64, primary key (a));")
	execSQL(t, db, "insert into t values (1, 2, 3);")
	execSQL(t, db, "insert into t values (9, 9, 9);")

	// (a, b) names more columns than the one column primary key, so there is no
	// pushdown and the tuple falls through to the filter, which compares it
	// lexicographically. The original bug was a panic in the pushdown check.
	assert.Equal(t, []Row{makeRow(1), makeRow(9)},
		querySQL(t, db, "select a from t where (a, b) >= (1, 2);"))
	assert.Equal(t, []Row{makeRow(9)},
		querySQL(t, db, "select a from t where (a, b) > (1, 2);"))
}

func TestPlanScanUsesIndexRange(t *testing.T) {
	schema := &Schema{
		Version: SchemaVersion,
		Table:   "t",
		Cols:    []Column{{"a", TypeI64}, {"b", TypeI64}, {"c", TypeStr}},
		Indices: [][]int{{0}, {1, 0}},
	}
	parseCond := func(s string) any {
		p := NewParser(s)
		expr, err := p.parseExpr()
		require.NoError(t, err)
		require.True(t, p.isEnd())
		return expr
	}

	// a range on an indexed column is pushed into the key range
	plan := planScan(schema, parseCond("b > 2"))
	assert.True(t, plan.indexed)
	assert.Equal(t, 1, plan.req.IndexNo)
	assert.Nil(t, plan.residual)

	// both bounds of a range are pushed down
	plan = planScan(schema, parseCond("a >= 10 and a <= 20"))
	assert.True(t, plan.indexed)
	assert.Equal(t, 0, plan.req.IndexNo)
	assert.Equal(t, OP_GE, plan.req.StartCmp)
	assert.Equal(t, OP_LE, plan.req.StopCmp)
	assert.Nil(t, plan.residual)

	// equality on the whole primary key pins one row down
	plan = planScan(schema, parseCond("a = 7"))
	assert.True(t, plan.indexed)
	assert.Equal(t, 0, plan.req.IndexNo)
	assert.Equal(t, []Cell{{Type: TypeI64, I64: 7}}, plan.req.Start)
	assert.Nil(t, plan.residual)

	// the part the range cannot express stays as a residual filter
	plan = planScan(schema, parseCond("b > 2 and c = 'x'"))
	assert.True(t, plan.indexed)
	assert.Equal(t, 1, plan.req.IndexNo)
	assert.NotNil(t, plan.residual)

	// nothing indexable: full scan of the primary index plus a filter
	plan = planScan(schema, parseCond("c = 'x' or a = 1"))
	assert.False(t, plan.indexed)
	assert.Equal(t, 0, plan.req.IndexNo)
	assert.Nil(t, plan.req.Start)
	assert.NotNil(t, plan.residual)

	// no WHERE at all: full scan, no filter
	plan = planScan(schema, nil)
	assert.False(t, plan.indexed)
	assert.Nil(t, plan.residual)
}

// The executor answers conditions no key range can express by scanning and
// filtering, instead of failing with "unimplemented WHERE".
func TestFilterOperator(t *testing.T) {
	db := newTestDB(t)
	execSQL(t, db, "create table t (a int64, b int64, c string, primary key (a), index (b));")
	for i := 0; i < 6; i++ {
		execSQL(t, db, "insert into t values ("+
			strconv.Itoa(i)+", "+strconv.Itoa(i%3)+", '"+strings.Repeat("x", i)+"');")
	}
	// a: 0..5, b: a%3, c: "" "x" "xx" ...

	cases := []struct {
		name string
		sql  string
		want []Row
	}{{
		name: "non-key equality",
		sql:  "select a from t where c = 'xx';",
		want: []Row{makeRow(2)},
	}, {
		name: "or",
		sql:  "select a from t where a = 1 or a = 4;",
		want: []Row{makeRow(1), makeRow(4)},
	}, {
		name: "not",
		sql:  "select a from t where not a < 4;",
		want: []Row{makeRow(4), makeRow(5)},
	}, {
		name: "not equal",
		sql:  "select a from t where b != 0;",
		want: []Row{makeRow(1), makeRow(2), makeRow(4), makeRow(5)},
	}, {
		name: "arithmetic",
		sql:  "select a from t where a * 2 = 6;",
		want: []Row{makeRow(3)},
	}, {
		name: "arithmetic on two columns",
		sql:  "select a from t where a - b = 3;",
		want: []Row{makeRow(3), makeRow(4), makeRow(5)},
	}, {
		name: "no where",
		sql:  "select a from t;",
		want: []Row{makeRow(0), makeRow(1), makeRow(2), makeRow(3), makeRow(4), makeRow(5)},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, querySQL(t, db, tc.sql))
		})
	}

	// an indexed prefix plus a residual: rows come back in index order
	assert.Equal(t, []Row{makeRow(1), makeRow(4), makeRow(2), makeRow(5)},
		querySQL(t, db, "select a from t where b >= 1 and c != '';"))

	// UPDATE and DELETE take the same path
	assert.Equal(t, uint64(2), execSQL(t, db, "update t set c = 'hit' where a = 1 or a = 4;"))
	assert.Equal(t, []Row{makeRow(1), makeRow(4)}, querySQL(t, db, "select a from t where c = 'hit';"))
	assert.Equal(t, uint64(3), execSQL(t, db, "delete from t where a - b = 3;"))
	assert.Equal(t, []Row{makeRow(0), makeRow(1), makeRow(2)}, querySQL(t, db, "select a from t;"))

	// a WHERE that is not a boolean is an error, not a wrong answer
	r, err := db.ExecStmt(parseStmt(t, "select a from t where c;"))
	require.NoError(t, err)
	defer func() { assert.NoError(t, r.Close()) }()
	_, err = r.Rows()
	assert.Error(t, err)
}

// An indexed predicate must still narrow the scan instead of reading the whole
// table and filtering.
func TestIndexedPredicateDoesNotScanEverything(t *testing.T) {
	db := newTestDB(t)
	execSQL(t, db, "create table t (a int64, b int64, primary key (a), index (b));")
	for i := 0; i < 100; i++ {
		execSQL(t, db, "insert into t values ("+strconv.Itoa(i)+", "+strconv.Itoa(i)+");")
	}

	r, err := db.ExecStmt(parseStmt(t, "select a from t where b >= 97;"))
	require.NoError(t, err)
	rows, err := r.Rows()
	require.NoError(t, err)
	assert.Equal(t, []Row{makeRow(97), makeRow(98), makeRow(99)}, rows)
	assert.Equal(t, int64(3), r.cursor.scanned, "the index range must not scan the whole table")
	assert.NoError(t, r.Close())

	// the same answer through a full scan reads every row
	r, err = db.ExecStmt(parseStmt(t, "select a from t where b + 0 >= 97;"))
	require.NoError(t, err)
	rows, err = r.Rows()
	require.NoError(t, err)
	assert.Equal(t, []Row{makeRow(97), makeRow(98), makeRow(99)}, rows)
	assert.Equal(t, int64(100), r.cursor.scanned)
	assert.NoError(t, r.Close())
}

func TestSelectLimitOffset(t *testing.T) {
	db := newTestDB(t)
	execSQL(t, db, "create table t (a int64, primary key (a));")
	for i := 0; i < 10; i++ {
		execSQL(t, db, "insert into t values ("+strconv.Itoa(i)+");")
	}

	assert.Equal(t, []Row{makeRow(0), makeRow(1)}, querySQL(t, db, "select a from t limit 2;"))
	assert.Equal(t, []Row{makeRow(3), makeRow(4)}, querySQL(t, db, "select a from t limit 2 offset 3;"))
	assert.Empty(t, querySQL(t, db, "select a from t limit 0;"))
	assert.Empty(t, querySQL(t, db, "select a from t limit 5 offset 100;"))
	assert.Len(t, querySQL(t, db, "select a from t limit 100;"), 10)
	assert.Equal(t, []Row{makeRow(9)}, querySQL(t, db, "select a from t where a >= 5 limit 1 offset 4;"))
}

// A LIMIT must stop the scan, not filter a fully materialised result.
func TestSelectLimitStopsScanningEarly(t *testing.T) {
	db := newTestDB(t)
	execSQL(t, db, "create table t (a int64, b int64, primary key (a));")
	for i := 0; i < 200; i++ {
		execSQL(t, db, "insert into t values ("+strconv.Itoa(i)+", "+strconv.Itoa(i)+");")
	}

	r, err := db.ExecStmt(parseStmt(t, "select a from t limit 3;"))
	require.NoError(t, err)
	rows, err := r.Rows()
	require.NoError(t, err)
	assert.Equal(t, []Row{makeRow(0), makeRow(1), makeRow(2)}, rows)
	assert.Equal(t, int64(3), r.cursor.scanned)
	assert.NoError(t, r.Close())

	// the offset is skipped during the scan, so it also stops early
	r, err = db.ExecStmt(parseStmt(t, "select a from t limit 2 offset 5;"))
	require.NoError(t, err)
	rows, err = r.Rows()
	require.NoError(t, err)
	assert.Equal(t, []Row{makeRow(5), makeRow(6)}, rows)
	assert.Equal(t, int64(7), r.cursor.scanned)
	assert.NoError(t, r.Close())
}

// UPDATE and DELETE must not materialise the whole match set.
func TestMutationsStreamInBatches(t *testing.T) {
	db := newTestDB(t)
	execSQL(t, db, "create table t (a int64, b int64, primary key (a), index (b));")
	const n = mutateBatchSize + 37
	for i := 0; i < n; i++ {
		execSQL(t, db, "insert into t values ("+strconv.Itoa(i)+", "+strconv.Itoa(i)+");")
	}

	// more matches than one batch, over a secondary index whose column the
	// statement rewrites: every row must be updated exactly once
	assert.Equal(t, uint64(n), execSQL(t, db, "update t set b = b + 1 where b >= 0;"))
	assert.Equal(t, []Row{makeRow(5, 6)}, querySQL(t, db, "select a, b from t where a = 5;"))
	assert.Equal(t, []Row{makeRow(n-1, n)},
		querySQL(t, db, "select a, b from t where a = "+strconv.Itoa(n-1)+";"))

	assert.Equal(t, uint64(n), execSQL(t, db, "delete from t where b >= 1;"))
	assert.Empty(t, querySQL(t, db, "select a from t;"))
}

func TestSQLResultContract(t *testing.T) {
	db := newTestDB(t)
	execSQL(t, db, "create table t (a int64, b string, primary key (a));")
	execSQL(t, db, "insert into t values (1, 'x');")

	// a SELECT exposes its columns before the first row
	r, err := db.Query("select a, b + 'y', 3 from t;")
	require.NoError(t, err)
	assert.Equal(t, []ColumnDesc{
		{"a", TypeI64},
		{"(b + y)", TypeStr},
		{"3", TypeI64},
	}, r.Columns())
	assert.Equal(t, uint64(0), r.RowsAffected())
	require.True(t, r.Next())
	assert.Equal(t, makeRow(1, "xy", 3), r.Row())
	assert.False(t, r.Next())
	assert.NoError(t, r.Err())
	assert.NoError(t, r.Close())
	// Close is idempotent and a closed cursor yields nothing more
	assert.NoError(t, r.Close())
	assert.False(t, r.Next())

	// a non SELECT reports the affected rows and has no columns
	r, err = db.Query("update t set b = 'z' where a = 1;")
	require.NoError(t, err)
	assert.Nil(t, r.Columns())
	assert.Equal(t, uint64(1), r.RowsAffected())
	assert.False(t, r.Next())
	assert.NoError(t, r.Close())

	// a bad statement never reaches the executor
	_, err = db.Query("select a from t")
	assert.Error(t, err)
	_, err = db.Query("select a from nosuchtable;")
	assert.Error(t, err)
}

// A statement executed inside a transaction sees that transaction's writes.
func TestExecStmtInTransaction(t *testing.T) {
	db := newTestDB(t)
	execSQL(t, db, "create table t (a int64, primary key (a));")

	tx := db.NewTX()
	r, err := tx.ExecStmt(parseStmt(t, "insert into t values (1);"))
	require.NoError(t, err)
	require.NoError(t, r.Close())

	r, err = tx.Query("select a from t;")
	require.NoError(t, err)
	rows, err := r.Rows()
	require.NoError(t, err)
	assert.Equal(t, []Row{makeRow(1)}, rows)
	require.NoError(t, r.Close())

	tx.Abort()
	assert.Empty(t, querySQL(t, db, "select a from t;"))
}

// A tuple comparison the planner cannot turn into a key range still has to
// answer: it falls through to the filter and compares lexicographically.
func TestTupleComparisonFallsBackToFilter(t *testing.T) {
	db := newTestDB(t)
	execSQL(t, db, "create table t (a int64, b string, primary key (a));")
	execSQL(t, db, "insert into t values (1, 'x');")
	execSQL(t, db, "insert into t values (2, 'a');")
	execSQL(t, db, "insert into t values (3, 'm');")

	assert.Equal(t, []Row{makeRow(1)}, querySQL(t, db, "select a from t where (a, b) = (1, 'x');"))
	// Lexicographic: (2,'a') loses to (2,'b') on the second element, so only
	// the rows after it qualify.
	assert.Equal(t, []Row{makeRow(3)}, querySQL(t, db, "select a from t where (a, b) > (2, 'b');"))
	assert.Empty(t, querySQL(t, db, "select a from t where (a, b) = (1, 'zzz');"))

	// Arity and type mismatches are still errors, not silent answers.
	r, err := db.ExecStmt(parseStmt(t, "select a from t where (a, b) = (1);"))
	if err == nil {
		defer func() { assert.NoError(t, r.Close()) }()
		_, err = r.Rows()
	}
	assert.Error(t, err)
}

// Malformed input must surface as an error, not as a panic from an assertion,
// because this package runs inside a Raft FSM.
func TestBadInputDoesNotPanic(t *testing.T) {
	db := newTestDB(t)
	execSQL(t, db, "create table t (a int64, b string, primary key (a));")
	execSQL(t, db, "insert into t values (1, 'x');")

	bad := []string{
		"select zzz from t;",
		"select a / 0 from t;",
		"select a + b from t;",
		"select a from t where a / 0 = 1;",
		"update t set b = a where a = 1;",
		"update t set a = 2 where a = 1;",
		"insert into t values (1);",
	}
	for _, s := range bad {
		t.Run(s, func(t *testing.T) {
			r, err := db.ExecStmt(parseStmt(t, s))
			if err != nil {
				return
			}
			defer func() { assert.NoError(t, r.Close()) }()
			_, err = r.Rows()
			assert.Error(t, err)
		})
	}

	// an invalid range request is rejected instead of asserting
	schema, err := db.GetSchema("t")
	require.NoError(t, err)
	tx := db.NewTX()
	defer tx.Abort()
	_, err = tx.Range(&schema, &RangeReq{StartCmp: OP_EQ, StopCmp: OP_LE})
	assert.Error(t, err)
	_, err = tx.Range(&schema, &RangeReq{StartCmp: OP_GE, StopCmp: OP_GT})
	assert.Error(t, err)
	_, err = tx.Seek(&schema, Row{makeCell("wrong type"), makeCell("x")})
	assert.Error(t, err)
}
