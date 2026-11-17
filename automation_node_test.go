package client

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A local_only automation names the node it runs on: Create sends it, and
// the automation core returns says it. One naming no node sends none.
func TestCreate_ALocalOnlyAutomationNamesItsNode(t *testing.T) {
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = append(sent, string(b))
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"auto_1","name":"scan","node_id":"node-2"}`)) //nolint:errcheck
	}))
	defer srv.Close()
	c := New(srv.URL).WithToken("t")

	scan := HTTPNode("scan", StrategyLocalOnly, HTTPNodeSettings{URL: "/apps/filesystem/api/ops/index-local"})
	got, err := c.Automation().Create(t.Context(), CreateAutomationRequest{Name: "scan", DAGConfig: DAGConfig{Nodes: []DAGNode{scan}}, TriggerType: "manual", NodeID: "node-2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent[0], `"node_id":"node-2"`) {
		t.Fatalf("Create sent %s, want the node", sent[0])
	}
	if got.NodeID != "node-2" {
		t.Fatalf("the created automation runs on %q, want node-2", got.NodeID)
	}

	every := HTTPNode("prune", StrategyAllNodes, HTTPNodeSettings{URL: "/apps/x/api/prune"})
	if _, err := c.Automation().Create(t.Context(), CreateAutomationRequest{Name: "prune", DAGConfig: DAGConfig{Nodes: []DAGNode{every}}, TriggerType: "manual"}); err != nil {
		t.Fatal(err)
	}
	var top struct {
		NodeID *string `json:"node_id"`
	}
	if err := json.Unmarshal([]byte(sent[1]), &top); err != nil || top.NodeID != nil {
		t.Fatalf("an automation naming no node sent %s", sent[1])
	}
}

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
