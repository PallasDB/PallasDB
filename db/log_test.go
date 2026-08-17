package db

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drain replays a log to its end, exactly as (*KV).openLog does.
func drain(log *Log) ([]Entry, error) {
	entries := []Entry{}
	for {
		ent := Entry{}
		eof, err := log.Read(&ent)
		if err != nil {
			return entries, err
		}
		if eof {
			return entries, nil
		}
		entries = append(entries, ent)
	}
}

// committedKeys replays a log and returns the keys of the committed records,
// applying the same "drop everything after the last commit" rule as recovery.
func committedKeys(t *testing.T, filename string) []string {
	t.Helper()
	log := Log{FileName: filename}
	require.NoError(t, log.Open())
	defer func() { assert.NoError(t, log.Close()) }()

	entries, err := drain(&log)
	require.NoError(t, err)

	committed := 0
	for i, ent := range entries {
		if ent.op == EntryCommit {
			committed = i + 1
		}
	}
	keys := []string{}
	for _, ent := range entries[:committed] {
		if ent.op != EntryCommit {
			keys = append(keys, string(ent.key))
		}
	}
	return keys
}

func writeTX(t *testing.T, log *Log, entries ...Entry) {
	t.Helper()
	for i := range entries {
		require.NoError(t, log.Write(&entries[i]))
	}
	require.NoError(t, log.Commit())
}

func TestLogRoundTrip(t *testing.T) {
	name := filepath.Join(t.TempDir(), "kv_log")
	log := Log{FileName: name}
	require.NoError(t, log.Open())

	// A fresh log is exactly a header, and its sequence floor is zero.
	st, err := log.fp.Stat()
	require.NoError(t, err)
	assert.EqualValues(t, logHeaderSize, st.Size())
	assert.EqualValues(t, 0, log.epoch)

	writeTX(t, &log, Entry{key: []byte("a"), val: []byte("1")})
	writeTX(t, &log, Entry{key: []byte("b"), op: EntryDel})
	require.NoError(t, log.Close())

	assert.Equal(t, []string{"a", "b"}, committedKeys(t, name))
}

// An aborted transaction leaves bytes behind. They must never be replayed, and
// the sequence numbers they consumed must never be reused.
func TestLogResetTXDiscardsUncommitted(t *testing.T) {
	name := filepath.Join(t.TempDir(), "kv_log")
	log := Log{FileName: name}
	require.NoError(t, log.Open())

	writeTX(t, &log, Entry{key: []byte("keep"), val: []byte("v")})
	seqAfterCommit := log.seq

	require.NoError(t, log.Write(&Entry{key: []byte("gone"), val: []byte("v")}))
	require.NoError(t, log.Write(&Entry{key: []byte("gone2"), val: []byte("v")}))
	log.ResetTX()
	assert.Equal(t, log.writer.committed, log.writer.offset, "cursor rewinds")
	assert.Greater(t, log.seq, seqAfterCommit, "sequence numbers are never reused")

	writeTX(t, &log, Entry{key: []byte("next"), val: []byte("v")})
	require.NoError(t, log.Close())

	assert.Equal(t, []string{"keep", "next"}, committedKeys(t, name))
}

