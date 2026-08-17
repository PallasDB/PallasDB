package db

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEntryEncode moved here from kv_test.go when Entry grew its sequence
// number: the record format belongs to kv_entry.go, and so does its test.
func TestEntryEncode(t *testing.T) {
	ent := Entry{key: []byte("k1"), val: []byte("xxx"), seq: 7}
	data := []byte{
		0x8e, 0x1a, 0x06, 0x82, // crc32 of everything below
		7, 0, 0, 0, 0, 0, 0, 0, // seq
		2, 0, 0, 0, // klen
		3, 0, 0, 0, // vlen
		0,                       // op = EntryAdd
		'k', '1', 'x', 'x', 'x', // key, val
	}

	encoded, err := ent.Encode()
	require.NoError(t, err)
	assert.Equal(t, data, encoded)

	decoded := Entry{}
	require.NoError(t, decoded.Decode(bytes.NewBuffer(data)))
	assert.Equal(t, ent, decoded)

	ent = Entry{key: []byte("k1"), val: []byte{}, seq: 1 << 40, op: EntryDel}
	data = []byte{
		0xb9, 0x49, 0x7e, 0xc9,
		0, 0, 0, 0, 0, 1, 0, 0, // seq = 1<<40, proving the field is a full u64
		2, 0, 0, 0,
		0, 0, 0, 0,
		1,
		'k', '1',
	}

	encoded, err = ent.Encode()
	require.NoError(t, err)
	assert.Equal(t, data, encoded)

	decoded = Entry{}
	require.NoError(t, decoded.Decode(bytes.NewBuffer(data)))
	assert.Equal(t, ent, decoded)
}

// The sequence number is inside the checksummed region so that a record cannot
// be re-stamped with a different generation and still look valid.
func TestEntrySeqIsChecksummed(t *testing.T) {
	ent := Entry{key: []byte("k"), val: []byte("v"), seq: 3, op: EntryAdd}
	data, err := ent.Encode()
	require.NoError(t, err)

	binary.LittleEndian.PutUint64(data[4:12], 4)
	assert.ErrorIs(t, (&Entry{}).Decode(bytes.NewBuffer(data)), ErrBadSum)
}

func TestEntryEncodeRejectsOversized(t *testing.T) {
	// The read side has always refused records above MaxEntrySize. Before the
	// write side agreed, an oversized value went into the log happily and then
	// made every subsequent Open fail: the store bricked itself.
	for _, tc := range []struct {
		name     string
		klen     int
		vlen     int
		rejected bool
	}{
		{"at the limit", 16, MaxEntrySize - 16, false},
		{"one byte over", 16, MaxEntrySize - 15, true},
		{"value alone over", 1, MaxEntrySize + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ent := Entry{
				key: bytes.Repeat([]byte("k"), tc.klen),
				val: bytes.Repeat([]byte("v"), tc.vlen),
			}
			data, err := ent.Encode()
			if !tc.rejected {
				require.NoError(t, err)
				assert.Len(t, data, entryHeaderSize+tc.klen+tc.vlen)
				return
			}
			require.ErrorIs(t, err, ErrEntryTooLarge)
			assert.Nil(t, data)
		})
	}
}

func TestEntryDecodeRejectsGarbageHeader(t *testing.T) {
	valid := func() []byte {
		data, err := (&Entry{key: []byte("k"), val: []byte("v"), seq: 1}).Encode()
		require.NoError(t, err)
		return data
	}

	t.Run("oversized length", func(t *testing.T) {
		data := valid()
		binary.LittleEndian.PutUint32(data[16:20], MaxEntrySize+1)
		// Rejected on the length field alone, before any allocation: a corrupt
		// vlen must not be able to make recovery allocate 4 GiB.
		assert.ErrorIs(t, (&Entry{}).Decode(bytes.NewBuffer(data)), ErrEntryTooLarge)
	})

	t.Run("unknown op", func(t *testing.T) {
		ent := Entry{key: []byte("k"), val: []byte("v"), seq: 1, op: EntryOp(9)}
		data, err := ent.Encode()
		require.NoError(t, err) // the encoder does not police ops...
		assert.ErrorIs(t, (&Entry{}).Decode(bytes.NewBuffer(data)), ErrBadEntryOp)
	})

	t.Run("truncated body", func(t *testing.T) {
		data := valid()
		err := (&Entry{}).Decode(bytes.NewBuffer(data[:len(data)-1]))
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})

	t.Run("truncated header", func(t *testing.T) {
		err := (&Entry{}).Decode(bytes.NewBuffer(valid()[:4]))
		assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})
}

// A log that already contains an oversized record must still open. The record
// is unreadable, so recovery has to treat it as the end of the log rather than
// as a fatal error -- otherwise one bad record is a permanent outage.
func TestLogOpensPastOversizedRecord(t *testing.T) {
	dir := t.TempDir()
	log := Log{FileName: dir + "/kv_log"}
	require.NoError(t, log.Open())

	require.NoError(t, log.Write(&Entry{key: []byte("keep"), val: []byte("me"), op: EntryAdd}))
	require.NoError(t, log.Commit())
	good := log.writer.committed

	// Forge a record whose length field claims more than MaxEntrySize. Encode
	// refuses to build one, which is the whole point, so patch it in.
	forged, err := (&Entry{key: []byte("x"), val: []byte("y"), seq: 99}).Encode()
	require.NoError(t, err)
	binary.LittleEndian.PutUint32(forged[16:20], MaxEntrySize+1)
	_, err = log.fp.WriteAt(forged, good)
	require.NoError(t, err)
	require.NoError(t, log.Close())

	reopened := Log{FileName: dir + "/kv_log"}
	require.NoError(t, reopened.Open())
	defer func() { assert.NoError(t, reopened.Close()) }()

	entries, err := drain(&reopened)
	require.NoError(t, err, "an oversized record must not fail Open")
	require.Len(t, entries, 2)
	assert.Equal(t, []byte("keep"), entries[0].key)
	assert.Equal(t, EntryCommit, entries[1].op)
	assert.Equal(t, good, reopened.writer.committed)
}

func TestEntryEncodeErrorMentionsLimit(t *testing.T) {
	_, err := (&Entry{val: make([]byte, MaxEntrySize+1)}).Encode()
	require.ErrorIs(t, err, ErrEntryTooLarge)
	assert.True(t, strings.Contains(err.Error(), "67108864"),
		"the error should name the limit, got %q", err)
	assert.False(t, errors.Is(err, ErrBadSum))
}
