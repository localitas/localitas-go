package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func newTestClientForAsync(t *testing.T) (*Client, *[]map[string]interface{}) {
	t.Helper()
	var mu sync.Mutex
	var completions []map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		completions = append(completions, payload)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	return c, &completions
}

func TestRunAsync_WithRunID_Returns202(t *testing.T) {
	c, completions := newTestClientForAsync(t)

	work := func(ctx context.Context) (map[string]interface{}, error) {
		return map[string]interface{}{"processed": float64(42)}, nil
	}

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set(AutomationRunIDHeader, "run-123")
	w := httptest.NewRecorder()

	handled := c.RunAsync(w, req, work)
	if !handled {
		t.Fatal("expected RunAsync to handle the request")
	}

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["run_id"] != "run-123" {
		t.Fatalf("expected run_id=run-123, got %s", resp["run_id"])
	}

	time.Sleep(200 * time.Millisecond)

	if len(*completions) == 0 {
		t.Fatal("expected completion to be published")
	}
	payload := (*completions)[0]
	if payload["status"] != "completed" {
		t.Fatalf("expected status=completed, got %v", payload["status"])
	}
}

func TestRunAsync_WithoutRunID_ReturnsFalse(t *testing.T) {
	c, _ := newTestClientForAsync(t)

	work := func(ctx context.Context) (map[string]interface{}, error) {
		return map[string]interface{}{}, nil
	}

	req := httptest.NewRequest("POST", "/test", nil)
	w := httptest.NewRecorder()

	handled := c.RunAsync(w, req, work)
	if handled {
		t.Fatal("expected RunAsync to not handle request without run ID header")
	}
}

func TestRunAsync_WorkError_Returns202AndPublishesFailure(t *testing.T) {
	c, completions := newTestClientForAsync(t)

	work := func(ctx context.Context) (map[string]interface{}, error) {
		return nil, fmt.Errorf("something broke")
	}

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set(AutomationRunIDHeader, "run-fail")
	w := httptest.NewRecorder()

	handled := c.RunAsync(w, req, work)
	if !handled {
		t.Fatal("expected RunAsync to handle the request")
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}

	time.Sleep(200 * time.Millisecond)

	if len(*completions) == 0 {
		t.Fatal("expected completion to be published")
	}
	payload := (*completions)[0]
	if payload["status"] != "failed" {
		t.Fatalf("expected status=failed, got %v", payload["status"])
	}
	if payload["error"] != "something broke" {
		t.Fatalf("expected error message, got %v", payload["error"])
	}
}