// The regression that motivates the whole design, at the log level: a truncate
// that is acknowledged but never reaches disk, followed by a shorter
// transaction that lands exactly on an old record boundary.
func TestLogTruncateNotDurableDoesNotResurrect(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "kv_log")
	fs := withFaultFS(t, matchBase("kv_log"))

	log := Log{FileName: name}
	require.NoError(t, log.Open())

	// Two identically sized transactions, so that whatever we write after the
	// truncate can be made to end exactly where the second one begins.
	writeTX(t, &log, Entry{key: []byte("aaa"), val: []byte("0123456789")})
	firstTXEnd := log.writer.committed
	writeTX(t, &log, Entry{key: []byte("old"), val: []byte("0123456789")})
	fullSize := log.writer.committed

	// Flush: the truncate is acknowledged and does nothing.
	fs.arm(faultSpec{op: opTruncate, at: 1, mode: faultDrop})
	require.NoError(t, log.Truncate())
	require.True(t, fs.hasFired(), "the truncate fault must actually have fired")
	fs.disarm()

	st, err := log.fp.Stat()
	require.NoError(t, err)
	require.EqualValues(t, fullSize, st.Size(), "the stale bytes must still be on disk")

	// Now write a transaction of exactly the first transaction's size, so the
	// leftover tail starts on a record boundary and decodes cleanly.
	writeTX(t, &log, Entry{key: []byte("new"), val: []byte("0123456789")})
	require.EqualValues(t, firstTXEnd, log.writer.committed,
		"test setup: the new transaction must end where the old second one starts")
	require.NoError(t, log.Close())

	// Guard against the test decaying into a tautology: the leftover really is
	// a well-formed, CRC-valid record sitting exactly at the new commit point.
	// Only the sequence floor stands between it and replay.
	raw, err := os.ReadFile(name)
	require.NoError(t, err)
	stale := Entry{}
	require.NoError(t, stale.Decode(bytes.NewReader(raw[firstTXEnd:])))
	require.Equal(t, []byte("old"), stale.key)

	// Without the floor, replay would walk from the new transaction straight
	// into the old one and accept its trailing EntryCommit.
	assert.Equal(t, []string{"new"}, committedKeys(t, name))
}

