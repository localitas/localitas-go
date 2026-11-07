package client

import (
	"encoding/json"
	"testing"
)

// An http-api node's metadata is the settings as core reads them: unset
// fields are left out (core's defaults), an explicit 0 retries is kept,
// and the body is the caller's JSON.
func TestHTTPNode_Metadata(t *testing.T) {
	n := HTTPNode("sync", StrategyRaftLeader, HTTPNodeSettings{
		URL:        "http://app/api/sync",
		Body:       json.RawMessage(`{"id":9007199254740993}`),
		TimeoutMs:  120000,
		MaxRetries: Count(0),
	})
	if n.NodeType != "http-api" || n.ExecutionStrategy != "raft_leader" {
		t.Fatalf("node %+v", n)
	}
	want := `{"url":"http://app/api/sync","body":{"id":9007199254740993},"timeout_ms":120000,"max_retries":0}`
	if string(n.Metadata) != want {
		t.Fatalf("metadata\n got %s\nwant %s", n.Metadata, want)
	}
	wire, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"node_id":"sync","node_type":"http-api","execution_strategy":"raft_leader","metadata":` + want + `}`; string(wire) != want {
		t.Fatalf("wire\n got %s\nwant %s", wire, want)
	}
}
