package client

import (
	"context"
	"net/http"
)

// ForceActiveStatus is whether the node asked may force itself active: only
// when no shard's voters that still answer can form a majority.
type ForceActiveStatus struct {
	NodeID        string   `json:"node_id"`
	Allowed       bool     `json:"allowed"`
	Reason        string   `json:"reason"`
	FailoverEpoch uint64   `json:"failover_epoch"`
	Shards        int      `json:"shards"`
	DownVoters    []string `json:"down_voters,omitempty"`
}

// ForceActiveResult reports a forced flip under way: the node restarts as
// every shard's only voter under its next incarnation.
type ForceActiveResult struct {
	NodeID          string `json:"node_id"`
	FailoverEpoch   uint64 `json:"failover_epoch"`
	NextIncarnation uint64 `json:"next_incarnation"`
	NextReplicaID   uint64 `json:"next_replica_id"`
	Shards          int    `json:"shards"`
}

// ForceActiveStatus asks the node this client talks to whether it may force
// itself active. Admin only.
func (c *Client) ForceActiveStatus(ctx context.Context) (*ForceActiveStatus, error) {
	var out ForceActiveStatus
	if err := c.do(ctx, http.MethodGet, "/api/cluster/force-active", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ForceActive makes the node this client talks to the active one, losing
// whatever the dead voters committed but never sent to it. Admin only;
// refused (409) while the cluster can still form a majority.
func (c *Client) ForceActive(ctx context.Context) (*ForceActiveResult, error) {
	var out ForceActiveResult
	if err := c.do(ctx, http.MethodPost, "/api/cluster/force-active", map[string]bool{"accept_loss": true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
