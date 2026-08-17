package grpcapi

import (
	"bytes"
	"time"

	"github.com/teddymalhan/pallasdb/db"
	pbv1 "github.com/teddymalhan/pallasdb/pb/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// rangeCheckInterval is how many rows are streamed between context and clock
// checks. Checking every row costs more than the send does on a keys-only scan.
const rangeCheckInterval = 128

// rangeLimits are the server-side bounds applied to every scan.
type rangeLimits struct {
	limit       uint64
	maxDuration time.Duration
}

// rangeScan is a validated Range request bound to the server's limits.
type rangeScan struct {
	start       []byte
	stop        []byte
	descending  bool
	keysOnly    bool
	limit       uint64
	maxDuration time.Duration
}

// newRangeScan resolves a request against the server defaults.
//
// An empty start scans from the first key and an empty stop scans to the last,
// which is what makes prefix and whole-keyspace scans expressible. Both bounds
// are inclusive, matching the storage iterator.
func newRangeScan(req *pbv1.RangeRequest, limits rangeLimits) rangeScan {
	scan := rangeScan{
		start:       req.GetStart(),
		stop:        req.GetStop(),
		descending:  req.GetDescending(),
		keysOnly:    req.GetKeysOnly(),
		limit:       req.GetLimit(),
		maxDuration: limits.maxDuration,
	}
	// A client may ask for fewer rows than the server default but not more:
	// the cap is what keeps a stream from pinning the KV snapshot forever.
	if scan.limit == 0 || (limits.limit > 0 && scan.limit > limits.limit) {
		scan.limit = limits.limit
	}
	return scan
}

// inBounds reports whether key is still inside the requested range. An empty
// stop means unbounded in the scan direction.
func (r rangeScan) inBounds(key []byte) bool {
	if len(r.stop) == 0 {
		return true
	}
	if r.descending {
		return bytes.Compare(key, r.stop) >= 0
	}
	return bytes.Compare(key, r.stop) <= 0
}

// seekToLaster is implemented by a KV transaction that can position an iterator
// on the greatest key. It is probed at runtime so a descending whole-keyspace
// scan starts working as soon as the storage layer offers it.
type seekToLaster interface {
	SeekToLast() (db.SortedKVIter, error)
}

// open positions an iterator on the first key of the scan.
func (r rangeScan) open(tx *db.KVTX) (db.SortedKVIter, error) {
	if !r.descending {
		// Seek positions on the first key >= start; a nil start yields the
		// first key in the store.
		iter, err := tx.Seek(r.start)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "range keys: %v", err)
		}
		return iter, nil
	}

	if len(r.start) == 0 {
		seeker, ok := any(tx).(seekToLaster)
		if !ok {
			return nil, status.Error(codes.InvalidArgument, "descending range requires a start key")
		}
		iter, err := seeker.SeekToLast()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "range keys: %v", err)
		}
		return iter, nil
	}

	iter, err := tx.Seek(r.start)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "range keys: %v", err)
	}
	// Seek lands on the first key >= start; a descending scan must begin at the
	// last key <= start, which is one step back unless start itself exists.
	if !iter.Valid() || bytes.Compare(iter.Key(), r.start) > 0 {
		if err := iter.Prev(); err != nil {
			return nil, status.Errorf(codes.Internal, "range keys: %v", err)
		}
	}
	return iter, nil
}

func (r rangeScan) advance(iter db.SortedKVIter) error {
	if r.descending {
		return iter.Prev()
	}
	return iter.Next()
}

// stream sends the scan over the wire. The caller owns the transaction.
//
// limit and maxDuration bound the scan itself, but they are checked between
// sends: a client that stops reading blocks this loop inside send, on gRPC
// flow control, and holds the caller's read transaction — and therefore the KV
// snapshot — until it resumes or the stream is torn down. Nothing here can
// preempt that. On a cluster node the FSM tolerates it (see
// cluster.FSM.lockForSwap), but it still pins retired SSTables.
func (r rangeScan) stream(tx *db.KVTX, out grpc.ServerStreamingServer[pbv1.RangeResponse]) error {
	iter, err := r.open(tx)
	if err != nil {
		return err
	}

	ctx := out.Context()
	deadline := time.Now().Add(r.maxDuration)
	var sent uint64
	for iter.Valid() && r.inBounds(iter.Key()) {
		// Engine metadata is not part of the user keyspace. Skipping rather
		// than stopping keeps a whole-keyspace scan usable: the reserved keys
		// sort before every table key, so aborting here would return nothing.
		if db.IsReservedKey(iter.Key()) {
			if err := r.advance(iter); err != nil {
				return status.Errorf(codes.Internal, "iterate range: %v", err)
			}
			continue
		}
		resp := &pbv1.RangeResponse{Key: iter.Key()}
		if !r.keysOnly {
			resp.Value = iter.Val()
		}
		if err := send(out, resp); err != nil {
			return err
		}
		sent++
		// Truncation at the limit is silent by design: the client resumes by
		// re-issuing the scan with start set to the last key it received.
		if r.limit > 0 && sent >= r.limit {
			return nil
		}
		if sent%rangeCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return status.FromContextError(err).Err()
			}
			if r.maxDuration > 0 && time.Now().After(deadline) {
				return status.Error(codes.DeadlineExceeded, "range exceeded the server scan deadline; resume from the last key received")
			}
		}
		if err := r.advance(iter); err != nil {
			return status.Errorf(codes.Internal, "iterate range: %v", err)
		}
	}
	return nil
}
