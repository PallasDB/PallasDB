package cluster

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
)

func cmdtestRoundTrip(t *testing.T, cmd Command) Command {
	t.Helper()
	data, err := EncodeCommand(cmd)
	if err != nil {
		t.Fatalf("EncodeCommand(%+v): %v", cmd, err)
	}
	got, err := DecodeCommand(data)
	if err != nil {
		t.Fatalf("DecodeCommand(%x): %v", data, err)
	}
	return got
}

func cmdtestEqualMutation(a, b Mutation) bool {
	return a.Op == b.Op && bytes.Equal(a.Key, b.Key) && bytes.Equal(a.Val, b.Val) && a.Mode == b.Mode
}

func TestCommandRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		cmd  Command
	}{
		{"put upsert", Command{Op: OpPut, Key: []byte("alpha"), Val: []byte("one"), Mode: 1}},
		{"put insert", Command{Op: OpPut, Key: []byte("beta"), Val: []byte("two"), Mode: 2}},
		{"put update", Command{Op: OpPut, Key: []byte("gamma"), Val: []byte("three"), Mode: 3}},
		{"put empty value", Command{Op: OpPut, Key: []byte("delta"), Mode: 1}},
		{"put binary", Command{Op: OpPut, Key: []byte{0x00, 0xff, '{', 0xc1}, Val: []byte{0x7f, 0x00, 0x80}, Mode: 1}},
		{"put large value", Command{Op: OpPut, Key: []byte("big"), Val: bytes.Repeat([]byte{0xab}, 200000), Mode: 1}},
		{"del", Command{Op: OpDel, Key: []byte("alpha")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cmdtestRoundTrip(t, tc.cmd)
			if got.Op != tc.cmd.Op || !bytes.Equal(got.Key, tc.cmd.Key) ||
				!bytes.Equal(got.Val, tc.cmd.Val) || got.Mode != tc.cmd.Mode {
				t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, tc.cmd)
			}
		})
	}
}

func TestCommandBatchRoundTrip(t *testing.T) {
	cmd := NewBatchCommand(
		Mutation{Op: OpPut, Key: []byte("k1"), Val: []byte("v1"), Mode: 1},
		Mutation{Op: OpDel, Key: []byte("k2")},
		Mutation{Op: OpPut, Key: []byte("k3"), Val: nil, Mode: 2},
	)
	got := cmdtestRoundTrip(t, cmd)
	if got.Op != OpBatch {
		t.Fatalf("op = %q, want %q", got.Op, OpBatch)
	}
	if len(got.Batch) != len(cmd.Batch) {
		t.Fatalf("batch len = %d, want %d", len(got.Batch), len(cmd.Batch))
	}
	for i := range cmd.Batch {
		if !cmdtestEqualMutation(got.Batch[i], cmd.Batch[i]) {
			t.Fatalf("mutation %d = %+v, want %+v", i, got.Batch[i], cmd.Batch[i])
		}
	}
	if muts := got.Mutations(); len(muts) != 3 {
		t.Fatalf("Mutations() len = %d, want 3", len(muts))
	}
}

func TestCommandMutationsFlattensSingleOps(t *testing.T) {
	muts := Command{Op: OpPut, Key: []byte("k"), Val: []byte("v"), Mode: 2}.Mutations()
	if len(muts) != 1 || !cmdtestEqualMutation(muts[0], Mutation{Op: OpPut, Key: []byte("k"), Val: []byte("v"), Mode: 2}) {
		t.Fatalf("Mutations() = %+v", muts)
	}
}

// The whole point of leaving JSON behind: keys and values ride the log as raw
// bytes instead of base64.
func TestCommandEncodingIsCompactBinary(t *testing.T) {
	key := []byte("user:0000000000000042")
	val := bytes.Repeat([]byte{0xf0}, 512)
	cmd := Command{Op: OpPut, Key: key, Val: val, Mode: 1}

	binaryEnc, err := EncodeCommand(cmd)
	if err != nil {
		t.Fatalf("EncodeCommand: %v", err)
	}
	jsonEnc, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if len(binaryEnc) >= len(jsonEnc) {
		t.Fatalf("binary encoding %d bytes is not smaller than JSON %d bytes", len(binaryEnc), len(jsonEnc))
	}
	if !bytes.Contains(binaryEnc, key) || !bytes.Contains(binaryEnc, val) {
		t.Fatal("binary encoding does not carry raw key/value bytes")
	}
	if binaryEnc[0] != commandMagic || binaryEnc[1] != CommandVersion {
		t.Fatalf("missing magic/version header: %x", binaryEnc[:2])
	}
}

