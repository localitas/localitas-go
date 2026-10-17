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

type AsyncWorkFunc func(ctx context.Context) (map[string]interface{}, error)

func (c *Client) RunAsync(w http.ResponseWriter, r *http.Request, work AsyncWorkFunc) bool {
	if c == nil {
		return false
	}
	runID := r.Header.Get(AutomationRunIDHeader)
	if runID == "" {
		return false
	}

	go func() {
		start := time.Now().UTC()
		result, err := runAsyncWork(work)
		duration := time.Since(start).Milliseconds()

		if result == nil {
			result = make(map[string]interface{})
		}
		result["duration_ms"] = duration

		status := "completed"
		errMsg := ""
		if err != nil {
			status = "failed"
			errMsg = err.Error()
		}

		c.publishCompletion(runID, status, result, errMsg)
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"run_id": runID})
	return true
}

// runAsyncWork runs work, turning a panic into the run's error: the work runs
// on its own goroutine, where a panic would otherwise end the process.
func runAsyncWork(work AsyncWorkFunc) (result map[string]interface{}, err error) {
	defer func() {
		if p := recover(); p != nil {
			result, err = nil, fmt.Errorf("panic: %v", p)
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
func (c *Client) publishCompletion(runID, status string, result map[string]interface{}, errMsg string) {
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
