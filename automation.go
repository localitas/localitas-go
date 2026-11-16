package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type AutomationClient struct {
	client *Client
}

func (c *Client) Automation() *AutomationClient {
	return &AutomationClient{client: c}
}

type AutomationRun struct {
	ID           string                 `json:"id"`
	AutomationID string                 `json:"automation_id"`
	Status       string                 `json:"status"`
	TriggeredBy  string                 `json:"triggered_by"`
	StartedAt    *time.Time             `json:"started_at,omitempty"`
	CompletedAt  *time.Time             `json:"completed_at,omitempty"`
	DurationMs   int64                  `json:"duration_ms,omitempty"`
	Error        string                 `json:"error,omitempty"`
	Result       map[string]interface{} `json:"result,omitempty"`
	// CompletionChannel is the automations cache channel core announces
	// this run's completion on: its owner's own channel, which only they
	// read. Empty from a core that announces every run on "completions".
	CompletionChannel string `json:"completion_channel,omitempty"`
}

type Automation struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	TriggerType string `json:"trigger_type,omitempty"`
	IsEnabled   bool   `json:"is_enabled,omitempty"`
}

// Where a node runs.
const (
	// StrategyRaftLeader runs the node on one node, the leader.
	StrategyRaftLeader = "raft_leader"
	// StrategyAnyNode runs the node on one node.
	StrategyAnyNode = "any_node"
	// StrategyAllNodes runs the node on every node, for per-node work.
	StrategyAllNodes = "all_nodes"
	// StrategyLocalOnly runs the node on every node, each calling its own.
	StrategyLocalOnly = "local_only"
)

// DAGNode is one node of an automation. Its settings are its type's, as
// JSON: build an http-api node with HTTPNode. Core refuses a key the
// node's type does not have.
type DAGNode struct {
	NodeID            string          `json:"node_id"`
	NodeType          string          `json:"node_type"`
	ExecutionStrategy string          `json:"execution_strategy"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

// HTTPNodeSettings is an http-api node's call. Unset fields take core's
// defaults: method POST, timeout 30s, 3 retries, backoff 2s ×2 up to 30s,
// expected status 200.
type HTTPNodeSettings struct {
	URL               string            `json:"url"`
	Method            string            `json:"method,omitempty"`
	ContentType       string            `json:"content_type,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	Body              json.RawMessage   `json:"body,omitempty"`
	TimeoutMs         int               `json:"timeout_ms,omitempty"`
	MaxRetries        *int              `json:"max_retries,omitempty"`
	BackoffMs         *int              `json:"backoff_ms,omitempty"`
	BackoffMultiplier float64           `json:"backoff_multiplier,omitempty"`
	MaxBackoffMs      int               `json:"max_backoff_ms,omitempty"`
	ExpectedStatus    int               `json:"expected_status,omitempty"`
	AsyncTimeoutMs    int               `json:"async_timeout_ms,omitempty"`
	VaultCredentialID string            `json:"vault_credential_id,omitempty"`
	VaultAuthFormat   string            `json:"vault_auth_format,omitempty"`
	VaultHeaderName   string            `json:"vault_header_name,omitempty"`
	VaultHeaderPrefix string            `json:"vault_header_prefix,omitempty"`
}

// Count is n as an optional setting (MaxRetries, BackoffMs), where 0 is a
// value and not "use the default".
func Count(n int) *int { return &n }

// HTTPNode is an http-api node that makes the call s describes.
func HTTPNode(id, strategy string, s HTTPNodeSettings) DAGNode {
	// A struct of strings, ints and raw JSON always encodes.
	meta, _ := json.Marshal(s)
	return DAGNode{NodeID: id, NodeType: "http-api", ExecutionStrategy: strategy, Metadata: meta}
}

