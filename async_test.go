package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type publishedCompletion struct {
	Path   string          `json:"-"`
	Status string          `json:"status"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

// scanSummary stands for an app's own result type.
type scanSummary struct {
	Processed int    `json:"processed"`
	Label     string `json:"label"`
}

// completionServer records each completion it accepts. statuses lists the
// HTTP status of successive publishes; once used up it answers 200.
func completionServer(t *testing.T, statuses ...int) (*Client, <-chan publishedCompletion, *atomic.Int32) {
	t.Helper()
	got := make(chan publishedCompletion, 16)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		if n <= len(statuses) && statuses[n-1] != http.StatusOK {
			http.Error(w, "unavailable", statuses[n-1])
			return
		}
		var p publishedCompletion
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Errorf("decode completion: %v", err)
		}
		p.Path = r.URL.Path
		got <- p
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL), got, &calls
}

func waitCompletion(t *testing.T, got <-chan publishedCompletion) publishedCompletion {
	t.Helper()
	select {
	case p := <-got:
		return p
	case <-time.After(10 * time.Second):
		t.Fatal("no completion published")
		return publishedCompletion{}
	}
}

func startAsync[T any](t *testing.T, c *Client, runID string, work func(ctx context.Context) (T, error)) {
	t.Helper()
	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set(AutomationRunIDHeader, runID)
	w := httptest.NewRecorder()
	if !RunAsync(c, w, req, work) {
		t.Fatal("expected RunAsync to handle the request")
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", w.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil || resp["run_id"] != runID {
		t.Fatalf("202 body = %v (%v), want run_id %s", resp, err, runID)
	}
}

// The work's own result type is what the run records, exactly: no field is
// added (core times the run itself).
func TestRunAsync_PublishesTheWorksTypedResult(t *testing.T) {
	c, got, _ := completionServer(t)
	startAsync(t, c, "run-123", func(ctx context.Context) (scanSummary, error) {
		return scanSummary{Processed: 42, Label: "typed"}, nil
	})

	p := waitCompletion(t, got)
	if p.Path != "/apps/automation/api/runs/run-123/complete" || p.Status != "completed" {
		t.Fatalf("completion = %+v", p)
	}
	if string(p.Result) != `{"processed":42,"label":"typed"}` {
		t.Fatalf("published result %s, want the work's result as it encodes", p.Result)
	}
}

func TestRunAsync_PublishesAListResult(t *testing.T) {
	c, got, _ := completionServer(t)
	startAsync(t, c, "run-list", func(ctx context.Context) ([]string, error) {
		return []string{"inbox", "sent"}, nil
	})

	p := waitCompletion(t, got)
	if p.Status != "completed" || string(p.Result) != `["inbox","sent"]` {
		t.Fatalf("completion = %+v, result %s", p, p.Result)
	}
}

func TestRunAsync_UnencodableResultPublishesFailure(t *testing.T) {
	c, got, _ := completionServer(t)
	startAsync(t, c, "run-chan", func(ctx context.Context) (chan int, error) {
		return make(chan int), nil
	})

	p := waitCompletion(t, got)
	if p.Status != "failed" || p.Error != "result could not be encoded: json: unsupported type: chan int" {
		t.Fatalf("completion = %+v, want failed saying the result could not be encoded", p)
	}
}

func TestRunAsync_WithoutRunID_ReturnsFalse(t *testing.T) {
	c, _, calls := completionServer(t)
	req := httptest.NewRequest("POST", "/test", nil)
	w := httptest.NewRecorder()
	if RunAsync(c, w, req, func(ctx context.Context) (scanSummary, error) { return scanSummary{}, nil }) {
		t.Fatal("expected RunAsync to not handle request without run ID header")
	}
	if calls.Load() != 0 {
		t.Fatal("no completion may be published without a run")
	}
}

func TestRunAsync_WorkErrorPublishesFailure(t *testing.T) {
	c, got, _ := completionServer(t)
	startAsync(t, c, "run-fail", func(ctx context.Context) (scanSummary, error) {
		return scanSummary{Processed: 1}, fmt.Errorf("something broke")
	})

	p := waitCompletion(t, got)
	if p.Status != "failed" || p.Error != "something broke" {
		t.Fatalf("completion = %+v, want failed with the work's error", p)
	}
	if len(p.Result) != 0 && string(p.Result) != "null" {
		t.Fatalf("a failed run published a result: %s", p.Result)
	}
}

// Work runs on its own goroutine, where a panic would end the whole app:
// it must become the run's failure instead.
func TestRunAsync_PanicBecomesFailure(t *testing.T) {
	c, got, _ := completionServer(t)
	startAsync(t, c, "run-panic", func(ctx context.Context) (scanSummary, error) {
		panic("boom")
	})

	p := waitCompletion(t, got)
	if p.Status != "failed" || p.Error != "panic: boom" {
		t.Fatalf("completion = %+v, want failed with the panic", p)
	}
}

// A publish that fails for a reason that may pass (the automation app
// unavailable while leadership moves) is retried until it lands.
func TestRunAsync_RetriesFailedPublish(t *testing.T) {
	c, got, calls := completionServer(t, http.StatusServiceUnavailable, http.StatusBadGateway)
	startAsync(t, c, "run-retry", func(ctx context.Context) (scanSummary, error) {
		return scanSummary{}, nil
	})

	p := waitCompletion(t, got)
	if p.Status != "completed" {
		t.Fatalf("completion = %+v", p)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("publish attempts = %d, want 3", n)
	}
}

// A publish the server refuses (the run does not exist) is not retried.
func TestRunAsync_DoesNotRetryRefusedPublish(t *testing.T) {
	c, _, calls := completionServer(t, http.StatusNotFound, http.StatusNotFound)
	startAsync(t, c, "run-gone", func(ctx context.Context) (scanSummary, error) {
		return scanSummary{}, nil
	})

	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(publishFirstBackoff * 3)
	if n := calls.Load(); n != 1 {
		t.Fatalf("publish attempts = %d, want 1 (a 404 is final)", n)
	}
}

func rawCompletionServer(t *testing.T) (*Client, <-chan string) {
	t.Helper()
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read completion: %v", err)
		}
		got <- string(b)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL), got
}

func waitRawCompletion(t *testing.T, got <-chan string) string {
	t.Helper()
	select {
	case b := <-got:
		return b
	case <-time.After(10 * time.Second):
		t.Fatal("no completion published")
		return ""
	}
}

func TestRunAsync_CompletionWireBytes(t *testing.T) {
	cases := []struct {
		name  string
		start func(t *testing.T, c *Client)
		want  string
	}{
		{"struct", func(t *testing.T, c *Client) {
			startAsync(t, c, "run-1", func(ctx context.Context) (scanSummary, error) {
				return scanSummary{Processed: 42, Label: "typed"}, nil
			})
		}, `{"status":"completed","result":{"processed":42,"label":"typed"},"error":""}`},
		{"nil pointer", func(t *testing.T, c *Client) {
			startAsync(t, c, "run-2", func(ctx context.Context) (*scanSummary, error) { return nil, nil })
		}, `{"status":"completed","result":null,"error":""}`},
		{"error", func(t *testing.T, c *Client) {
			startAsync(t, c, "run-3", func(ctx context.Context) (scanSummary, error) {
				return scanSummary{Processed: 1}, fmt.Errorf("something broke")
			})
		}, `{"status":"failed","error":"something broke"}`},
		{"slice", func(t *testing.T, c *Client) {
			startAsync(t, c, "run-4", func(ctx context.Context) ([]string, error) { return []string{"a", "b"}, nil })
		}, `{"status":"completed","result":["a","b"],"error":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, got := rawCompletionServer(t)
			tc.start(t, c)
			if b := waitRawCompletion(t, got); b != tc.want {
				t.Fatalf("completion body\n got %s\nwant %s", b, tc.want)
			}
		})
	}
}