// Truncate must raise the floor durably before it shrinks the file, and the
// floor must survive a restart.
func TestLogEpochSurvivesRestart(t *testing.T) {
	name := filepath.Join(t.TempDir(), "kv_log")
	log := Log{FileName: name}
	require.NoError(t, log.Open())
	writeTX(t, &log, Entry{key: []byte("a"), val: []byte("1")})
	seq := log.seq
	require.NoError(t, log.Truncate())
	assert.Equal(t, seq, log.epoch)
	require.NoError(t, log.Close())

	reopened := Log{FileName: name}
	require.NoError(t, reopened.Open())
	defer func() { assert.NoError(t, reopened.Close()) }()
	assert.Equal(t, seq, reopened.epoch, "the floor is persisted in the log header")
	assert.Equal(t, seq, reopened.seq, "replay starts above the floor")

	entries, err := drain(&reopened)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// Truncate keeps its promise even when the file truncate itself fails outright:
// the cursors still reset, so the next records land in front of the stale ones
// rather than behind them where replay would never reach them.
func TestLogTruncateFailureStillResetsCursor(t *testing.T) {
	name := filepath.Join(t.TempDir(), "kv_log")
	fs := withFaultFS(t, matchBase("kv_log"))

	log := Log{FileName: name}
	require.NoError(t, log.Open())
	writeTX(t, &log, Entry{key: []byte("old"), val: []byte("0123456789")})

	fs.arm(faultSpec{op: opTruncate, at: 1, mode: faultFail})
	err := log.Truncate()
	require.Error(t, err)
	require.True(t, fs.hasFired())
	fs.disarm()

	assert.EqualValues(t, logHeaderSize, log.writer.offset)
	assert.EqualValues(t, logHeaderSize, log.writer.committed)

	writeTX(t, &log, Entry{key: []byte("new"), val: []byte("x")})
	require.NoError(t, log.Close())
	assert.Equal(t, []string{"new"}, committedKeys(t, name))
}

// If raising the sequence floor fails, Truncate must report it and must not go
// on to destroy a log it cannot fence.
//
// The two failure points differ in what recovery is allowed to do afterwards.
// A failed header *write* leaves the old floor on disk, so the records must
// still replay. A failed header *sync* leaves it unknown whether the new floor
// landed, so either outcome is correct -- and both are safe, because Truncate
// is only ever called once the memtable it covers is already durable in an
// SSTable. What is never acceptable is losing the file itself.
func TestLogTruncateFailsWhenFloorCannotBeRaised(t *testing.T) {
	setup := func(t *testing.T) (string, *faultFS, *Log) {
		t.Helper()
		name := filepath.Join(t.TempDir(), "kv_log")
		fs := withFaultFS(t, matchBase("kv_log"))
		log := &Log{FileName: name}
		require.NoError(t, log.Open())
		writeTX(t, log, Entry{key: []byte("a"), val: []byte("1")})
		return name, fs, log
	}

	t.Run("header write fails", func(t *testing.T) {
		name, fs, log := setup(t)
		fs.arm(faultSpec{op: opWriteAt, at: 1, mode: faultFail})
		require.Error(t, log.Truncate())
		require.True(t, fs.hasFired())
		fs.disarm()

		assert.EqualValues(t, 0, log.epoch, "the floor did not move")
		require.NoError(t, log.Close())
		assert.Equal(t, []string{"a"}, committedKeys(t, name))
	})

	t.Run("header sync fails", func(t *testing.T) {
		name, fs, log := setup(t)
		fs.arm(faultSpec{op: opSync, at: 1, mode: faultFail})
		require.Error(t, log.Truncate())
		require.True(t, fs.hasFired())
		fs.disarm()

		st, err := log.fp.Stat()
		require.NoError(t, err)
		assert.Greater(t, st.Size(), int64(logHeaderSize), "the log was not destroyed")
		require.NoError(t, log.Close())

		// Whichever way the ambiguous sync fell, reopening must succeed.
		reopened := Log{FileName: name}
		require.NoError(t, reopened.Open())
		_, err = drain(&reopened)
		require.NoError(t, err)
		assert.NoError(t, reopened.Close())
	})
}

// Replay physically drops the unreplayable tail, so a later short transaction
// cannot expose a stale record that happens to land on a record boundary.
func TestLogReplayTrimsTail(t *testing.T) {
	name := filepath.Join(t.TempDir(), "kv_log")
	log := Log{FileName: name}
	require.NoError(t, log.Open())
	writeTX(t, &log, Entry{key: []byte("a"), val: []byte("1")})
	committed := log.writer.committed

	// A transaction that was interrupted before its commit record landed.
	require.NoError(t, log.Write(&Entry{key: []byte("torn"), val: []byte("xxxx")}))
	require.NoError(t, log.Close())

	reopened := Log{FileName: name}
	require.NoError(t, reopened.Open())
	entries, err := drain(&reopened)
	require.NoError(t, err)
	assert.Len(t, entries, 3, "the uncommitted record is still read...")

	st, err := reopened.fp.Stat()
	require.NoError(t, err)
	assert.Equal(t, committed, st.Size(), "...but no longer exists on disk")
	require.NoError(t, reopened.Close())

	assert.Equal(t, []string{"a"}, committedKeys(t, name))
}

// A log with content but no readable header has no knowable sequence floor.
// Guessing one risks a resurrection, so Open fails instead.
func TestLogCorruptHeaderFailsOpen(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch func([]byte)
	}{
		{"bad magic", func(b []byte) { b[0] ^= 0xff }},
		{"bad checksum", func(b []byte) { b[8] ^= 0xff }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "kv_log")
			log := Log{FileName: name}
			require.NoError(t, log.Open())
			writeTX(t, &log, Entry{key: []byte("a"), val: []byte("1")})
			require.NoError(t, log.Close())

			raw, err := os.ReadFile(name)
			require.NoError(t, err)
			tc.patch(raw)
			require.NoError(t, os.WriteFile(name, raw, 0o644))

			broken := Log{FileName: name}
			err = broken.Open()
			require.ErrorIs(t, err, ErrLogHeaderCorrupt)
			assert.Nil(t, broken.fp, "a failed Open must not leak the handle")
		})
	}
}

// A log file that was created but never got its header is indistinguishable
// from a fresh one, because no complete record can precede the header.
func TestLogHeaderlessFileOpensFresh(t *testing.T) {
	name := filepath.Join(t.TempDir(), "kv_log")
	require.NoError(t, os.WriteFile(name, []byte{'P', 'D', 'B'}, 0o644))

	log := Log{FileName: name}
	require.NoError(t, log.Open())
	defer func() { assert.NoError(t, log.Close()) }()
	assert.EqualValues(t, 0, log.epoch)

	entries, err := drain(&log)
	require.NoError(t, err)
	assert.Empty(t, entries)
}
