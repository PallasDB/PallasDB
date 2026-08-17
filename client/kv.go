package client

import (
	"context"
	"errors"
	"io"

	pbv1 "github.com/teddymalhan/pallasdb/pb/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PutMode selects the write semantics of [Client.Put].
type PutMode int

const (
	// PutUpsert writes the key whether or not it already exists.
	PutUpsert PutMode = iota
	// PutInsert fails if the key already exists.
	PutInsert
	// PutUpdate fails if the key does not already exist.
	PutUpdate
)

func (m PutMode) proto() pbv1.PutMode {
	switch m {
	case PutInsert:
		return pbv1.PutMode_PUT_MODE_INSERT
	case PutUpdate:
		return pbv1.PutMode_PUT_MODE_UPDATE
	default:
		return pbv1.PutMode_PUT_MODE_UPSERT
	}
}

// String implements fmt.Stringer.
func (m PutMode) String() string {
	switch m {
	case PutInsert:
		return "insert"
	case PutUpdate:
		return "update"
	default:
		return "upsert"
	}
}

// ParsePutMode maps the CLI spelling of a write mode to a PutMode.
func ParsePutMode(s string) (PutMode, error) {
	switch s {
	case "upsert", "":
		return PutUpsert, nil
	case "insert":
		return PutInsert, nil
	case "update":
		return PutUpdate, nil
	default:
		return PutUpsert, errors.New("invalid write mode " + s)
	}
}

// Consistency selects the freshness guarantee of a read.
type Consistency int

const (
	// ConsistencyDefault lets the server choose; today that is linearizable.
	ConsistencyDefault Consistency = iota
	// ConsistencyLinearizable is served by the leader after it confirms
	// leadership, so it never returns stale data.
	ConsistencyLinearizable
	// ConsistencyStale is served from the local FSM of whichever node was
	// dialled and may lag the leader arbitrarily.
	ConsistencyStale
)

func (c Consistency) proto() pbv1.Consistency {
	switch c {
	case ConsistencyLinearizable:
		return pbv1.Consistency_CONSISTENCY_LINEARIZABLE
	case ConsistencyStale:
		return pbv1.Consistency_CONSISTENCY_STALE
	default:
		return pbv1.Consistency_CONSISTENCY_UNSPECIFIED
	}
}

// String implements fmt.Stringer.
func (c Consistency) String() string {
	switch c {
	case ConsistencyLinearizable:
		return "linearizable"
	case ConsistencyStale:
		return "stale"
	default:
		return "default"
	}
}

// ParseConsistency maps the CLI spelling of a consistency level.
func ParseConsistency(s string) (Consistency, error) {
	switch s {
	case "", "default":
		return ConsistencyDefault, nil
	case "linearizable", "strong":
		return ConsistencyLinearizable, nil
	case "stale":
		return ConsistencyStale, nil
	default:
		return ConsistencyDefault, errors.New("invalid consistency " + s)
	}
}

// KeyValue is one entry streamed by [Client.Range]. Value is nil when the
// request asked for keys only. Both slices are owned by the caller.
type KeyValue struct {
	Key   []byte
	Value []byte
}

// Get reads a single key. It returns [ErrNotFound] if the key is absent.
func (c *Client) Get(ctx context.Context, key []byte, consistency Consistency) ([]byte, error) {
	var value []byte
	err := c.call(ctx, func(ctx context.Context, cn *conn) error {
		resp, err := cn.kv.Get(ctx, &pbv1.GetRequest{
			Key:         key,
			Consistency: consistency.proto(),
		})
		if err != nil {
			return err
		}
		value = resp.GetValue()
		return nil
	})
	if err != nil {
		return nil, mapNotFound(err)
	}
	return value, nil
}

// Put writes a key and reports whether the stored value changed.
func (c *Client) Put(ctx context.Context, key, value []byte, mode PutMode) (bool, error) {
	var updated bool
	err := c.call(ctx, func(ctx context.Context, cn *conn) error {
		resp, err := cn.kv.Put(ctx, &pbv1.PutRequest{
			Key:   key,
			Value: value,
			Mode:  mode.proto(),
		})
		if err != nil {
			return err
		}
		updated = resp.GetUpdated()
		return nil
	})
	if err != nil {
		return false, err
	}
	return updated, nil
}

// Delete removes a key. It returns [ErrNotFound] if the key is absent.
func (c *Client) Delete(ctx context.Context, key []byte) (bool, error) {
	var deleted bool
	err := c.call(ctx, func(ctx context.Context, cn *conn) error {
		resp, err := cn.kv.Delete(ctx, &pbv1.DeleteRequest{Key: key})
		if err != nil {
			return err
		}
		deleted = resp.GetDeleted()
		return nil
	})
	if err != nil {
		return false, mapNotFound(err)
	}
	return deleted, nil
}

// RangeRequest describes a scan. A nil Start scans from the first key and a nil
// Stop scans through the last; Limit zero means "server default".
type RangeRequest struct {
	Start       []byte
	Stop        []byte
	Descending  bool
	Limit       uint64
	KeysOnly    bool
	Consistency Consistency
}

// Range streams the entries of a key range to fn, which is called once per
// entry in scan order. If fn returns an error the stream is abandoned and that
// error is returned unchanged; returning [io.EOF] stops the scan cleanly.
//
// A leader redirect is attempted only when the stream fails before its first
// entry, so rows are never delivered twice.
func (c *Client) Range(ctx context.Context, req RangeRequest, fn func(KeyValue) error) error {
	return c.call(ctx, func(ctx context.Context, cn *conn) error {
		streamCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		stream, err := cn.kv.Range(streamCtx, &pbv1.RangeRequest{
			Start:       req.Start,
			Stop:        req.Stop,
			Descending:  req.Descending,
			Limit:       req.Limit,
			KeysOnly:    req.KeysOnly,
			Consistency: req.Consistency.proto(),
		})
		if err != nil {
			return err
		}

		delivered := false
		for {
			msg, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return guard(err, delivered)
			}
			delivered = true
			if err := fn(KeyValue{Key: msg.GetKey(), Value: msg.GetValue()}); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return noRetry{err}
			}
		}
	})
}

// RangeSlice is Range buffered into a slice. Prefer [Client.Range] for scans
// whose size is not known to be small.
func (c *Client) RangeSlice(ctx context.Context, req RangeRequest) ([]KeyValue, error) {
	var out []KeyValue
	err := c.Range(ctx, req, func(kv KeyValue) error {
		out = append(out, kv)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// IsPartialScan reports whether err ended a [Client.Range] stream early
// because the server stopped scanning, not because the scan failed. A server
// bounds a scan by both a row count and a deadline and reports neither in the
// stream, so a scan that must be complete has to be resumed from the last key
// received until it stops producing new entries.
//
// A caller-side deadline or cancellation reaches the client as the same gRPC
// code, so check ctx.Err() before treating an error as resumable.
func IsPartialScan(err error) bool {
	return err != nil && status.Code(err) == codes.DeadlineExceeded
}

// guard suppresses redirection once a stream has handed data to the caller.
func guard(err error, delivered bool) error {
	if delivered {
		return noRetry{err}
	}
	return err
}

func mapNotFound(err error) error {
	if status.Code(err) == codes.NotFound {
		return ErrNotFound
	}
	return err
}
