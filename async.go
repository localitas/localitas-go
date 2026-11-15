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
// on success (it must encode to a JSON object, as a struct does), its error
// on failure. Core times the run itself. Without a run ID it returns false
// and the handler answers the call itself.
func RunAsync[T any](c *Client, w http.ResponseWriter, r *http.Request, work func(ctx context.Context) (T, error)) bool {
	if c == nil {
		return false
	}
	runID := r.Header.Get(AutomationRunIDHeader)
	if runID == "" {
		return false
	}

	go func() {
		result, err := runAsyncWork(work)
		if err != nil {
			c.publishCompletion(runID, "failed", nil, err.Error())
			return
		}
		c.publishCompletion(runID, "completed", result, "")
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"run_id": runID})
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
func (c *Client) publishCompletion(runID, status string, result any, errMsg string) {
	backoff := publishFirstBackoff
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := c.Automation().PublishResult(ctx, runID, status, result, errMsg)
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
