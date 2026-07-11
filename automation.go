package client

import (
	"context"
	"encoding/json"
	"fmt"
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

func (a *AutomationClient) PublishResult(ctx context.Context, runID string, status string, result map[string]interface{}, errMsg string) error {
	if status == "" {
		status = "completed"
	}
	payload := map[string]interface{}{
		"status": status,
		"result": result,
		"error":  errMsg,
	}
	return a.client.do(ctx, "POST", fmt.Sprintf("/apps/automation/api/runs/%s/complete", runID), payload, nil)
}

func (a *AutomationClient) WaitForRun(ctx context.Context, runID string, opts ...WaitOpts) (*AutomationRun, error) {
	var o WaitOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.Timeout == 0 {
		o.Timeout = time.Hour
	}
	if o.PollInterval == 0 {
		o.PollInterval = 2 * time.Second
	}

	if o.UsePubSub && a.client.baseURL != "" {
		run, err := a.waitViaPubSub(ctx, runID, o.Timeout)
		if err == nil {
			return run, nil
		}
	}

	return a.waitViaPoll(ctx, runID, o.Timeout, o.PollInterval)
}

type WaitOpts struct {
	Timeout      time.Duration
	PollInterval time.Duration
	UsePubSub    bool
}

func (a *AutomationClient) waitViaPoll(ctx context.Context, runID string, timeout, interval time.Duration) (*AutomationRun, error) {
	deadline := time.After(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		run, err := a.GetRun(ctx, runID)
		if err != nil {
			return nil, err
		}
		if run.Status != "running" && run.Status != "pending" {
			return run, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, fmt.Errorf("timeout waiting for run %s", runID)
		case <-ticker.C:
		}
	}
}

func (a *AutomationClient) waitViaPubSub(ctx context.Context, runID string, timeout time.Duration) (*AutomationRun, error) {
	wsURL := a.client.baseURL
	wsURL = "ws" + wsURL[4:]
	wsURL += "/apps/cache/ws/localitas_automations"

	ps := NewPubSubWS(wsURL, a.client.token)
	defer ps.Close()

	done := make(chan *AutomationRun, 1)
	ps.Subscribe("completions", "sdk-wait-"+runID, func(msg PubSubMessage) {
		var payload struct {
			RunID  string                 `json:"run_id"`
			Status string                 `json:"status"`
			Result map[string]interface{} `json:"result"`
			Error  string                 `json:"error"`
		}
		if json.Unmarshal([]byte(msg.Value), &payload) == nil && payload.RunID == runID {
			run := &AutomationRun{
				ID:     runID,
				Status: payload.Status,
				Result: payload.Result,
				Error:  payload.Error,
			}
			select {
			case done <- run:
			default:
			}
		}
	})

	select {
	case run := <-done:
		return run, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, fmt.Errorf("pubsub timeout waiting for run %s", runID)
	}
}