type DAGConfig struct {
	DAGID       string    `json:"dag_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Nodes       []DAGNode `json:"nodes"`
}

type PeriodicTrigger struct {
	Schedule string `json:"schedule"`
	Timezone string `json:"timezone"`
}

type TriggerConfig struct {
	Periodic *PeriodicTrigger `json:"periodic,omitempty"`
}

type CreateAutomationRequest struct {
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	DAGConfig     DAGConfig     `json:"dag_config"`
	TriggerType   string        `json:"trigger_type"`
	TriggerConfig TriggerConfig `json:"trigger_config"`
	IsEnabled     bool          `json:"is_enabled"`
}

func (a *AutomationClient) List(ctx context.Context) ([]Automation, error) {
	var resp struct {
		Automations []Automation `json:"automations"`
	}
	if err := a.client.do(ctx, "GET", "/apps/automation/api/automations", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Automations, nil
}

func (a *AutomationClient) Create(ctx context.Context, req CreateAutomationRequest) (*Automation, error) {
	var out Automation
	if err := a.client.do(ctx, "POST", "/apps/automation/api/automations", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *AutomationClient) Trigger(ctx context.Context, automationID string) (*AutomationRun, error) {
	var run AutomationRun
	err := a.client.do(ctx, "POST", fmt.Sprintf("/apps/automation/api/automations/%s/trigger", automationID), nil, &run)
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (a *AutomationClient) GetRun(ctx context.Context, runID string) (*AutomationRun, error) {
	var run AutomationRun
	err := a.client.do(ctx, "GET", fmt.Sprintf("/apps/automation/api/runs/%s", runID), nil, &run)
	if err != nil {
		return nil, err
	}
	return &run, nil
}

// runOutcome is what a run's completion carries.
type runOutcome struct {
	Status string `json:"status"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error"`
}

// PublishResult completes a run with status ("completed" when empty), the
// work's result (a value that encodes to a JSON object, such as a struct, or
// nil) and, for a failure, its error.
func (a *AutomationClient) PublishResult(ctx context.Context, runID string, status string, result any, errMsg string) error {
	if status == "" {
		status = "completed"
	}
	return a.client.do(ctx, "POST", fmt.Sprintf("/apps/automation/api/runs/%s/complete", runID), runOutcome{Status: status, Result: result, Error: errMsg}, nil)
}

// The automations cache, and the channel a core that predates per-owner
// completion channels announces every finished run on.
const (
	automationsCache   = "localitas_automations"
	completionsChannel = "completions"
)

// completionChannel is where run's completion is announced.
func completionChannel(run *AutomationRun) string {
	if run.CompletionChannel != "" {
		return run.CompletionChannel
	}
	return completionsChannel
}

// WaitOpts bounds WaitForRun.
type WaitOpts struct {
	// Timeout is how long to wait for the run; an hour when zero.
	Timeout time.Duration
}

// runFinished reports whether a run has its outcome.
func runFinished(run *AutomationRun) bool {
	return run.Status != "running" && run.Status != "pending"
}

// WaitForRun returns the run once it has finished, without polling it.
//
// Every finished run is announced once, after its outcome is recorded, on
// the channel its first read names (its owner's own completions channel;
// only the owner can read the run, so the caller is the owner). WaitForRun
// reads the run to learn the channel, notes the channel's last message,
// then reads the run again; the read is linearizable, so a run whose
// completion is at or before the noted message is read finished. A run
// still going is waited for by watching the channel after the noted
// message: its completion comes after it, so it cannot be missed. A run
// that does not exist, or is not the caller's, is the read's error.
func (a *AutomationClient) WaitForRun(ctx context.Context, runID string, opts ...WaitOpts) (*AutomationRun, error) {
	timeout := time.Hour
	if len(opts) > 0 && opts[0].Timeout > 0 {
		timeout = opts[0].Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	run, err := a.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if runFinished(run) {
		return run, nil
	}
	channel := completionChannel(run)
	noted, err := a.client.Cache(automationsCache).PubSub(channel, PubSubOpts{}).LastSeq(ctx)
	if err != nil {
		return nil, fmt.Errorf("note the completions channel: %w", err)
	}
	if run, err = a.GetRun(ctx, runID); err != nil {
		return nil, err
	}
	if runFinished(run) {
		return run, nil
	}

	ps := NewPubSubWS("ws"+strings.TrimPrefix(a.client.baseURL, "http")+"/apps/cache/ws/"+automationsCache, a.client.bearer(ctx))
	defer ps.Close()
	finished := make(chan struct{}, 1)
	ps.Watch(channel, noted, func(msg PubSubMessage) {
		var announced struct {
			RunID string `json:"run_id"`
		}
		if json.Unmarshal([]byte(msg.Value), &announced) == nil && announced.RunID == runID {
			select {
			case finished <- struct{}{}:
			default:
			}
		}
	})
	select {
	case <-finished:
		return a.GetRun(ctx, runID)
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for run %s: %w", runID, ctx.Err())
	}
}
