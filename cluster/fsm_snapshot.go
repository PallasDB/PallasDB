package cluster

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/hashicorp/raft"
	"github.com/teddymalhan/pallasdb/db"
)

// FSMSnapshot implements raft.FSMSnapshot.
// It holds an open transaction providing a point-in-time consistent view.
type FSMSnapshot struct {
	iter    db.SortedKVIter
	cleanup func()
}

// Persist writes all live key-value pairs to the raft.SnapshotSink.
// Wire format:
//
//	[8B: record count uint64 LE]
//	For each record: [4B klen uint32 LE][4B vlen uint32 LE][key bytes][val bytes]
func (s *FSMSnapshot) Persist(sink raft.SnapshotSink) error {
	// Collect all pairs first so we know the count.
	type pair struct{ k, v []byte }
	var pairs []pair
	for s.iter.Valid() {
		pairs = append(pairs, pair{append([]byte(nil), s.iter.Key()...), append([]byte(nil), s.iter.Val()...)})
		if err := s.iter.Next(); err != nil {
			_ = sink.Cancel()
			return fmt.Errorf("snapshot iterate: %w", err)
		}
	}

	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(len(pairs)))
	if _, err := sink.Write(buf); err != nil {
		_ = sink.Cancel()
		return fmt.Errorf("snapshot write count: %w", err)
	}

	hdr := make([]byte, 8)
	for _, p := range pairs {
		binary.LittleEndian.PutUint32(hdr[0:4], uint32(len(p.k)))
		binary.LittleEndian.PutUint32(hdr[4:8], uint32(len(p.v)))
		if _, err := sink.Write(hdr); err != nil {
			_ = sink.Cancel()
			return fmt.Errorf("snapshot write header: %w", err)
		}
		if _, err := sink.Write(p.k); err != nil {
			_ = sink.Cancel()
			return fmt.Errorf("snapshot write key: %w", err)
		}
		if len(p.v) > 0 {
			if _, err := sink.Write(p.v); err != nil {
				_ = sink.Cancel()
				return fmt.Errorf("snapshot write val: %w", err)
			}
		}
	}
	return sink.Close()
}

// Release is called after Persist completes (success or failure).
func (s *FSMSnapshot) Release() {
	s.cleanup()
}

// readSnapshot parses the binary snapshot format from r into key-value pairs.
func readSnapshot(r io.Reader) ([]Command, error) {
	hdr := make([]byte, 8)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, fmt.Errorf("read count: %w", err)
	}
	count := binary.LittleEndian.Uint64(hdr)

	pairs := make([]Command, 0, count)
	rec := make([]byte, 8)
	for i := uint64(0); i < count; i++ {
		if _, err := io.ReadFull(r, rec); err != nil {
			return nil, fmt.Errorf("read record header %d: %w", i, err)
		}
		klen := binary.LittleEndian.Uint32(rec[0:4])
		vlen := binary.LittleEndian.Uint32(rec[4:8])

		k := make([]byte, klen)
		if _, err := io.ReadFull(r, k); err != nil {
			return nil, fmt.Errorf("read key %d: %w", i, err)
		}
		v := make([]byte, vlen)
		if vlen > 0 {
			if _, err := io.ReadFull(r, v); err != nil {
				return nil, fmt.Errorf("read val %d: %w", i, err)
			}
		}
		pairs = append(pairs, Command{Op: OpPut, Key: k, Val: v, Mode: 1 /* ModeUpsert */})
	}
	return pairs, nil
}
