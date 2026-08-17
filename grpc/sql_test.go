package grpcapi

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/teddymalhan/pallasdb/db"
	pbv1 "github.com/teddymalhan/pallasdb/pb/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// fakeCursor is a hand-rolled SQLCursor. The wire tests drive this rather than
// a real db result so they assert the streaming contract itself, independent of
// how db happens to execute a statement.
type fakeCursor struct {
	columns []SQLColumn
	rows    []db.Row
	updated uint64
	pos     int
	err     error
	closed  bool
}

func (c *fakeCursor) Columns() []SQLColumn { return c.columns }

func (c *fakeCursor) Next() bool {
	if c.err != nil || c.pos >= len(c.rows) {
		return false
	}
	c.pos++
	return true
}

func (c *fakeCursor) Row() db.Row {
	if c.pos <= 0 || c.pos > len(c.rows) {
		return nil
	}
	return c.rows[c.pos-1]
}

func (c *fakeCursor) RowsAffected() uint64 { return c.updated }
func (c *fakeCursor) Err() error           { return c.err }
func (c *fakeCursor) Close() error         { c.closed = true; return nil }

// staticExecutor answers every statement with a canned cursor, exercising the
// real Cell to Value conversion and the real header/row/trailer sequencing.
type staticExecutor struct {
	cursor *fakeCursor
	err    error
}

func (e staticExecutor) Query(context.Context, string) (SQLCursor, error) {
	if e.err != nil {
		return nil, e.err
	}
	if e.cursor == nil {
		return &fakeCursor{}, nil
	}
	return e.cursor, nil
}

func newSQLClient(t *testing.T, exec SQLExecutor) pbv1.SQLServiceClient {
	t.Helper()
	_, dial, _ := newTestServer(t, WithSQL(exec))
	return pbv1.NewSQLServiceClient(dial(grpc.WithTransportCredentials(insecure.NewCredentials())))
}

func collectQuery(ctx context.Context, t *testing.T, client pbv1.SQLServiceClient, statement string) []*pbv1.QueryResponse {
	t.Helper()
	stream, err := client.Query(ctx, &pbv1.QueryRequest{Statement: statement})
	require.NoError(t, err)
	var out []*pbv1.QueryResponse
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return out
		}
		require.NoError(t, err)
		out = append(out, msg)
	}
}

func TestQueryStreamsHeaderThenRows(t *testing.T) {
	client := newSQLClient(t, staticExecutor{cursor: &fakeCursor{
		columns: []SQLColumn{{Name: "id", Type: db.TypeI64}, {Name: "name", Type: db.TypeStr}},
		rows: []db.Row{
			{{Type: db.TypeI64, I64: 1}, {Type: db.TypeStr, Str: []byte("ada")}},
			{{Type: db.TypeI64, I64: 2}, {Type: db.TypeStr, Str: []byte("grace")}},
		},
	}})

	msgs := collectQuery(testContext(t), t, client, "SELECT id, name FROM people;")
	require.Len(t, msgs, 3)

	header := msgs[0]
	require.Empty(t, header.GetValues(), "the header message carries no values")
	require.Equal(t, uint64(0), header.GetRowsAffected())
	require.Len(t, header.GetColumns(), 2)
	require.Equal(t, "id", header.GetColumns()[0].GetName())
	require.Equal(t, pbv1.ValueType_VALUE_TYPE_INT64, header.GetColumns()[0].GetType())
	require.Equal(t, "name", header.GetColumns()[1].GetName())
	require.Equal(t, pbv1.ValueType_VALUE_TYPE_STRING, header.GetColumns()[1].GetType())

	for _, row := range msgs[1:] {
		require.Empty(t, row.GetColumns(), "row messages carry no schema")
		require.Len(t, row.GetValues(), 2)
	}
	require.Equal(t, int64(1), msgs[1].GetValues()[0].GetInt64Value())
	require.Equal(t, "ada", msgs[1].GetValues()[1].GetStringValue())
	require.Equal(t, int64(2), msgs[2].GetValues()[0].GetInt64Value())
	require.Equal(t, "grace", msgs[2].GetValues()[1].GetStringValue())
}

