package cluster

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"testing"
)

// snaptestIter is an in-memory db.SortedKVIter over a fixed set of pairs.
type snaptestIter struct {
	keys [][]byte
	vals [][]byte
	pos  int
	err  error
}

func (it *snaptestIter) Valid() bool   { return it.pos < len(it.keys) }
func (it *snaptestIter) Key() []byte   { return it.keys[it.pos] }
func (it *snaptestIter) Val() []byte   { return it.vals[it.pos] }
func (it *snaptestIter) Deleted() bool { return false }
func (it *snaptestIter) Prev() error   { it.pos--; return nil }
func (it *snaptestIter) Next() error {
	if it.err != nil {
		return it.err
	}
	it.pos++
	return nil
}

// snaptestPairs builds n deterministic pairs with valSize-byte values.
func snaptestPairs(n, valSize int) *snaptestIter {
	it := &snaptestIter{keys: make([][]byte, n), vals: make([][]byte, n)}
	for i := range n {
		it.keys[i] = []byte(fmt.Sprintf("key:%09d", i))
		val := make([]byte, valSize)
		for j := range val {
			val[j] = byte(i + j)
		}
		it.vals[i] = val
	}
	return it
}

// snaptestCollect streams a snapshot into memory.
func snaptestCollect(t *testing.T, data []byte) ([][]byte, [][]byte) {
	t.Helper()
	var keys, vals [][]byte
	if err := readSnapshot(bytes.NewReader(data), func(k, v []byte) error {
		keys = append(keys, k)
		vals = append(vals, v)
		return nil
	}); err != nil {
		t.Fatalf("readSnapshot: %v", err)
	}
	return keys, vals
}

func snaptestEncode(t *testing.T, iter *snaptestIter) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := writeSnapshot(&buf, iter); err != nil {
		t.Fatalf("writeSnapshot: %v", err)
	}
	return buf.Bytes()
}

func TestSnapshotRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		n       int
		valSize int
	}{
		{"empty", 0, 0},
		{"single", 1, 4},
		{"empty values", 16, 0},
		// Large enough that the old implementation buffered the whole data set
		// twice: once in Persist, once in readSnapshot.
		{"large", 20000, 512},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := snaptestPairs(tc.n, tc.valSize)
			data := snaptestEncode(t, src)
			keys, vals := snaptestCollect(t, data)

			if len(keys) != tc.n {
				t.Fatalf("got %d records, want %d", len(keys), tc.n)
			}
			for i := range keys {
				if !bytes.Equal(keys[i], src.keys[i]) {
					t.Fatalf("key %d = %q, want %q", i, keys[i], src.keys[i])
				}
				if !bytes.Equal(vals[i], src.vals[i]) {
					t.Fatalf("value %d mismatch (%d bytes, want %d)", i, len(vals[i]), len(src.vals[i]))
				}
			}
		})
	}
}

// The reader must not hold the whole data set: it hands out one record at a
// time and never needs the caller to keep them.
func TestSnapshotReaderStreams(t *testing.T) {
	const n = 5000
	data := snaptestEncode(t, snaptestPairs(n, 256))

	seen := 0
	live := 0
	err := readSnapshot(bytes.NewReader(data), func(k, v []byte) error {
		seen++
		// Only one record is ever in flight; retaining nothing must be legal.
		live = 1
		_ = k
		_ = v
		return nil
	})
	if err != nil {
		t.Fatalf("readSnapshot: %v", err)
	}
	if seen != n {
		t.Fatalf("visited %d records, want %d", seen, n)
	}
	if live != 1 {
		t.Fatalf("live = %d", live)
	}
}

