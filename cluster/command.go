package cluster

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// Op identifies the mutation type in a Raft log entry.
type Op string

const (
	OpPut   Op = "put"
	OpDel   Op = "del"
	OpBatch Op = "batch"
)

// Wire format of a replicated command:
//
//	[1B magic][1B version][1B opcode][payload]
//
// put payload:   [uvarint klen][key][uvarint vlen][val][uvarint mode]
// del payload:   [uvarint klen][key]
// batch payload: [uvarint count]{ [1B opcode][put/del payload] }*count
//
// The first byte disambiguates the format: commands used to be encoded with
// encoding/json, so a leading '{' selects the legacy decoder. Encoding always
// produces the binary form.
const (
	commandMagic   byte = 0xC1
	CommandVersion byte = 1

	opCodePut   byte = 1
	opCodeDel   byte = 2
	opCodeBatch byte = 3

	legacyJSONPrefix byte = '{'
)

// Limits applied to every length read off the wire, for both Raft commands and
// snapshot records. A corrupt or hostile length field must not turn into a
// multi-gigabyte allocation.
const (
	// MaxKeyLen bounds a single key.
	MaxKeyLen = 1 << 20 // 1 MiB
	// MaxValueLen bounds a single value.
	MaxValueLen = 64 << 20 // 64 MiB
	// MaxBatchMutations bounds the number of mutations in one batch command.
	MaxBatchMutations = 1 << 20
)

var (
	// ErrEmptyCommand is returned when decoding zero bytes.
	ErrEmptyCommand = errors.New("cluster: empty command")
	// ErrUnknownCommandFormat is returned when the leading byte matches neither
	// the binary magic nor the legacy JSON prefix.
	ErrUnknownCommandFormat = errors.New("cluster: unknown command format")
	// ErrTrailingCommandBytes is returned when a command decodes but bytes remain.
	ErrTrailingCommandBytes = errors.New("cluster: trailing bytes after command")
	// ErrEmptyBatch is returned when encoding a batch with no mutations.
	ErrEmptyBatch = errors.New("cluster: batch command has no mutations")
)

// Mutation is a single key mutation inside a command.
// Mode mirrors db.UpdateMode: 1=Upsert, 2=Insert, 3=Update. It is only
// meaningful for OpPut.
type Mutation struct {
	Op   Op     `json:"op"`
	Key  []byte `json:"key"`
	Val  []byte `json:"val,omitempty"`
	Mode int    `json:"mode,omitempty"`
}

// Command is the payload written into each Raft log entry.
// For OpPut and OpDel the flat Key/Val/Mode fields carry the mutation; for
// OpBatch the mutations live in Batch and are applied in one db transaction.
type Command struct {
	Op    Op         `json:"op"`
	Key   []byte     `json:"key,omitempty"`
	Val   []byte     `json:"val,omitempty"`
	Mode  int        `json:"mode,omitempty"`
	Batch []Mutation `json:"batch,omitempty"`
}

// NewBatchCommand builds a command that applies every mutation atomically.
func NewBatchCommand(muts ...Mutation) Command {
	return Command{Op: OpBatch, Batch: muts}
}

// Mutations returns the mutations a command applies, flattening the single-key
// form so callers can treat every command uniformly.
func (c Command) Mutations() []Mutation {
	if c.Op == OpBatch {
		return c.Batch
	}
	return []Mutation{{Op: c.Op, Key: c.Key, Val: c.Val, Mode: c.Mode}}
}

func opCode(op Op) (byte, bool) {
	switch op {
	case OpPut:
		return opCodePut, true
	case OpDel:
		return opCodeDel, true
	case OpBatch:
		return opCodeBatch, true
	default:
		return 0, false
	}
}

func opFromCode(code byte) (Op, bool) {
	switch code {
	case opCodePut:
		return OpPut, true
	case opCodeDel:
		return OpDel, true
	case opCodeBatch:
		return OpBatch, true
	default:
		return "", false
	}
}