// A node whose Bolt log still holds JSON entries from the previous release must
// replay it.
func TestDecodeLegacyJSONCommand(t *testing.T) {
	cases := []struct {
		name string
		data string
		want Command
	}{
		{
			name: "put",
			data: `{"op":"put","key":"aGVsbG8=","val":"d29ybGQ=","mode":1}`,
			want: Command{Op: OpPut, Key: []byte("hello"), Val: []byte("world"), Mode: 1},
		},
		{
			name: "put insert mode",
			data: `{"op":"put","key":"aGVsbG8=","val":"d29ybGQ=","mode":2}`,
			want: Command{Op: OpPut, Key: []byte("hello"), Val: []byte("world"), Mode: 2},
		},
		{
			name: "del",
			data: `{"op":"del","key":"aGVsbG8="}`,
			want: Command{Op: OpDel, Key: []byte("hello")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeCommand([]byte(tc.data))
			if err != nil {
				t.Fatalf("DecodeCommand(legacy): %v", err)
			}
			if got.Op != tc.want.Op || !bytes.Equal(got.Key, tc.want.Key) ||
				!bytes.Equal(got.Val, tc.want.Val) || got.Mode != tc.want.Mode {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDecodeLegacyJSONRejectsUnknownOp(t *testing.T) {
	if _, err := DecodeCommand([]byte(`{"op":"frobnicate","key":"aGk="}`)); err == nil {
		t.Fatal("expected an error for an unknown legacy op")
	}
}

func TestEncodeCommandRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		cmd  Command
	}{
		{"unknown op", Command{Op: "frobnicate", Key: []byte("k")}},
		{"empty op", Command{Key: []byte("k")}},
		{"empty batch", Command{Op: OpBatch}},
		{"oversized key", Command{Op: OpPut, Key: make([]byte, MaxKeyLen+1), Mode: 1}},
		{"delete with value", Command{Op: OpDel, Key: []byte("k"), Val: []byte("v")}},
		{"batch with bad mutation", NewBatchCommand(Mutation{Op: "nope", Key: []byte("k")})},
		{"negative mode", Command{Op: OpPut, Key: []byte("k"), Mode: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if data, err := EncodeCommand(tc.cmd); err == nil {
				t.Fatalf("expected an error, got %x", data)
			}
		})
	}
}

func TestDecodeCommandRejectsCorruptInput(t *testing.T) {
	valid, err := EncodeCommand(Command{Op: OpPut, Key: []byte("k"), Val: []byte("v"), Mode: 1})
	if err != nil {
		t.Fatalf("EncodeCommand: %v", err)
	}

	wrongVersion := append([]byte(nil), valid...)
	wrongVersion[1] = CommandVersion + 1

	badOpcode := append([]byte(nil), valid...)
	badOpcode[2] = 99

	trailing := append(append([]byte(nil), valid...), 0x00)

	// A key length that would allocate a gigabyte if it were trusted.
	absurdKeyLen := []byte{commandMagic, CommandVersion, opCodePut}
	absurdKeyLen = binary.AppendUvarint(absurdKeyLen, 1<<30)
	absurdKeyLen = append(absurdKeyLen, 'k')

	// A batch header claiming far more mutations than the payload holds.
	absurdBatch := []byte{commandMagic, CommandVersion, opCodeBatch}
	absurdBatch = binary.AppendUvarint(absurdBatch, uint64(MaxBatchMutations)+1)

	shortBatch := []byte{commandMagic, CommandVersion, opCodeBatch}
	shortBatch = binary.AppendUvarint(shortBatch, 1000)

	cases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"unknown format", []byte{0x42, 0x00}},
		{"header only", []byte{commandMagic, CommandVersion}},
		{"wrong version", wrongVersion},
		{"bad opcode", badOpcode},
		{"truncated payload", valid[:len(valid)-1]},
		{"trailing bytes", trailing},
		{"absurd key length", absurdKeyLen},
		{"batch count over limit", absurdBatch},
		{"batch truncated", shortBatch},
		{"nested batch mutation", []byte{commandMagic, CommandVersion, opCodeBatch, 0x01, opCodeBatch, 0x01, 'k'}},
		{"malformed json", []byte(`{"op":`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := DecodeCommand(tc.data); err == nil {
				t.Fatalf("expected an error, decoded %+v", got)
			}
		})
	}
}

func TestDecodeCommandEmptyIsSentinel(t *testing.T) {
	if _, err := DecodeCommand(nil); !errors.Is(err, ErrEmptyCommand) {
		t.Fatalf("err = %v, want %v", err, ErrEmptyCommand)
	}
	if _, err := DecodeCommand([]byte{0x42}); !errors.Is(err, ErrUnknownCommandFormat) {
		t.Fatalf("err = %v, want %v", err, ErrUnknownCommandFormat)
	}
	if _, err := EncodeCommand(Command{Op: OpBatch}); !errors.Is(err, ErrEmptyBatch) {
		t.Fatalf("err = %v, want %v", err, ErrEmptyBatch)
	}
}
