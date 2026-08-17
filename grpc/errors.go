package grpcapi

import (
	"context"
	"errors"

	"github.com/hashicorp/raft"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// errorDomain scopes the structured error details this server attaches.
	errorDomain = "pallasdb.v1"
	// reasonNotLeader marks a redirect: the request must be retried against the
	// node named in the detail metadata.
	reasonNotLeader = "NOT_LEADER"

	metadataLeaderID       = "leader_id"
	metadataLeaderGRPCAddr = "leader_grpc_addr"
	metadataLeaderRaftAddr = "leader_raft_addr"
)

// raftErrorCode maps a Raft error onto the gRPC code that tells the client the
// truth about retrying.
//
//   - ErrNotLeader, ErrLeadershipLost, ErrRaftShutdown and a transfer in
//     progress all mean "this node cannot serve you, try again (elsewhere)".
//     Note that ErrLeadershipLost does not mean the write failed: it may or may
//     not have committed, so the client must retry idempotently.
//   - ErrEnqueueTimeout means the apply never entered the log within the
//     deadline, which is a deadline failure, not an internal one.
func raftErrorCode(err error) codes.Code {
	switch {
	case err == nil:
		return codes.OK
	case errors.Is(err, raft.ErrNotLeader),
		errors.Is(err, raft.ErrLeadershipLost),
		errors.Is(err, raft.ErrRaftShutdown),
		errors.Is(err, raft.ErrLeadershipTransferInProgress),
		errors.Is(err, raft.ErrCantBootstrap):
		return codes.Unavailable
	case errors.Is(err, raft.ErrEnqueueTimeout):
		return codes.DeadlineExceeded
	case errors.Is(err, raft.ErrAbortedByRestore):
		return codes.Aborted
	default:
		return codes.Internal
	}
}

// leaderInfo identifies the node a client should retry against.
type leaderInfo struct {
	ID       string
	GRPCAddr string
	RaftAddr string
}

func (l leaderInfo) known() bool { return l.ID != "" || l.RaftAddr != "" || l.GRPCAddr != "" }

// target is the address a client can actually dial. The Raft transport address
// is only a fallback hint; it is not a gRPC endpoint.
func (l leaderInfo) target() string {
	if l.GRPCAddr != "" {
		return l.GRPCAddr
	}
	return "unknown"
}

// notLeaderError builds the redirect returned by a non-leader. The dialable
// gRPC address is carried both in the message and as ErrorInfo details so
// clients can redirect programmatically instead of parsing prose.
func notLeaderError(leader leaderInfo) error {
	var st *status.Status
	if leader.known() {
		st = status.Newf(codes.Unavailable, "not leader; retry against %s", leader.target())
	} else {
		st = status.New(codes.Unavailable, "not leader; no leader elected")
	}
	return withLeaderDetails(st, leader)
}

// raftStatusError maps a Raft failure to a status, attaching leader details
// whenever the failure means the client should go somewhere else. A context
// error from the caller is reported as such rather than blamed on Raft.
func raftStatusError(op string, err error, leader leaderInfo) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	code := raftErrorCode(err)
	st := status.Newf(code, "%s: %v", op, err)
	if code == codes.Unavailable {
		return withLeaderDetails(st, leader)
	}
	return st.Err()
}

func withLeaderDetails(st *status.Status, leader leaderInfo) error {
	if !leader.known() {
		return st.Err()
	}
	detailed, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: reasonNotLeader,
		Domain: errorDomain,
		Metadata: map[string]string{
			metadataLeaderID:       leader.ID,
			metadataLeaderGRPCAddr: leader.GRPCAddr,
			metadataLeaderRaftAddr: leader.RaftAddr,
		},
	})
	if err != nil {
		// Attaching details can only fail if the status is OK, which it is not.
		return st.Err()
	}
	return detailed.Err()
}

// LeaderFromError extracts the redirect target a server attached to a
// not-leader error. It returns ok=false for any other error.
func LeaderFromError(err error) (nodeID, grpcAddr string, ok bool) {
	st, isStatus := status.FromError(err)
	if !isStatus {
		return "", "", false
	}
	for _, detail := range st.Details() {
		info, isInfo := detail.(*errdetails.ErrorInfo)
		if !isInfo || info.GetReason() != reasonNotLeader {
			continue
		}
		return info.GetMetadata()[metadataLeaderID], info.GetMetadata()[metadataLeaderGRPCAddr], true
	}
	return "", "", false
}
