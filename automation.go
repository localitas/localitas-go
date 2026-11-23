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
	ID           string     `json:"id"`
	AutomationID string     `json:"automation_id"`
	Status       string     `json:"status"`
	TriggeredBy  string     `json:"triggered_by"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	DurationMs   int64      `json:"duration_ms,omitempty"`
	Error        string     `json:"error,omitempty"`
	// Result is the run's result, any JSON value; nil when it has none.
	Result json.RawMessage `json:"result,omitempty"`
	// CompletionChannel is the automations cache channel core hints this
	// run's end on, once and best-effort: its owner's own channel, which
	// only they read. WaitForRun does not depend on the hint.
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
	Status         string          `json:"status"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          string          `json:"error"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
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
// the work's encoded result (any JSON value: object, list, string, number;
// nil for none) and, for a failure, its error. It names no try: prefer
// Complete with the AsyncCall, which core restricts to the try it called.
func (a *AutomationClient) PublishResult(ctx context.Context, runID string, status string, result json.RawMessage, errMsg string) error {
	return a.Complete(ctx, AsyncCall{RunID: runID}, status, result, errMsg)
}

// Complete completes call as PublishResult does, echoing its
// Idempotency-Key, so core ends that try and refuses the completion (409)
// once the try no longer waits for it: a zombie's late result is not
// recorded.
func (a *AutomationClient) Complete(ctx context.Context, call AsyncCall, status string, result json.RawMessage, errMsg string) error {
	if status == "" {
		status = "completed"
	}
	return a.client.do(ctx, "POST", "/apps/automation/api/runs/"+url.PathEscape(call.RunID)+"/complete",
		runOutcome{Status: status, Result: result, Error: errMsg, IdempotencyKey: call.IdempotencyKey}, nil)
}

// WaitOpts bounds WaitForRun.
type WaitOpts struct {
	// Timeout is how long to wait for the run; an hour when zero.
	Timeout time.Duration
}

// runWaitHold is how long core holds one wait for a run before answering
// that it has not finished: under core's 30 s write timeout, which would
// cut a longer one.
const runWaitHold = 25 * time.Second

// runWait is core's answer to GET /runs/{id}/wait: the run as core last
// read it, and whether it has finished (completed or failed).
type runWait struct {
	Finished bool          `json:"finished"`
	Run      AutomationRun `json:"run"`
}

// WaitForRun returns the run once it has finished, without polling it.
//
// It asks core's wait for the run, a long poll any node answers from the
// committed run: at once when the run has finished, else as soon as a
// commit ends it, or after core's hold that it has not, when it asks
// again. It depends on nothing being published: a node that records the
// run's end and dies before telling anyone still leaves the end committed.
// A core that cannot answer yet (503, or its process not there) is waited
// for (WaitReady) and asked again. A run that does not exist, or is not the
// caller's, is core's answer as an *APIError.
func (a *AutomationClient) WaitForRun(ctx context.Context, runID string, opts ...WaitOpts) (*AutomationRun, error) {
	timeout := time.Hour
	if len(opts) > 0 && opts[0].Timeout > 0 {
		timeout = opts[0].Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		hold := runWaitHold
		if deadline, ok := ctx.Deadline(); ok {
			hold = min(hold, time.Until(deadline))
		}
		if hold <= 0 {
			return nil, fmt.Errorf("wait for run %s: %w", runID, context.DeadlineExceeded)
		}
		var ans runWait
		path := "/apps/automation/api/runs/" + url.PathEscape(runID) + "/wait?" + url.Values{"timeout": {hold.String()}}.Encode()
		err := a.client.do(ctx, http.MethodGet, path, nil, &ans)
		switch {
		case err == nil && ans.Finished:
			return &ans.Run, nil
		case err == nil:
		case ctx.Err() != nil:
			return nil, fmt.Errorf("wait for run %s: %w", runID, ctx.Err())
		case notReady(err):
			if err := a.client.WaitReady(ctx, PhaseReady); err != nil {
				return nil, fmt.Errorf("wait for run %s: %w", runID, err)
			}
		default:
			return nil, err
		}
	}
}