func TestQueryReportsRowsAffectedForNonSelect(t *testing.T) {
	client := newSQLClient(t, staticExecutor{cursor: &fakeCursor{updated: 3}})

	msgs := collectQuery(testContext(t), t, client, "DELETE FROM people WHERE id = 1;")
	require.Len(t, msgs, 2)
	require.Empty(t, msgs[0].GetColumns())
	require.Empty(t, msgs[0].GetValues())
	require.Equal(t, uint64(3), msgs[1].GetRowsAffected())
}

// An empty SELECT still announces its schema, and the column type is exact:
// the cursor reports it from the plan rather than inferring it from a first row
// that does not exist.
func TestQueryEmptySelectStillSendsHeader(t *testing.T) {
	client := newSQLClient(t, staticExecutor{cursor: &fakeCursor{
		columns: []SQLColumn{{Name: "id", Type: db.TypeI64}},
	}})

	msgs := collectQuery(testContext(t), t, client, "SELECT id FROM people;")
	require.Len(t, msgs, 1)
	require.Len(t, msgs[0].GetColumns(), 1)
	require.Equal(t, "id", msgs[0].GetColumns()[0].GetName())
	require.Equal(t, pbv1.ValueType_VALUE_TYPE_INT64, msgs[0].GetColumns()[0].GetType())
}

func TestQueryRejectsBadStatements(t *testing.T) {
	ctx := testContext(t)

	empty := newSQLClient(t, staticExecutor{})
	stream, err := empty.Query(ctx, &pbv1.QueryRequest{Statement: "  "})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	failing := newSQLClient(t, staticExecutor{err: errors.New("unknown statement")})
	stream, err = failing.Query(ctx, &pbv1.QueryRequest{Statement: "NOPE;"})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.ErrorContains(t, err, "unknown statement")
}

// The db-backed executor runs against a real database: it must surface a parse
// failure as an error rather than an empty result, and refuse to run at all
// when it has no database.
func TestDBExecutorAgainstRealDatabase(t *testing.T) {
	store, err := db.NewDB(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	exec := NewDBExecutor(store)

	_, err = exec.Query(context.Background(), "NOPE;")
	require.Error(t, err, "a statement that does not parse must not yield a cursor")

	_, err = NewDBExecutor(nil).Query(context.Background(), "SELECT 1;")
	require.ErrorContains(t, err, "no database configured")

	for _, stmt := range []string{
		"CREATE TABLE people (id int64, name string, primary key (id));",
		"INSERT INTO people VALUES (1, 'ada');",
	} {
		result, err := store.Query(stmt)
		require.NoError(t, err)
		require.NoError(t, result.Close())
	}
	cursor, err := exec.Query(context.Background(), "SELECT id, name FROM people;")
	require.NoError(t, err)
	t.Cleanup(func() { _ = cursor.Close() })
	require.Equal(t, []SQLColumn{{Name: "id", Type: db.TypeI64}, {Name: "name", Type: db.TypeStr}}, cursor.Columns())
	require.True(t, cursor.Next())
	require.Equal(t, int64(1), cursor.Row()[0].I64)
	require.False(t, cursor.Next())
	require.NoError(t, cursor.Err())
}

func TestCellConversion(t *testing.T) {
	values, err := rowValues(db.Row{
		{Type: db.TypeI64, I64: -7},
		{Type: db.TypeStr, Str: []byte("hello")},
	})
	require.NoError(t, err)
	require.Equal(t, int64(-7), values[0].GetInt64Value())
	require.Equal(t, "hello", values[1].GetStringValue())

	_, err = cellValue(db.Cell{Type: 0})
	require.ErrorContains(t, err, "unsupported cell type")
}
