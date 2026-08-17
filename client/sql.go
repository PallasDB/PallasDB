package client

import (
	"context"
	"errors"
	"io"
	"strconv"

	pbv1 "github.com/teddymalhan/pallasdb/pb/v1"
)

// ValueType is the SQL type of a column or cell.
type ValueType int

const (
	// ValueTypeUnknown is a cell the server did not type, i.e. SQL NULL.
	ValueTypeUnknown ValueType = iota
	// ValueTypeInt64 is a 64-bit signed integer.
	ValueTypeInt64
	// ValueTypeString is a UTF-8 string.
	ValueTypeString
)

// String implements fmt.Stringer.
func (t ValueType) String() string {
	switch t {
	case ValueTypeInt64:
		return "int64"
	case ValueTypeString:
		return "string"
	default:
		return "unknown"
	}
}

func valueTypeFromProto(t pbv1.ValueType) ValueType {
	switch t {
	case pbv1.ValueType_VALUE_TYPE_INT64:
		return ValueTypeInt64
	case pbv1.ValueType_VALUE_TYPE_STRING:
		return ValueTypeString
	default:
		return ValueTypeUnknown
	}
}

// Column describes one projected column of a query result.
type Column struct {
	Name string
	Type ValueType
}

// Value is a single result cell. Type selects which of Int and Str carries the
// payload; ValueTypeUnknown means the cell is NULL.
type Value struct {
	Type ValueType
	Int  int64
	Str  string
}

// String renders the cell for display. NULL cells render as "NULL".
func (v Value) String() string {
	switch v.Type {
	case ValueTypeInt64:
		return strconv.FormatInt(v.Int, 10)
	case ValueTypeString:
		return v.Str
	default:
		return "NULL"
	}
}

// Any returns the cell as a native Go value suitable for JSON encoding:
// int64, string, or nil.
func (v Value) Any() any {
	switch v.Type {
	case ValueTypeInt64:
		return v.Int
	case ValueTypeString:
		return v.Str
	default:
		return nil
	}
}

func valueFromProto(v *pbv1.Value) Value {
	switch payload := v.GetValue().(type) {
	case *pbv1.Value_Int64Value:
		return Value{Type: ValueTypeInt64, Int: payload.Int64Value}
	case *pbv1.Value_StringValue:
		return Value{Type: ValueTypeString, Str: payload.StringValue}
	default:
		return Value{Type: ValueTypeUnknown}
	}
}

// QueryHandler receives a streamed query result. Either callback may be nil.
// Returning an error from a callback abandons the stream and surfaces that
// error from [Client.QueryStream]; returning io.EOF stops it cleanly.
type QueryHandler struct {
	// OnColumns is called exactly once, before any row, with the projected
	// columns. Non-SELECT statements report zero columns.
	OnColumns func([]Column) error
	// OnRow is called once per result row.
	OnRow func([]Value) error
}

// Result is a buffered query result: the projection, every row, and the rows
// affected by a non-SELECT statement.
type Result struct {
	Columns      []Column
	Rows         [][]Value
	RowsAffected uint64
}

// Query executes a single SQL statement and buffers the whole result. Use
// [Client.QueryStream] when the result may not fit comfortably in memory.
func (c *Client) Query(ctx context.Context, statement string, consistency Consistency) (*Result, error) {
	result := &Result{}
	affected, err := c.QueryStream(ctx, statement, consistency, QueryHandler{
		OnColumns: func(cols []Column) error {
			result.Columns = cols
			return nil
		},
		OnRow: func(row []Value) error {
			result.Rows = append(result.Rows, row)
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	result.RowsAffected = affected
	return result, nil
}

// QueryStream executes a single SQL statement and dispatches the streamed
// result to handler, returning the rows affected reported by a non-SELECT
// statement.
//
// The wire contract is one header message carrying the columns, then one
// message per row carrying values, then — for non-SELECT statements — a final
// message carrying rows_affected. A leader redirect is attempted only when the
// stream fails before the header is delivered.
func (c *Client) QueryStream(
	ctx context.Context,
	statement string,
	consistency Consistency,
	handler QueryHandler,
) (uint64, error) {
	if statement == "" {
		return 0, errors.New("pallasdb: statement is required")
	}

	var rowsAffected uint64
	err := c.call(ctx, func(ctx context.Context, cn *conn) error {
		streamCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		stream, err := cn.sql.Query(streamCtx, &pbv1.QueryRequest{
			Statement:   statement,
			Consistency: consistency.proto(),
		})
		if err != nil {
			return err
		}

		rowsAffected = 0
		headerSeen := false
		for {
			msg, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return guard(err, headerSeen)
			}

			if !headerSeen {
				headerSeen = true
				if handler.OnColumns != nil {
					if cbErr := handler.OnColumns(columnsFromProto(msg.GetColumns())); cbErr != nil {
						return stopStream(cbErr)
					}
				}
				continue
			}

			if values := msg.GetValues(); len(values) > 0 {
				if handler.OnRow != nil {
					if cbErr := handler.OnRow(rowFromProto(values)); cbErr != nil {
						return stopStream(cbErr)
					}
				}
				continue
			}
			rowsAffected = msg.GetRowsAffected()
		}
	})
	if err != nil {
		return 0, err
	}
	return rowsAffected, nil
}

// stopStream translates a handler error into a terminal, non-retryable error,
// treating io.EOF as "stop here, no error".
func stopStream(err error) error {
	if errors.Is(err, io.EOF) {
		return nil
	}
	return noRetry{err}
}

func columnsFromProto(in []*pbv1.ColumnDescriptor) []Column {
	if len(in) == 0 {
		return nil
	}
	out := make([]Column, len(in))
	for i, col := range in {
		out[i] = Column{Name: col.GetName(), Type: valueTypeFromProto(col.GetType())}
	}
	return out
}

func rowFromProto(in []*pbv1.Value) []Value {
	out := make([]Value, len(in))
	for i, v := range in {
		out[i] = valueFromProto(v)
	}
	return out
}
