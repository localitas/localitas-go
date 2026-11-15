package client

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// CronSpec is an app's cron.json: the jobs core schedules for it. Core reads
// it when it registers the app and keeps the app's jobs in step with it.
type CronSpec struct {
	Jobs []CronJob `json:"jobs"`
}

// CronJob is one scheduled call core makes to the app. Unset fields take
// core's defaults (method POST, timeout 30s, 3 attempts with exponential
// backoff, no notification).
type CronJob struct {
	ID                string            `json:"id"`
	Path              string            `json:"path"`
	Schedule          string            `json:"schedule"`
	Description       string            `json:"description,omitempty"`
	Method            string            `json:"method,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	Body              json.RawMessage   `json:"body,omitempty"`
	Timeout           string            `json:"timeout,omitempty"`
	Retry             *CronRetry        `json:"retry,omitempty"`
	Auth              *CronAuth         `json:"auth,omitempty"`
	NotifyLevel       string            `json:"notify_level,omitempty"`
	ExecutionStrategy string            `json:"execution_strategy,omitempty"`
}

type CronRetry struct {
	MaxAttempts       int     `json:"max_attempts,omitempty"`
	Backoff           string  `json:"backoff,omitempty"`
	InitialDelay      string  `json:"initial_delay,omitempty"`
	MaxDelay          string  `json:"max_delay,omitempty"`
	BackoffMultiplier float64 `json:"backoff_multiplier,omitempty"`
}

// CronAuth has core send a vault credential with the job's call.
type CronAuth struct {
	VaultCredentialID string `json:"vault_credential_id,omitempty"`
	HeaderName        string `json:"header_name,omitempty"`
	HeaderPrefix      string `json:"header_prefix,omitempty"`
}

// JSONBody encodes a job's body from the request type of the handler it
// calls, so a job's body is never shaped apart from what its handler reads.
// It panics on a value that cannot encode: a programming error, caught when
// the spec is built.
func JSONBody(req any) json.RawMessage {
	b, err := json.Marshal(req)
	if err != nil {
		panic(fmt.Sprintf("cron body %T: %v", req, err))
	}
	return b
}

// ServeHTTP serves the spec as the app's cron.json.
func (s CronSpec) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
