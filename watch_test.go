package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"
)

// answer is one answer a waitCore gives: a status and a JSON body.
type answer struct {
	status int
	body   any
}

// waitCore stands in for core's run wait and app changes long polls: each
// question gets the next of its route's answers, in order, and the last
// answer once they run out is held until the question is cancelled. Its
// readiness endpoint answers ready. asks keeps every question's query.
type waitCore struct {
	*httptest.Server
	mu      sync.Mutex
	answers map[string][]answer
	asks    map[string][]url.Values
	heard   chan string
}

func newWaitCore(t *testing.T, answers map[string][]answer) *waitCore {
	t.Helper()
	f := &waitCore{answers: answers, asks: map[string][]url.Values{}, heard: make(chan string, 64)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/system/ready", func(w http.ResponseWriter, r *http.Request) {
		f.record("ready", r)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(fakeReadyAnswer{Phase: "ready", Want: r.URL.Query().Get("phase"), Ready: true})
	})
	mux.HandleFunc("GET /apps/automation/api/runs/{id}/wait", func(w http.ResponseWriter, r *http.Request) {
		f.serve(w, r, "wait:"+r.PathValue("id"))
	})
	mux.HandleFunc("GET /apps/ext/changes", func(w http.ResponseWriter, r *http.Request) {
		f.serve(w, r, "changes")
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *waitCore) record(route string, r *http.Request) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asks[route] = append(f.asks[route], r.URL.Query())
	select {
	case f.heard <- route:
	default:
	}
	return len(f.asks[route])
}

// heardAsks waits until route has been asked n times.
func (f *waitCore) heardAsks(t *testing.T, route string, n int) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for len(f.asked(route)) < n {
		select {
		case <-f.heard:
		case <-timeout:
			t.Fatalf("%s asked %d times, want %d", route, len(f.asked(route)), n)
		}
	}
}

func (f *waitCore) serve(w http.ResponseWriter, r *http.Request, route string) {
	n := f.record(route, r)
	f.mu.Lock()
	answers := f.answers[route]
	f.mu.Unlock()
	if n > len(answers) {
		<-r.Context().Done()
		return
	}
	a := answers[n-1]
	w.Header().Set("Content-Type", "application/json")
	if a.status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(a.status)
	json.NewEncoder(w).Encode(a.body)
}

func (f *waitCore) asked(route string) []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.asks[route]...)
}

func waitAnswer(finished bool, status string) answer {
	return answer{http.StatusOK, map[string]any{"finished": finished, "run": map[string]any{"id": "run_1", "status": status}}}
}

// WaitForRun asks core's wait again on each answer that the run has not
// finished yet, and returns the run core answers finished.
func TestWaitForRun_AsksAgainUntilCoreAnswersTheRunFinished(t *testing.T) {
	core := newWaitCore(t, map[string][]answer{"wait:run_1": {
		waitAnswer(false, "queued"),
		waitAnswer(false, "running"),
		waitAnswer(true, "completed"),
	}})
	run, err := New(core.URL).Automation().WaitForRun(context.Background(), "run_1", WaitOpts{Timeout: 10 * time.Second})
	if err != nil || run.ID != "run_1" || run.Status != "completed" {
		t.Fatalf("WaitForRun: %+v %v, want run_1 completed", run, err)
	}
	asks := core.asked("wait:run_1")
	if len(asks) != 3 {
		t.Fatalf("asked core's wait %d times, want 3", len(asks))
	}
	for _, q := range asks {
		hold, err := time.ParseDuration(q.Get("timeout"))
		if err != nil || hold <= 0 || hold > runWaitHold {
			t.Fatalf("asked with timeout %q, want a hold up to %v", q.Get("timeout"), runWaitHold)
		}
	}
}

// A core that answers it cannot take the wait yet (503) is waited for on
// its readiness endpoint, then asked again.
func TestWaitForRun_NotReadyCoreIsWaitedForThenAskedAgain(t *testing.T) {
	core := newWaitCore(t, map[string][]answer{"wait:run_1": {
		{http.StatusServiceUnavailable, map[string]string{"error": "node not ready: serving"}},
		waitAnswer(true, "failed"),
	}})
	run, err := New(core.URL).Automation().WaitForRun(context.Background(), "run_1", WaitOpts{Timeout: 10 * time.Second})
	if err != nil || run.Status != "failed" {
		t.Fatalf("WaitForRun: %+v %v, want the run failed", run, err)
	}
	if n := len(core.asked("ready")); n != 1 {
		t.Fatalf("asked core's readiness %d times, want once", n)
	}
}

// A run core refuses (not the caller's) is core's answer, as an *APIError.
func TestWaitForRun_RefusedRunIsCoresAnswer(t *testing.T) {
	core := newWaitCore(t, map[string][]answer{"wait:run_1": {{http.StatusForbidden, map[string]string{"error": "Access denied"}}}})
	_, err := New(core.URL).Automation().WaitForRun(context.Background(), "run_1", WaitOpts{Timeout: 10 * time.Second})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden {
		t.Fatalf("WaitForRun on a refused run: %v, want a 403 APIError", err)
	}
}

// WaitForRun ends at its timeout while core holds the wait.
func TestWaitForRun_EndsAtItsTimeout(t *testing.T) {
	core := newWaitCore(t, map[string][]answer{})
	start := time.Now()
	_, err := New(core.URL).Automation().WaitForRun(context.Background(), "run_1", WaitOpts{Timeout: 200 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForRun past its timeout: %v, want the deadline", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("WaitForRun took %v to give up after 200ms", took)
	}
}

func changesAnswer(version uint64, apps ...appVersion) answer {
	return answer{http.StatusOK, appChanges{Version: version, Apps: apps}}
}

// WatchAppChanges reads the apps' version, then asks for the changes after
// the version it holds, and passes each change to onChange: an app listed
// past that version registered, a held app no longer listed removed. An
// answer that the version has not passed changes nothing.
func TestWatchAppChanges_PassesEachCommittedChange(t *testing.T) {
	core := newWaitCore(t, map[string][]answer{"changes": {
		changesAnswer(5, appVersion{"alpha", 3}),
		changesAnswer(5, appVersion{"alpha", 3}),
		changesAnswer(7, appVersion{"alpha", 3}, appVersion{"beta", 7}),
		changesAnswer(9, appVersion{"beta", 7}),
	}})
	got := make(chan AppChange, 8)
	w, err := New(core.URL).WatchAppChanges(context.Background(), func(c AppChange) { got <- c })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, want := range []AppChange{{"beta", "registered"}, {"alpha", "removed"}} {
		select {
		case c := <-got:
			if c != want {
				t.Fatalf("change %+v, want %+v", c, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("never heard %+v", want)
		}
	}
	core.heardAsks(t, "changes", 5)
	asks := core.asked("changes")
	if len(asks) != 5 || asks[0].Has("after") {
		t.Fatalf("questions %v, want the version first then four waits", asks)
	}
	for i, want := range []uint64{5, 5, 7, 9} {
		if after, _ := strconv.ParseUint(asks[i+1].Get("after"), 10, 64); after != want {
			t.Fatalf("question %d asked after %s, want %d", i+2, asks[i+1].Get("after"), want)
		}
	}
	select {
	case c := <-got:
		t.Fatalf("an unchanged answer passed %+v", c)
	default:
	}
}