func uvarintLen(v uint64) int {
	n := 1
	for v >= 0x80 {
		v >>= 7
		n++
	}
	return n
}

func mutationSize(m Mutation) int {
	n := 1 + uvarintLen(uint64(len(m.Key))) + len(m.Key)
	if m.Op == OpPut {
		n += uvarintLen(uint64(len(m.Val))) + len(m.Val)
		n += uvarintLen(uint64(m.Mode))
	}
	return n
}

func appendMutation(buf []byte, m Mutation) []byte {
	code, _ := opCode(m.Op)
	buf = append(buf, code)
	buf = binary.AppendUvarint(buf, uint64(len(m.Key)))
	buf = append(buf, m.Key...)
	if m.Op == OpPut {
		buf = binary.AppendUvarint(buf, uint64(len(m.Val)))
		buf = append(buf, m.Val...)
		buf = binary.AppendUvarint(buf, uint64(m.Mode))
	}
	return buf
}

func validateMutation(m Mutation) error {
	switch m.Op {
	case OpPut, OpDel:
	default:
		return fmt.Errorf("cluster: unsupported mutation op %q", m.Op)
	}
	if len(m.Key) > MaxKeyLen {
		return fmt.Errorf("cluster: key of %d bytes exceeds limit %d", len(m.Key), MaxKeyLen)
	}
	if len(m.Val) > MaxValueLen {
		return fmt.Errorf("cluster: value of %d bytes exceeds limit %d", len(m.Val), MaxValueLen)
	}
	if m.Op == OpDel && len(m.Val) > 0 {
		return errors.New("cluster: delete mutation carries a value")
	}
	if m.Mode < 0 {
		return fmt.Errorf("cluster: negative update mode %d", m.Mode)
	}
	return nil
}

// EncodeCommand serialises a command into the compact versioned binary format.
func EncodeCommand(cmd Command) ([]byte, error) {
	code, ok := opCode(cmd.Op)
	if !ok {
		return nil, fmt.Errorf("cluster: unsupported command op %q", cmd.Op)
	}

	if cmd.Op != OpBatch {
		m := Mutation{Op: cmd.Op, Key: cmd.Key, Val: cmd.Val, Mode: cmd.Mode}
		if err := validateMutation(m); err != nil {
			return nil, err
		}
		buf := make([]byte, 0, 2+mutationSize(m))
		buf = append(buf, commandMagic, CommandVersion)
		return appendMutation(buf, m), nil
	}

	if len(cmd.Batch) == 0 {
		return nil, ErrEmptyBatch
	}
	if len(cmd.Batch) > MaxBatchMutations {
		return nil, fmt.Errorf("cluster: batch of %d mutations exceeds limit %d", len(cmd.Batch), MaxBatchMutations)
	}
	size := 2 + 1 + uvarintLen(uint64(len(cmd.Batch)))
	for _, m := range cmd.Batch {
		if err := validateMutation(m); err != nil {
			return nil, err
		}
		size += mutationSize(m)
	}
	buf := make([]byte, 0, size)
	buf = append(buf, commandMagic, CommandVersion, code)
	buf = binary.AppendUvarint(buf, uint64(len(cmd.Batch)))
	for _, m := range cmd.Batch {
		buf = appendMutation(buf, m)
	}
	return buf, nil
}

// DecodeCommand parses a Raft log payload. It accepts the current binary format
// and the legacy JSON format so a Raft log written by an older release still
// replays.
func DecodeCommand(data []byte) (Command, error) {
	if len(data) == 0 {
		return Command{}, ErrEmptyCommand
	}
	switch data[0] {
	case legacyJSONPrefix:
		return decodeLegacyJSON(data)
	case commandMagic:
		return decodeBinary(data)
	default:
		return Command{}, fmt.Errorf("%w: leading byte 0x%02x", ErrUnknownCommandFormat, data[0])
	}
}

