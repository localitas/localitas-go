package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"
)

const AutomationRunIDHeader = "Localitas-Automation-Run-ID"

// RunAsync runs work after answering 202 when core's automation called with
// a run ID, and publishes its outcome to the run: the work's own result type
// on success (any value that encodes to JSON: object, list, string, number),
// its error on failure, or a failure saying the result could not be encoded.
// The completion echoes the call's Idempotency-Key, so core ends the try it
// called and no other. Core times the run itself. Without a run ID it
// returns false and the handler answers the call itself.
func RunAsync[T any](c *Client, w http.ResponseWriter, r *http.Request, work func(ctx context.Context) (T, error)) bool {
	if c == nil {
		return false
	}
	call := AsyncCallFrom(r)
	if call.RunID == "" {
		return false
	}

	go func() {
		result, err := runAsyncWork(work)
		if err != nil {
			c.publishCompletion(call, "failed", nil, err.Error())
			return
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			c.publishCompletion(call, "failed", nil, fmt.Sprintf("result could not be encoded: %v", err))
			return
		}
		c.publishCompletion(call, "completed", encoded, "")
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"run_id": call.RunID})
	return true
}

// runAsyncWork runs work, turning a panic into the run's error: the work runs
// on its own goroutine, where a panic would otherwise end the process.
func runAsyncWork[T any](work func(ctx context.Context) (T, error)) (result T, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return work(context.Background())
}

const (
	publishAttempts     = 6
	publishFirstBackoff = 250 * time.Millisecond
)

// publishCompletion reports a run's outcome, retrying a publish that failed
// for a reason that may pass (a 5xx, a dropped connection): a run whose
// completion never arrives stays running until its caller times out.
func (c *Client) publishCompletion(call AsyncCall, status string, result json.RawMessage, errMsg string) {
	runID := call.RunID
	backoff := publishFirstBackoff
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := c.Automation().Complete(ctx, call, status, result, errMsg)
		cancel()
		if err == nil {
			return
		}
		var apiErr *APIError
		retryable := !errors.As(err, &apiErr) || apiErr.StatusCode >= 500 || apiErr.StatusCode == http.StatusTooManyRequests
		if !retryable || attempt == publishAttempts {
			log.Printf("async run %s: completion (%s) not published after %d attempts: %v", runID, status, attempt, err)
			return
		}
		time.Sleep(backoff)
		backoff *= 2
	}
}
