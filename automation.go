package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	// NodeID is the node a local_only automation runs on.
	NodeID string `json:"node_id,omitempty"`
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

// Create stores the automation req describes (or replaces the caller's
// automation of that name), after checking it as core will (Validate).
func (a *AutomationClient) Create(ctx context.Context, req CreateAutomationRequest) (*Automation, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
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
	Status         string `json:"status"`
	Result         any    `json:"result,omitempty"`
	Error          string `json:"error"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// IdempotencyKeyHeader names the try core calls an app for:
// "<run>/<task>/<try>", the same on every attempt of that call.
const IdempotencyKeyHeader = "Idempotency-Key"

// AsyncCall is the automation call an app answers asynchronously: its run,
// and the Idempotency-Key naming the task's try, when core sent one.
type AsyncCall struct {
	RunID          string
	IdempotencyKey string
}

// AsyncCallFrom is the automation call r is, from its headers; RunID is
// empty when r is not one.
func AsyncCallFrom(r *http.Request) AsyncCall {
	return AsyncCall{RunID: r.Header.Get(AutomationRunIDHeader), IdempotencyKey: r.Header.Get(IdempotencyKeyHeader)}
}

// PublishResult completes run runID with status ("completed" when empty),
// the work's result (a value that encodes to a JSON object, such as a
// struct, or nil) and, for a failure, its error. It names no try: prefer
// Complete with the AsyncCall, which core restricts to the try it called.
func (a *AutomationClient) PublishResult(ctx context.Context, runID string, status string, result any, errMsg string) error {
	return a.Complete(ctx, AsyncCall{RunID: runID}, status, result, errMsg)
}

// Complete completes call as PublishResult does, echoing its
// Idempotency-Key, so core ends that try and refuses the completion (409)
// once the try no longer waits for it: a zombie's late result is not
// recorded.
func (a *AutomationClient) Complete(ctx context.Context, call AsyncCall, status string, result any, errMsg string) error {
	if status == "" {
		status = "completed"
	}
	return a.client.do(ctx, "POST", "/apps/automation/api/runs/"+url.PathEscape(call.RunID)+"/complete",
		runOutcome{Status: status, Result: result, Error: errMsg, IdempotencyKey: call.IdempotencyKey}, nil)
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

// runFinished reports whether a run has its outcome: completed or failed. A
// run queued, running or up_for_retry (waiting to be tried again) has not.
func runFinished(run *AutomationRun) bool {
	return run.Status == "completed" || run.Status == "failed"
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

	finished := make(chan struct{}, 1)
	ps := a.client.Cache(automationsCache).Watch(channel, noted, func(msg PubSubMessage) {
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
	defer ps.Close()
	select {
	case <-finished:
		return a.GetRun(ctx, runID)
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for run %s: %w", runID, ctx.Err())
	}
}
