package grpcapi

import (
	"context"
	"errors"

	"github.com/teddymalhan/pallasdb/db"
)

// DBExecutor runs statements against a *db.DB.
//
// This file is the only place that touches db's SQL execution API: db owns
// statement syntax and result iteration, this package owns the wire. db.Query
// parses and executes in one call and hands back a streaming cursor, so the
// adaptation here is a direct pass-through.
type DBExecutor struct {
	db *db.DB
}

// NewDBExecutor binds the database statements execute against.
func NewDBExecutor(database *db.DB) *DBExecutor {
	return &DBExecutor{db: database}
}

// Query parses and executes statement.
func (e *DBExecutor) Query(_ context.Context, statement string) (SQLCursor, error) {
	if e.db == nil {
		return nil, errors.New("no database configured")
	}
	result, err := e.db.Query(statement)
	if err != nil {
		return nil, err
	}
	return &dbCursor{result: result}, nil
}

// dbCursor adapts *db.SQLResult to SQLCursor. db's cursor already has the
// shape this package needs; only the column descriptor type differs.
type dbCursor struct {
	result  *db.SQLResult
	columns []SQLColumn
	mapped  bool
}

// Columns is memoised because SQLServer reads it once per statement while the
// db cursor rebuilds the slice on each call.
func (c *dbCursor) Columns() []SQLColumn {
	if c.mapped {
		return c.columns
	}
	c.mapped = true
	desc := c.result.Columns()
	if len(desc) == 0 {
		return nil
	}
	c.columns = make([]SQLColumn, len(desc))
	for i, d := range desc {
		c.columns[i] = SQLColumn{Name: d.Name, Type: d.Type}
	}
	return c.columns
}

func (c *dbCursor) Next() bool { return c.result.Next() }

func (c *dbCursor) Row() db.Row { return c.result.Row() }

func (c *dbCursor) RowsAffected() uint64 { return c.result.RowsAffected() }

func (c *dbCursor) Err() error { return c.result.Err() }

// Close releases the read transaction a SELECT holds open. It reports only that
// release failing; why the query stopped stays on Err.
func (c *dbCursor) Close() error { return c.result.Close() }
