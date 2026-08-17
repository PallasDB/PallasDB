package grpcapi

import (
	"context"
	"errors"

	"github.com/teddymalhan/pallasdb/db"
)

// StatementParser turns SQL text into a db statement value.
//
// It is injected rather than called directly because the db package's parser
// entry point is the one piece of the SQL surface the grpc package must not
// guess at: db owns statement syntax, this package owns the wire.
type StatementParser func(statement string) (any, error)

// DBExecutor runs statements against a *db.DB.
//
// This file is the only place that touches db's SQL execution API. When db's
// streaming cursor lands, dbCursor is the single type that changes; SQLServer
// and the wire contract are unaffected.
type DBExecutor struct {
	db    *db.DB
	parse StatementParser
}

// NewDBExecutor binds a database and the parser used to interpret statements.
func NewDBExecutor(database *db.DB, parse StatementParser) *DBExecutor {
	return &DBExecutor{db: database, parse: parse}
}

// Query parses and executes statement.
func (e *DBExecutor) Query(_ context.Context, statement string) (SQLCursor, error) {
	if e.db == nil {
		return nil, errors.New("no database configured")
	}
	if e.parse == nil {
		return nil, errors.New("no statement parser configured")
	}
	parsed, err := e.parse(statement)
	if err != nil {
		return nil, err
	}
	result, err := e.db.ExecStmt(parsed)
	if err != nil {
		return nil, err
	}
	return newDBCursor(result), nil
}

// dbCursor adapts db.SQLResult to SQLCursor.
type dbCursor struct {
	columns []SQLColumn
	rows    []db.Row
	updated uint64
	pos     int
}

func newDBCursor(result db.SQLResult) *dbCursor {
	cursor := &dbCursor{rows: result.Values, updated: uint64(max(result.Updated, 0)), pos: -1}
	if len(result.Header) > 0 {
		cursor.columns = make([]SQLColumn, len(result.Header))
		for i, name := range result.Header {
			cursor.columns[i] = SQLColumn{Name: name, Type: columnType(result.Values, i)}
		}
	}
	return cursor
}

// columnType reports the type of column i. db reports header names only, so the
// type is read off the first row; an empty result set leaves it unspecified.
func columnType(rows []db.Row, i int) db.CellType {
	if len(rows) == 0 || i >= len(rows[0]) {
		return 0
	}
	return rows[0][i].Type
}

func (c *dbCursor) Columns() []SQLColumn { return c.columns }

func (c *dbCursor) Next() bool {
	if c.pos+1 >= len(c.rows) {
		c.pos = len(c.rows)
		return false
	}
	c.pos++
	return true
}

func (c *dbCursor) Row() db.Row {
	if c.pos < 0 || c.pos >= len(c.rows) {
		return nil
	}
	return c.rows[c.pos]
}

func (c *dbCursor) RowsAffected() uint64 { return c.updated }

func (c *dbCursor) Err() error { return nil }

// Close releases the cursor. The current db API materialises results before
// returning, so there is no transaction left to release.
func (c *dbCursor) Close() error {
	c.rows, c.pos = nil, 0
	return nil
}
