package cluster

import (
	"strings"
	"testing"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/require"
)

// A committed entry this binary cannot decode is deterministic: every replica
// running this build reaches the same conclusion, so the abort message is the
// only diagnostic an operator gets and has to name the actual remedy.
func TestUndecodableEntryNamesTheUpgrade(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{
			name: "newer command version",
			data: []byte{commandMagic, CommandVersion + 1, 0},
			want: "newer PallasDB than this binary",
		},
		{
			name: "unknown opcode",
			data: []byte{commandMagic, CommandVersion, 0xEE},
			want: "newer PallasDB than this binary",
		},
		{
			name: "unrecognised format",
			data: []byte{0xFF, 0x00},
			want: "newer PallasDB than this binary",
		},
		{
			name: "corrupt binary command",
			data: []byte{commandMagic},
			want: "damaged",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fsm := fsmtestNew(t)
			var msg string
			func() {
				defer func() {
					r := recover()
					require.NotNil(t, r, "expected the FSM to abort")
					s, ok := r.(string)
					require.True(t, ok, "panic value %v", r)
					msg = s
				}()
				fsm.Apply(&raft.Log{Index: 4711, Data: tc.data})
			}()

			require.True(t, strings.Contains(msg, tc.want),
				"abort message %q does not explain the remedy (%q)", msg, tc.want)
			require.True(t, strings.Contains(msg, "4711"),
				"abort message %q does not name the offending entry index", msg)
		})
	}
}

// The entry must never be skipped: skipping forks this replica's data from the
// cluster's, which no later tooling can undo.
func TestUndecodableEntryDoesNotAdvanceApplied(t *testing.T) {
	fsm := fsmtestNew(t)
	fsmtestPut(t, fsm, 7, "k", "v")

	fsmtestWantFatal(t, func() {
		fsm.Apply(&raft.Log{Index: 8, Data: []byte{commandMagic, CommandVersion + 1, 0}})
	})
	require.Equal(t, uint64(7), fsm.AppliedIndex(),
		"an entry the FSM refused to apply must not count as applied")
}