func TestComplete_WireBytes(t *testing.T) {
	c, got := rawCompletionServer(t)
	call := AsyncCall{RunID: "run-9", IdempotencyKey: "run-9/a/1"}
	if err := c.Automation().Complete(context.Background(), call, "", nil, ""); err != nil {
		t.Fatal(err)
	}
	if b, want := waitRawCompletion(t, got), `{"status":"completed","error":"","idempotency_key":"run-9/a/1"}`; b != want {
		t.Fatalf("completion body\n got %s\nwant %s", b, want)
	}
}

func TestAutomationRun_DecodesAnyJSONResult(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"object", `{"id":"r1","status":"completed","result":{"answer":42,"label":"done"}}`, `{"answer":42,"label":"done"}`},
		{"list", `{"id":"r1","status":"completed","result":["a","b"]}`, `["a","b"]`},
		{"none", `{"id":"r1","status":"completed"}`, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			run, err := New(srv.URL).Automation().GetRun(context.Background(), "r1")
			if err != nil {
				t.Fatal(err)
			}
			if string(run.Result) != tc.want {
				t.Fatalf("result = %s, want %s", run.Result, tc.want)
			}
		})
	}
}

func TestPublishCompletion_GivesUpAfterLimitedAttempts(t *testing.T) {
	statuses := make([]int, publishAttempts+5)
	for i := range statuses {
		statuses[i] = http.StatusServiceUnavailable
	}
	c, _, calls := completionServer(t, statuses...)
	start := time.Now()
	c.publishCompletion(AsyncCall{RunID: "run-down"}, "completed", nil, "")
	if n := calls.Load(); n != publishAttempts {
		t.Fatalf("publish attempts = %d, want %d", n, publishAttempts)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("giving up took %s", elapsed)
	}
}
