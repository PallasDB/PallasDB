package discovery

// Config holds Serf discovery settings for a PallasDB node.
type Config struct {
	NodeID            string
	GRPCAddr          string
	RaftAddr          string
	SerfAddr          string
	SerfAdvertiseAddr string
	JoinAddrs         []string
	EventBuffer       int
	// EncryptKey is the memberlist gossip encryption key. It must be 16, 24,
	// or 32 bytes; nil leaves gossip in plaintext.
	EncryptKey []byte
}

// MemberStatus is the discovery-layer status of a node.
type MemberStatus string

const (
	MemberStatusUnknown MemberStatus = "unknown"
	MemberStatusAlive   MemberStatus = "alive"
	MemberStatusLeaving MemberStatus = "leaving"
	MemberStatusLeft    MemberStatus = "left"
	MemberStatusFailed  MemberStatus = "failed"
)

// NodeInfo describes a PallasDB node discovered through Serf gossip.
type NodeInfo struct {
	NodeID   string
	GRPCAddr string
	RaftAddr string
	SerfAddr string
	Status   MemberStatus
}

// EventType identifies a discovery event kind.
type EventType int

const (
	EventMemberJoin EventType = iota + 1
	EventMemberUpdate
	EventMemberLeave
	EventMemberFailed
)

// Event is a normalized discovery event emitted by the Serf adapter.
type Event struct {
	Type   EventType
	Member NodeInfo
}
