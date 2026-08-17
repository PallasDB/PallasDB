package discovery

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"

	"github.com/hashicorp/serf/serf"
)

const (
	serfTagService  = "service"
	serfTagNodeID   = "node_id"
	serfTagGRPCAddr = "grpc_addr"
	serfTagRaftAddr = "raft_addr"
	serviceName     = "pallasdb"
)

// SerfDiscovery wraps a Serf instance behind the cluster discovery interface.
type SerfDiscovery struct {
	cfg     Config
	eventCh chan serf.Event
	events  chan Event

	mu   sync.RWMutex
	serf *serf.Serf
}

// NewSerfDiscovery builds a Serf-backed discovery adapter.
func NewSerfDiscovery(cfg Config) (*SerfDiscovery, error) {
	if cfg.NodeID == "" {
		return nil, fmt.Errorf("node id is required")
	}
	if cfg.RaftAddr == "" {
		return nil, fmt.Errorf("raft addr is required")
	}
	if cfg.SerfAddr == "" {
		return nil, fmt.Errorf("serf addr is required")
	}
	if cfg.EventBuffer <= 0 {
		cfg.EventBuffer = 64
	}
	if err := validateEncryptKey(cfg.EncryptKey); err != nil {
		return nil, err
	}
	return &SerfDiscovery{
		cfg:     cfg,
		eventCh: make(chan serf.Event, cfg.EventBuffer),
		events:  make(chan Event, cfg.EventBuffer),
	}, nil
}

// Start creates the underlying Serf node and joins configured peers.
func (d *SerfDiscovery) Start() error {
	conf := serf.DefaultConfig()
	conf.NodeName = d.cfg.NodeID
	conf.EventCh = d.eventCh
	conf.EventBuffer = d.cfg.EventBuffer
	conf.LogOutput = io.Discard
	conf.MemberlistConfig.LogOutput = io.Discard
	conf.Tags = map[string]string{
		serfTagService:  serviceName,
		serfTagNodeID:   d.cfg.NodeID,
		serfTagGRPCAddr: d.cfg.GRPCAddr,
		serfTagRaftAddr: d.cfg.RaftAddr,
	}

	bindHost, bindPort, err := splitHostPort(d.cfg.SerfAddr)
	if err != nil {
		return fmt.Errorf("parse serf addr: %w", err)
	}
	conf.MemberlistConfig.BindAddr = bindHost
	conf.MemberlistConfig.BindPort = bindPort

	if d.cfg.SerfAdvertiseAddr != "" {
		advertiseHost, advertisePort, err := splitHostPort(d.cfg.SerfAdvertiseAddr)
		if err != nil {
			return fmt.Errorf("parse serf advertise addr: %w", err)
		}
		conf.MemberlistConfig.AdvertiseAddr = advertiseHost
		conf.MemberlistConfig.AdvertisePort = advertisePort
	}

	if len(d.cfg.EncryptKey) > 0 {
		conf.MemberlistConfig.SecretKey = d.cfg.EncryptKey
	}

	s, err := serf.Create(conf)
	if err != nil {
		return fmt.Errorf("create serf: %w", err)
	}

	d.mu.Lock()
	d.serf = s
	d.mu.Unlock()

	go d.forwardEvents(s)

	if len(d.cfg.JoinAddrs) > 0 {
		if _, err := s.Join(d.cfg.JoinAddrs, true); err != nil {
			_ = d.Shutdown()
			return fmt.Errorf("join serf cluster: %w", err)
		}
	}
	return nil
}

// Events returns normalized membership events.
func (d *SerfDiscovery) Events() <-chan Event { return d.events }

// Members returns the current known PallasDB members.
func (d *SerfDiscovery) Members() []NodeInfo {
	d.mu.RLock()
	s := d.serf
	d.mu.RUnlock()
	if s == nil {
		return nil
	}

	members := s.Members()
	infos := make([]NodeInfo, 0, len(members))
	for _, member := range members {
		info, ok := nodeInfoFromMember(member)
		if ok {
			infos = append(infos, info)
		}
	}
	return infos
}

// Leave announces an intentional departure to the rest of the gossip pool, so
// peers treat it as a graceful leave rather than a failure. Callers that want
// the node to be considered temporarily gone should skip it and call Shutdown
// on its own.
func (d *SerfDiscovery) Leave() error {
	d.mu.RLock()
	s := d.serf
	d.mu.RUnlock()
	if s == nil {
		return nil
	}
	return s.Leave()
}

// Shutdown stops the Serf node without announcing a departure.
func (d *SerfDiscovery) Shutdown() error {
	d.mu.RLock()
	s := d.serf
	d.mu.RUnlock()
	if s == nil {
		return nil
	}
	return s.Shutdown()
}

func (d *SerfDiscovery) forwardEvents(s *serf.Serf) {
	defer close(d.events)
	for {
		select {
		case event, ok := <-d.eventCh:
			if !ok {
				return
			}
			memberEvent, ok := event.(serf.MemberEvent)
			if !ok {
				continue
			}

			eventType, ok := normalizeEventType(memberEvent.Type)
			if !ok {
				continue
			}
			for _, member := range memberEvent.Members {
				info, ok := nodeInfoFromMember(member)
				if !ok {
					continue
				}
				select {
				case d.events <- Event{Type: eventType, Member: info}:
				case <-s.ShutdownCh():
					return
				}
			}
		case <-s.ShutdownCh():
			return
		}
	}
}

func normalizeEventType(eventType serf.EventType) (EventType, bool) {
	switch eventType {
	case serf.EventMemberJoin:
		return EventMemberJoin, true
	case serf.EventMemberUpdate:
		return EventMemberUpdate, true
	case serf.EventMemberLeave:
		return EventMemberLeave, true
	case serf.EventMemberFailed:
		return EventMemberFailed, true
	default:
		return 0, false
	}
}

func nodeInfoFromMember(member serf.Member) (NodeInfo, bool) {
	if member.Tags[serfTagService] != serviceName {
		return NodeInfo{}, false
	}
	nodeID := member.Tags[serfTagNodeID]
	raftAddr := member.Tags[serfTagRaftAddr]
	if nodeID == "" || raftAddr == "" {
		return NodeInfo{}, false
	}
	return NodeInfo{
		NodeID:   nodeID,
		GRPCAddr: member.Tags[serfTagGRPCAddr],
		RaftAddr: raftAddr,
		SerfAddr: net.JoinHostPort(member.Addr.String(), strconv.Itoa(int(member.Port))),
		Status:   normalizeMemberStatus(member.Status),
	}, true
}

func normalizeMemberStatus(status serf.MemberStatus) MemberStatus {
	switch status {
	case serf.StatusAlive:
		return MemberStatusAlive
	case serf.StatusLeaving:
		return MemberStatusLeaving
	case serf.StatusLeft:
		return MemberStatusLeft
	case serf.StatusFailed:
		return MemberStatusFailed
	default:
		return MemberStatusUnknown
	}
}

func splitHostPort(addr string) (host string, port int, err error) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	if host == "" {
		host = "0.0.0.0"
	}
	port, err = strconv.Atoi(portText)
	if err != nil {
		return "", 0, err
	}
	if port < 0 || port > 65535 {
		return "", 0, fmt.Errorf("port %d out of range", port)
	}
	return host, port, nil
}

// validateEncryptKey rejects gossip keys memberlist's AES cipher cannot use.
func validateEncryptKey(key []byte) error {
	switch len(key) {
	case 0, 16, 24, 32:
		return nil
	default:
		return fmt.Errorf("serf encrypt key must be 16, 24, or 32 bytes, got %d", len(key))
	}
}
