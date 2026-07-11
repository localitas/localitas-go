package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunAsync_WithRunID_Returns202(t *testing.T) {
	work := func(ctx context.Context) (map[string]interface{}, error) {
		return map[string]interface{}{"processed": 42}, nil
	}

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set(AutomationRunIDHeader, "run-123")
	w := httptest.NewRecorder()

	handled := RunAsync(w, req, nil, work)
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
}

func TestRunAsync_WithoutRunID_ReturnsFalse(t *testing.T) {
	work := func(ctx context.Context) (map[string]interface{}, error) {
		return map[string]interface{}{}, nil
	}

	req := httptest.NewRequest("POST", "/test", nil)
	w := httptest.NewRecorder()

	handled := RunAsync(w, req, nil, work)
	if handled {
		t.Fatal("expected RunAsync to not handle request without run ID header")
	}
}

func TestRunAsync_WorkError_Returns202(t *testing.T) {
	work := func(ctx context.Context) (map[string]interface{}, error) {
		return nil, fmt.Errorf("something broke")
	}

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set(AutomationRunIDHeader, "run-fail")
	w := httptest.NewRecorder()

	handled := RunAsync(w, req, nil, work)
	if !handled {
		t.Fatal("expected RunAsync to handle the request")
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}
}
