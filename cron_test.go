package client

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type folderScan struct {
	Path    string   `json:"path"`
	Exclude []string `json:"exclude,omitempty"`
}

// An app serves its jobs as cron.json; a job's body is the request type of
// the handler it calls, so the two cannot drift apart.
func TestCronSpec_ServesItsJobsAsCronJSON(t *testing.T) {
	spec := CronSpec{Jobs: []CronJob{{
		ID:                "cron:antivirus:scan-folder",
		Path:              "/api/scan-folder",
		Method:            "POST",
		Schedule:          "0 2 * * *",
		Description:       "Scans the data directory",
		ExecutionStrategy: "all_nodes",
		Timeout:           "3600s",
		Body:              JSONBody(folderScan{Path: "/data", Exclude: []string{"raft", "*.log"}}),
		Retry:             &CronRetry{MaxAttempts: 1},
	}}}

	rec := httptest.NewRecorder()
	spec.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/cron.json", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("cron.json: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields()
	var served CronSpec
	if err := dec.Decode(&served); err != nil {
		t.Fatalf("cron.json does not decode as a CronSpec: %v", err)
	}
	if len(served.Jobs) != 1 {
		t.Fatalf("served %d jobs, want 1", len(served.Jobs))
	}
	job := served.Jobs[0]
	if job.ID != "cron:antivirus:scan-folder" || job.Path != "/api/scan-folder" || job.Method != "POST" ||
		job.Schedule != "0 2 * * *" || job.ExecutionStrategy != "all_nodes" || job.Timeout != "3600s" ||
		job.Retry == nil || job.Retry.MaxAttempts != 1 {
		t.Fatalf("served job %+v", job)
	}

	body := json.NewDecoder(bytes.NewReader(job.Body))
	body.DisallowUnknownFields()
	var req folderScan
	if err := body.Decode(&req); err != nil {
		t.Fatalf("the job's body is not the handler's request: %v", err)
	}
	if req.Path != "/data" || len(req.Exclude) != 2 || req.Exclude[1] != "*.log" {
		t.Fatalf("job body %+v", req)
	}
}

// A request type that cannot encode is a programming error, caught when the
// spec is built rather than served as a broken job.
func TestJSONBody_PanicsOnAnUnencodableRequest(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("JSONBody encoded a channel")
		}
	}()
	JSONBody(make(chan int))
}