func decodeLegacyJSON(data []byte) (Command, error) {
	var cmd Command
	if err := json.Unmarshal(data, &cmd); err != nil {
		return Command{}, fmt.Errorf("cluster: decode legacy command: %w", err)
	}
	if _, ok := opCode(cmd.Op); !ok {
		return Command{}, fmt.Errorf("cluster: legacy command has unsupported op %q", cmd.Op)
	}
	return cmd, nil
}

func decodeBinary(data []byte) (Command, error) {
	if len(data) < 3 {
		return Command{}, errors.New("cluster: truncated command header")
	}
	if v := data[1]; v != CommandVersion {
		return Command{}, fmt.Errorf("cluster: unsupported command version %d (want %d)", v, CommandVersion)
	}
	op, ok := opFromCode(data[2])
	if !ok {
		return Command{}, fmt.Errorf("cluster: unknown command opcode %d", data[2])
	}

	rest := data[3:]
	if op != OpBatch {
		m, rest, err := decodeMutationBody(op, rest)
		if err != nil {
			return Command{}, err
		}
		if len(rest) != 0 {
			return Command{}, ErrTrailingCommandBytes
		}
		return Command{Op: m.Op, Key: m.Key, Val: m.Val, Mode: m.Mode}, nil
	}

	count, n := binary.Uvarint(rest)
	if n <= 0 {
		return Command{}, errors.New("cluster: bad batch count")
	}
	if count > MaxBatchMutations {
		return Command{}, fmt.Errorf("cluster: batch count %d exceeds limit %d", count, MaxBatchMutations)
	}
	rest = rest[n:]
	batch := make([]Mutation, 0, min(count, 1024))
	for i := uint64(0); i < count; i++ {
		if len(rest) == 0 {
			return Command{}, fmt.Errorf("cluster: truncated batch at mutation %d", i)
		}
		mop, ok := opFromCode(rest[0])
		if !ok || mop == OpBatch {
			return Command{}, fmt.Errorf("cluster: bad mutation opcode %d at %d", rest[0], i)
		}
		var (
			m   Mutation
			err error
		)
		m, rest, err = decodeMutationBody(mop, rest[1:])
		if err != nil {
			return Command{}, fmt.Errorf("cluster: batch mutation %d: %w", i, err)
		}
		batch = append(batch, m)
	}
	if len(rest) != 0 {
		return Command{}, ErrTrailingCommandBytes
	}
	return Command{Op: OpBatch, Batch: batch}, nil
}

// decodeMutationBody parses the payload of one mutation (the opcode is already
// consumed) and returns the remaining bytes.
func decodeMutationBody(op Op, buf []byte) (Mutation, []byte, error) {
	key, buf, err := decodeBytes(buf, MaxKeyLen, "key")
	if err != nil {
		return Mutation{}, nil, err
	}
	m := Mutation{Op: op, Key: key}
	if op != OpPut {
		return m, buf, nil
	}
	val, buf, err := decodeBytes(buf, MaxValueLen, "value")
	if err != nil {
		return Mutation{}, nil, err
	}
	mode, n := binary.Uvarint(buf)
	if n <= 0 {
		return Mutation{}, nil, errors.New("cluster: bad update mode")
	}
	if mode > uint64(^uint32(0)) {
		return Mutation{}, nil, fmt.Errorf("cluster: absurd update mode %d", mode)
	}
	m.Val = val
	m.Mode = int(mode)
	return m, buf[n:], nil
}

func decodeBytes(buf []byte, limit int, what string) ([]byte, []byte, error) {
	n, read := binary.Uvarint(buf)
	if read <= 0 {
		return nil, nil, fmt.Errorf("cluster: bad %s length", what)
	}
	if n > uint64(limit) {
		return nil, nil, fmt.Errorf("cluster: %s length %d exceeds limit %d", what, n, limit)
	}
	buf = buf[read:]
	if uint64(len(buf)) < n {
		return nil, nil, fmt.Errorf("cluster: truncated %s: want %d bytes, have %d", what, n, len(buf))
	}
	if n == 0 {
		return nil, buf, nil
	}
	return buf[:n:n], buf[n:], nil
}