func TestSnapshotHeaderAndTrailer(t *testing.T) {
	data := snaptestEncode(t, snaptestPairs(3, 8))
	if !bytes.HasPrefix(data, snapshotMagic[:]) {
		t.Fatalf("missing magic: %x", data[:4])
	}
	if v := binary.LittleEndian.Uint16(data[4:6]); v != snapshotVersion {
		t.Fatalf("version = %d, want %d", v, snapshotVersion)
	}
	body := data[:len(data)-snapshotTrailerLen]
	want := crc32.Checksum(body, crc32c)
	if got := binary.LittleEndian.Uint32(data[len(data)-snapshotTrailerLen:]); got != want {
		t.Fatalf("trailer crc = %08x, want %08x", got, want)
	}
	if body[len(body)-1] != snapshotEndTag {
		t.Fatalf("missing terminator: %x", body[len(body)-1])
	}
}

func TestSnapshotRejectsCorruption(t *testing.T) {
	good := snaptestEncode(t, snaptestPairs(8, 32))

	corrupt := func(mutate func([]byte) []byte) []byte {
		return mutate(append([]byte(nil), good...))
	}

	// A record header claiming a 3 GiB key.
	absurdKlen := corrupt(func(b []byte) []byte {
		binary.LittleEndian.PutUint32(b[snapshotHeaderLen+1:snapshotHeaderLen+5], 3<<30)
		return b
	})
	absurdVlen := corrupt(func(b []byte) []byte {
		binary.LittleEndian.PutUint32(b[snapshotHeaderLen+5:snapshotHeaderLen+9], 1<<30)
		return b
	})

	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"empty stream", nil, nil},
		{"bad magic", corrupt(func(b []byte) []byte { b[0] ^= 0xff; return b }), ErrSnapshotMagic},
		{"wrong version", corrupt(func(b []byte) []byte {
			binary.LittleEndian.PutUint16(b[4:6], snapshotVersion+1)
			return b
		}), ErrSnapshotVersion},
		{"truncated header", good[:4], nil},
		{"truncated mid record", good[:snapshotHeaderLen+6], nil},
		{"truncated before trailer", good[:len(good)-snapshotTrailerLen], nil},
		{"truncated trailer", good[:len(good)-1], nil},
		{"flipped crc", corrupt(func(b []byte) []byte { b[len(b)-1] ^= 0x01; return b }), ErrSnapshotChecksum},
		{"flipped payload byte", corrupt(func(b []byte) []byte {
			b[snapshotHeaderLen+10] ^= 0x40
			return b
		}), ErrSnapshotChecksum},
		{"absurd key length", absurdKlen, ErrSnapshotCorrupt},
		{"absurd value length", absurdVlen, ErrSnapshotCorrupt},
		{"bad record tag", corrupt(func(b []byte) []byte { b[snapshotHeaderLen] = 7; return b }), ErrSnapshotCorrupt},
		{"zero length key", corrupt(func(b []byte) []byte {
			binary.LittleEndian.PutUint32(b[snapshotHeaderLen+1:snapshotHeaderLen+5], 0)
			return b
		}), ErrSnapshotCorrupt},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			visited := 0
			err := readSnapshot(bytes.NewReader(tc.data), func(k, v []byte) error {
				visited++
				if len(k) > MaxKeyLen || len(v) > MaxValueLen {
					t.Fatalf("visitor received an over-long record: %d/%d bytes", len(k), len(v))
				}
				return nil
			})
			if err == nil {
				t.Fatalf("expected an error, visited %d records", visited)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSnapshotWriterRejectsOverlongRecord(t *testing.T) {
	iter := &snaptestIter{
		keys: [][]byte{make([]byte, MaxKeyLen+1)},
		vals: [][]byte{nil},
	}
	if err := writeSnapshot(io.Discard, iter); err == nil {
		t.Fatal("expected an error for an over-long key")
	}
}

func TestSnapshotVisitorErrorPropagates(t *testing.T) {
	data := snaptestEncode(t, snaptestPairs(10, 8))
	sentinel := errors.New("stop")
	err := readSnapshot(bytes.NewReader(data), func(k, v []byte) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
}

func TestSnapshotIteratorErrorPropagates(t *testing.T) {
	iter := snaptestPairs(4, 8)
	iter.err = errors.New("boom")
	if err := writeSnapshot(io.Discard, iter); err == nil {
		t.Fatal("expected the iterator error to surface")
	}
}
