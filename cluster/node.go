package cluster

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
	"github.com/teddymalhan/pallasdb/db"
	pbv1 "github.com/teddymalhan/pallasdb/pb/v1"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Config holds all parameters needed to start a cluster node.
type Config struct {
	NodeID    string
	RaftAddr  string        // TCP address for Raft transport, e.g. ":7001"
	RaftDir   string        // directory for BoltDB log store and snapshots
	Bootstrap bool          // true only when starting the very first node of a fresh cluster
	JoinAddr  string        // gRPC address of an existing node; empty if bootstrapping
	Timeout   time.Duration // Raft apply timeout (default 10s)
}

// Node wraps a raft.Raft instance with its supporting infrastructure.
type Node struct {
	raft      *raft.Raft
	fsm       *FSM
	transport *raft.NetworkTransport
	logStore  *raftboltdb.BoltStore
	snapStore raft.SnapshotStore
	cfg       Config
}

// Open initializes all Raft infrastructure and starts the node.
func Open(kvStore *db.KV, cfg Config) (*Node, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}

	if err := os.MkdirAll(cfg.RaftDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir raft dir: %w", err)
	}

	rc := raft.DefaultConfig()
	rc.LocalID = raft.ServerID(cfg.NodeID)

	addr, err := net.ResolveTCPAddr("tcp", cfg.RaftAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve raft addr: %w", err)
	}
	transport, err := raft.NewTCPTransport(cfg.RaftAddr, addr, 3, 10*time.Second, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("new tcp transport: %w", err)
	}

	boltStore, err := openBoltStoreWithTimeout(filepath.Join(cfg.RaftDir, "raft.db"), 5*time.Second)
	if err != nil {
		_ = transport.Close()
		return nil, fmt.Errorf("open bolt store: %w", err)
	}

	snapStore, err := raft.NewFileSnapshotStore(cfg.RaftDir, 2, os.Stderr)
	if err != nil {
		_ = transport.Close()
		_ = closeBoltStore(boltStore)
		return nil, fmt.Errorf("new snapshot store: %w", err)
	}

	fsm := NewFSM(kvStore, kvStore.Options.Dirpath)

	r, err := raft.NewRaft(rc, fsm, boltStore, boltStore, snapStore, transport)
	if err != nil {
		_ = transport.Close()
		_ = closeBoltStore(boltStore)
		return nil, fmt.Errorf("new raft: %w", err)
	}

	if cfg.Bootstrap {
		configuration := raft.Configuration{
			Servers: []raft.Server{
				{
					ID:      rc.LocalID,
					Address: transport.LocalAddr(),
				},
			},
		}
		if f := r.BootstrapCluster(configuration); f.Error() != nil {
			_ = r.Shutdown()
			_ = transport.Close()
			_ = closeBoltStore(boltStore)
			return nil, fmt.Errorf("bootstrap: %w", f.Error())
		}
	}

	node := &Node{
		raft:      r,
		fsm:       fsm,
		transport: transport,
		logStore:  boltStore,
		snapStore: snapStore,
		cfg:       cfg,
	}

	if cfg.JoinAddr != "" {
		if err := sendJoinRequest(cfg.JoinAddr, cfg.NodeID, cfg.RaftAddr, cfg.Timeout); err != nil {
			_ = node.Shutdown()
			return nil, fmt.Errorf("join cluster: %w", err)
		}
	}

	return node, nil
}

// Raft returns the underlying raft.Raft instance.
func (n *Node) Raft() *raft.Raft { return n.raft }

// FSM returns the FSM wrapping the KV store.
func (n *Node) FSM() *FSM { return n.fsm }

// LeaderAddr returns the current leader's Raft transport address.
func (n *Node) LeaderAddr() string {
	addr, _ := n.raft.LeaderWithID()
	return string(addr)
}

// AddVoter adds a peer to the cluster. Safe to call on the leader; idempotent.
func (n *Node) AddVoter(nodeID, addr string) error {
	f := n.raft.AddVoter(raft.ServerID(nodeID), raft.ServerAddress(addr), 0, n.cfg.Timeout)
	return f.Error()
}

// Shutdown cleanly stops Raft and closes BoltDB.
func (n *Node) Shutdown() error {
	if f := n.raft.Shutdown(); f.Error() != nil {
		return f.Error()
	}
	_ = n.transport.Close()
	return closeBoltStore(n.logStore)
}

func closeBoltStore(s *raftboltdb.BoltStore) error {
	return s.Close()
}

func sendJoinRequest(joinAddr, nodeID, raftAddr string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	conn, err := grpc.NewClient(joinAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("create join grpc client: %w", err)
	}
	defer func() { _ = conn.Close() }()

	client := pbv1.NewClusterServiceClient(conn)
	_, err = client.Join(ctx, &pbv1.JoinRequest{NodeId: nodeID, RaftAddr: raftAddr})
	if err != nil {
		return fmt.Errorf("call join grpc: %w", err)
	}
	return nil
}

// openBoltStoreWithTimeout opens a BoltStore with a lock timeout so that a
// previous crashed instance does not block startup indefinitely.
func openBoltStoreWithTimeout(path string, timeout time.Duration) (*raftboltdb.BoltStore, error) {
	return raftboltdb.New(raftboltdb.Options{
		Path: path,
		BoltOptions: &bolt.Options{
			Timeout: timeout,
		},
	})
}
