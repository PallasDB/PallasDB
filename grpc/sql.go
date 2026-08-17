package grpcapi

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/teddymalhan/pallasdb/db"
	pbv1 "github.com/teddymalhan/pallasdb/pb/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SQLColumn describes one projected column. It is expressed in db terms so the
// executor abstraction stays free of protobuf; the mapping to pbv1 lives in
// this file, and the db package never imports proto.
type SQLColumn struct {
	Name string
	Type db.CellType
}

// SQLCursor streams the result of a single statement. Close is mandatory and
// idempotent: for a SELECT the cursor holds a read transaction, and leaving it
// open pins the KV snapshot. Close reports only the failure to release that
// transaction; why the query itself stopped is always on Err.
type SQLCursor interface {
	// Columns describes the projected columns and is valid before the first
	// Next, including for a SELECT that matches nothing. It is empty for a
	// statement that projects no rows, which is the discriminator this package
	// uses to decide whether a rows_affected trailer belongs on the wire.
	Columns() []SQLColumn
	// Next advances to the next row, returning false at end of results or on
	// error.
	Next() bool
	// Row returns the current row. Its memory is reused, so it is valid only
	// until the next Next call: encode it before advancing, or clone it.
	Row() db.Row
	// RowsAffected reports the mutation count of a non-SELECT statement and is
	// zero for a SELECT.
	RowsAffected() uint64
	// Err reports why Next returned false, if it was not simply the end.
	Err() error
	Close() error
}

// SQLExecutor parses and executes one statement, returning a streaming cursor.
type SQLExecutor interface {
	Query(ctx context.Context, statement string) (SQLCursor, error)
}

// SQLServer serves SQLService over an SQLExecutor.
type SQLServer struct {
	exec SQLExecutor
	pbv1.UnimplementedSQLServiceServer
}

func NewSQLServer(exec SQLExecutor) *SQLServer { return &SQLServer{exec: exec} }

// Query executes one statement and streams its result.
//
// The wire contract is fixed: exactly one header message carrying the columns
// and no values, then one message per row carrying only values, then for a
// non-SELECT statement a final message carrying rows_affected. A client can
// therefore treat the first message as the schema unconditionally.
//
// QueryRequest.consistency is accepted and ignored here: the single-node server
// reads the local store, which is always linearizable with respect to its own
// writes.
func (s *SQLServer) Query(req *pbv1.QueryRequest, stream grpc.ServerStreamingServer[pbv1.QueryResponse]) error {
	return ignoreClosedStream(s.query(req, stream))
}

func (s *SQLServer) query(req *pbv1.QueryRequest, stream grpc.ServerStreamingServer[pbv1.QueryResponse]) error {
	if strings.TrimSpace(req.GetStatement()) == "" {
		return status.Error(codes.InvalidArgument, "statement is required")
	}
	ctx := stream.Context()
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}

	cursor, err := s.exec.Query(ctx, req.GetStatement())
	if err != nil {
		// Parsing, name resolution and type errors are all the caller's fault;
		// a statement that fails after it started producing rows is reported
		// through Err below as Internal.
		return status.Errorf(codes.InvalidArgument, "execute statement: %v", err)
	}
	defer func() { _ = cursor.Close() }()

	columns := cursor.Columns()
	if err := send(stream, &pbv1.QueryResponse{Columns: columnDescriptors(columns)}); err != nil {
		return err
	}

	for cursor.Next() {
		if err := ctx.Err(); err != nil {
			return status.FromContextError(err).Err()
		}
		values, err := rowValues(cursor.Row())
		if err != nil {
			return status.Errorf(codes.Internal, "encode row: %v", err)
		}
		if err := send(stream, &pbv1.QueryResponse{Values: values}); err != nil {
			return err
		}
	}
	if err := cursor.Err(); err != nil {
		return status.Errorf(codes.Internal, "execute statement: %v", err)
	}

	if len(columns) == 0 {
		if err := send(stream, &pbv1.QueryResponse{RowsAffected: cursor.RowsAffected()}); err != nil {
			return err
		}
	}
	if err := cursor.Close(); err != nil {
		return status.Errorf(codes.Internal, "close cursor: %v", err)
	}
	return nil
}

// send treats a closed client stream as a clean end rather than an error: the
// client got what it asked for and hung up.
func send[T any](stream grpc.ServerStreamingServer[T], msg *T) error {
	if err := stream.Send(msg); err != nil {
		if errors.Is(err, io.EOF) {
			return errStreamClosed
		}
		return err
	}
	return nil
}

// errStreamClosed unwinds a send loop after the client went away.
var errStreamClosed = errors.New("grpcapi: client closed the stream")

// ignoreClosedStream turns the sentinel back into a successful RPC: the client
// hanging up mid-stream is not a server error.
func ignoreClosedStream(err error) error {
	if errors.Is(err, errStreamClosed) {
		return nil
	}
	return err
}

func columnDescriptors(columns []SQLColumn) []*pbv1.ColumnDescriptor {
	if len(columns) == 0 {
		return nil
	}
	out := make([]*pbv1.ColumnDescriptor, len(columns))
	for i, column := range columns {
		out[i] = &pbv1.ColumnDescriptor{Name: column.Name, Type: valueType(column.Type)}
	}
	return out
}

func valueType(t db.CellType) pbv1.ValueType {
	switch t {
	case db.TypeI64:
		return pbv1.ValueType_VALUE_TYPE_INT64
	case db.TypeStr:
		return pbv1.ValueType_VALUE_TYPE_STRING
	default:
		return pbv1.ValueType_VALUE_TYPE_UNSPECIFIED
	}
}

func rowValues(row db.Row) ([]*pbv1.Value, error) {
	out := make([]*pbv1.Value, len(row))
	for i := range row {
		value, err := cellValue(row[i])
		if err != nil {
			return nil, err
		}
		out[i] = value
	}
	return out, nil
}

func cellValue(cell db.Cell) (*pbv1.Value, error) {
	switch cell.Type {
	case db.TypeI64:
		return &pbv1.Value{Value: &pbv1.Value_Int64Value{Int64Value: cell.I64}}, nil
	case db.TypeStr:
		return &pbv1.Value{Value: &pbv1.Value_StringValue{StringValue: string(cell.Str)}}, nil
	default:
		return nil, errors.New("unsupported cell type")
	}
}
