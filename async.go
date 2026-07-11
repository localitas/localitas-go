package client

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

const AutomationRunIDHeader = "Localitas-Automation-Run-ID"

type AsyncWorkFunc func(ctx context.Context) (map[string]interface{}, error)

func RunAsync(w http.ResponseWriter, r *http.Request, c *Client, work AsyncWorkFunc) bool {
	runID := r.Header.Get(AutomationRunIDHeader)
	if runID == "" {
		return false
	}

	go func() {
		start := time.Now().UTC()
		result, err := work(context.Background())
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

		if c != nil {
			c.Automation().PublishResult(context.Background(), runID, status, result, errMsg)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"run_id": runID})
	return true
}
