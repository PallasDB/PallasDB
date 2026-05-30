package cluster

import (
	"encoding/json"
	"net/http"

	"github.com/hashicorp/raft"
)

// JoinRequest is the body of the HTTP join endpoint.
type JoinRequest struct {
	NodeID string `json:"node_id"`
	Addr   string `json:"addr"`
}

// JoinHandler returns an HTTP handler for POST /cluster/join.
// Only the leader processes the request; followers respond 503 with the leader address.
func JoinHandler(node *Node) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if node.raft.State() != raft.Leader {
			http.Error(w, "not leader; leader is "+node.LeaderAddr(), http.StatusServiceUnavailable)
			return
		}

		var req JoinRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.NodeID == "" || req.Addr == "" {
			http.Error(w, "node_id and addr are required", http.StatusBadRequest)
			return
		}

		if err := node.AddVoter(req.NodeID, req.Addr); err != nil {
			http.Error(w, "add voter: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}
