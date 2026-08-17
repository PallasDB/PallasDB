package db

import (
	"strings"
	"testing"
)

// seedStmts are the statements every fuzz target starts from: the shapes that
// used to panic, overflow the stack or fail to parse.
var seedStmts = []string{
	"select a from t;",
	"select * from t;",
	"select *, a from t where a = 1;",
	"select a from t limit 3 offset 1;",
	"select a from t where (a, b) >= (1, 2);",
	"select a from t where b != 0 or not a < 2;",
	"select a, a * 4 - b, c + c from t where c = 'x';",
	"insert into t values (1, 2, 'x');",
	"update t set a=1,b=2 where a=1;",
	"update t set b = b + 1;",
	"delete from t where a - b = 0;",
	"create table u (a int64, b string, primary key (a), index (b));",
	"drop table u;",
	"select a from t where " + strings.Repeat("(", 2000) + "1" + strings.Repeat(")", 2000) + ";",
	"select a from t where " + strings.Repeat("1+", 2000) + "1 = 1;",
	"select a from t where " + strings.Repeat("a = 1 and ", 200) + "a = 1;",
	"select a from t where not " + strings.Repeat("not ", 2000) + "a;",
}

// FuzzParseStmt drives the parser and the header renderer. Neither may panic,
// whatever the input is.
func FuzzParseStmt(f *testing.F) {
	for _, s := range seedStmts {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		stmt, err := ParseStmt(s)
		if err != nil {
			return
		}
		switch ptr := stmt.(type) {
		case *StmtSelect:
			_ = exprs2header(ptr.cols)
		case *StmtUpdate:
			for _, assign := range ptr.value {
				_ = expr2str(assign.expr)
			}
		}
	})
}

// FuzzExecStmt runs whatever parses against a real table. The engine is
// embedded under a Raft FSM, so a malformed statement must come back as an
// error and never take the process down.
func FuzzExecStmt(f *testing.F) {
	db := &DB{}
	db.KV.Options.Dirpath = f.TempDir()
	if err := db.Open(); err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() {
		if err := db.Close(); err != nil {
			f.Error(err)
		}
	})

	setup := []string{
		"create table t (a int64, b int64, c string, primary key (a), index (b));",
		"insert into t values (1, 10, 'x');",
		"insert into t values (2, 20, 'y');",
		"insert into t values (3, 20, 'z');",
	}
	for _, s := range setup {
		stmt, err := ParseStmt(s)
		if err != nil {
			f.Fatal(err)
		}
		r, err := db.ExecStmt(stmt)
		if err != nil {
			f.Fatal(err)
		}
		if err := r.Close(); err != nil {
			f.Fatal(err)
		}
	}

	for _, s := range seedStmts {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		stmt, err := ParseStmt(s)
		if err != nil {
			return
		}
		r, err := db.ExecStmt(stmt)
		if err != nil {
			return
		}
		defer func() {
			if err := r.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
		}()
		_ = r.Columns()
		_ = r.RowsAffected()
		for r.Next() {
			_ = r.Row()
		}
		_ = r.Err()
	})
}
