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

// staticExecutor answers every statement with a canned db.SQLResult, exercising
// the real cursor adapter and the real Cell to Value conversion.
type staticExecutor struct {
	result db.SQLResult
	err    error
}

func (e staticExecutor) Query(context.Context, string) (SQLCursor, error) {
	if e.err != nil {
		return nil, e.err
	}
	return newDBCursor(e.result), nil
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
	client := newSQLClient(t, staticExecutor{result: db.SQLResult{
		Header: []string{"id", "name"},
		Values: []db.Row{
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
	client := newSQLClient(t, staticExecutor{result: db.SQLResult{Updated: 3}})

	msgs := collectQuery(testContext(t), t, client, "DELETE FROM people WHERE id = 1;")
	require.Len(t, msgs, 2)
	require.Empty(t, msgs[0].GetColumns())
	require.Empty(t, msgs[0].GetValues())
	require.Equal(t, uint64(3), msgs[1].GetRowsAffected())
}

func TestQueryEmptySelectStillSendsHeader(t *testing.T) {
	client := newSQLClient(t, staticExecutor{result: db.SQLResult{Header: []string{"id"}}})

	msgs := collectQuery(testContext(t), t, client, "SELECT id FROM people;")
	require.Len(t, msgs, 1)
	require.Len(t, msgs[0].GetColumns(), 1)
	require.Equal(t, pbv1.ValueType_VALUE_TYPE_UNSPECIFIED, msgs[0].GetColumns()[0].GetType())
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

// The db-backed executor must refuse to run without the pieces it needs rather
// than pretending a statement succeeded.
func TestDBExecutorRequiresParser(t *testing.T) {
	store, err := db.NewDB(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	_, err = NewDBExecutor(store, nil).Query(context.Background(), "SELECT 1;")
	require.ErrorContains(t, err, "no statement parser configured")

	parseErr := errors.New("syntax error")
	_, err = NewDBExecutor(store, func(string) (any, error) { return nil, parseErr }).Query(context.Background(), "SELECT 1;")
	require.ErrorIs(t, err, parseErr)
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
