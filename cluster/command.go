package cluster

import "encoding/json"

// Op identifies the mutation type in a Raft log entry.
type Op string

const (
	OpPut Op = "put"
	OpDel Op = "del"
)

// Command is the JSON payload written into each Raft log entry.
// Mode mirrors pallasdb.UpdateMode: 1=Upsert, 2=Insert, 3=Update.
// encoding/json marshals []byte fields as base64 automatically.
type Command struct {
	Op   Op     `json:"op"`
	Key  []byte `json:"key"`
	Val  []byte `json:"val,omitempty"`
	Mode int    `json:"mode,omitempty"`
}

func EncodeCommand(cmd Command) ([]byte, error) {
	return json.Marshal(cmd)
}

func DecodeCommand(data []byte) (Command, error) {
	var cmd Command
	return cmd, json.Unmarshal(data, &cmd)
}
