package cluster

import (
	"context"
	"time"

	"github.com/hashicorp/raft"
)

// appliedFenceInterval is the safety net for a leadership change this node
// never observed. Observations are delivered on a buffered channel that raft
// drops from when it is full, so the loop must not depend on seeing every one.
const appliedFenceInterval = time.Second

// watchLeadership registers the leadership observer. It runs before the node
// can win an election, so leadershipFenceLoop cannot miss the first one.
func (n *Node) watchLeadership() {
	// Buffered and non-blocking: raft must never stall on this loop, and the
	// timer in leadershipFenceLoop covers anything the buffer drops.
	n.fenceCh = make(chan raft.Observation, 4)
	n.fenceObs = raft.NewObserver(n.fenceCh, false, func(o *raft.Observation) bool {
		_, ok := o.Data.(raft.LeaderObservation)
		return ok
	})
	n.raft.RegisterObserver(n.fenceObs)
}

// leadershipFenceLoop keeps this node's FSM applied index able to reach the
// commit index for as long as the node is leader.
//
// It reacts to leadership observations immediately and re-checks on a timer, so
// a dropped observation delays the fence rather than losing it.
func (n *Node) leadershipFenceLoop(ctx context.Context) {
	defer n.wg.Done()
	defer n.raft.DeregisterObserver(n.fenceObs)

	ticker := time.NewTicker(appliedFenceInterval)
	defer ticker.Stop()

	for {
		// A leadership observation fires when leadership is assumed, which is
		// before the term's no-op is dispatched, let alone committed — so the
		// applied and commit indexes still agree and the gap is yet to open.
		// That path therefore always fences; only the safety net checks first.
		forced := false
		select {
		case <-ctx.Done():
			return
		case <-n.fenceCh:
			forced = true
		case <-ticker.C:
		}
		n.fenceAppliedIndex(forced)
	}
}

// fenceAppliedIndex proves the FSM goroutine has drained every entry committed
// so far and records that, so a linearizable read stops waiting on an index the
// FSM will never be handed.
//
// Raft appends exactly one no-op per leadership term and never routes it to an
// FSM (raft.prepareLog ignores LogNoop), so without this the FSM applied index
// sits permanently behind the commit index for the rest of the term and every
// linearizable read blocks until the next write happens to close the gap.
//
// A barrier entry is different: raft hands it to the FSM goroutine, which
// consumes its channel strictly in order. So once the barrier future resolves,
// every entry up to and including the barrier's own index has been processed,
// and the barrier index is a sound applied index.
//
// Redundant calls are cheap and harmless: unless forced, the common case
// returns after two atomic loads, and the applied index never moves backwards.
func (n *Node) fenceAppliedIndex(force bool) {
	if n.raft.State() != raft.Leader {
		return
	}
	if !force && n.fsm.AppliedIndex() >= n.raft.CommitIndex() {
		return
	}

	future := n.raft.Barrier(n.cfg.Timeout)
	if err := future.Error(); err != nil {
		// Losing leadership or shutting down mid-barrier is expected; the next
		// leader fences its own term.
		return
	}
	indexed, ok := future.(raft.IndexFuture)
	if !ok {
		return
	}
	n.fsm.ObserveAppliedIndex(indexed.Index())
}
