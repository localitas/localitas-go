package client

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// unansweringCore takes every request and never answers until the test ends.
func unansweringCore(t *testing.T) string {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv.URL
}

// shortDefaults makes the client's defaults short for one test.
func shortDefaults(t *testing.T, request, header time.Duration) {
	t.Helper()
	oldRequest, oldClient := requestTimeout, defaultHTTPClient
	requestTimeout = request
	defaultHTTPClient = newDefaultHTTPClient(header)
	t.Cleanup(func() { requestTimeout, defaultHTTPClient = oldRequest, oldClient })
}

func failsWithin(t *testing.T, what string, call func() error) {
	t.Helper()
	errc := make(chan error, 1)
	go func() { errc <- call() }()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatalf("%s to a core that never answers reported success", what)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s still waiting on a core that never answers after 5s", what)
	}
}

// A call with no deadline of its own gives up at the client's default
// instead of hanging on a core that never answers.
func TestDo_UnansweringCoreGivesUpAtTheDefault(t *testing.T) {
	shortDefaults(t, 100*time.Millisecond, time.Minute)
	c := New(unansweringCore(t))
	failsWithin(t, "a call", func() error {
		return c.Do(context.Background(), "GET", "/api/anything", nil, nil)
	})
}

// A download, which may stream for long, still gives up when core never
// starts answering.
func TestDownload_UnansweringCoreGivesUp(t *testing.T) {
	shortDefaults(t, time.Minute, 100*time.Millisecond)
	c := New(unansweringCore(t))
	failsWithin(t, "a download", func() error {
		_, err := c.Download(context.Background(), "/api/file", &bytes.Buffer{})
		return err
	})
}

// A caller's own deadline wins over the default: a call allowed longer is
// not cut when the default passes.
func TestDo_TheCallersDeadlineWins(t *testing.T) {
	shortDefaults(t, 50*time.Millisecond, time.Minute)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Core answers well after the default would have passed.
		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := New(srv.URL).Do(ctx, "GET", "/api/slow", nil, nil); err != nil {
		t.Fatalf("a call allowed 5s was cut: %v", err)
	}
}

// A download that is answering is not cut by the default: its body may take
// as long as it takes.
func TestDownload_ALongBodyIsNotCutAtTheDefault(t *testing.T) {
	shortDefaults(t, 50*time.Millisecond, 50*time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("first half,"))
		w.(http.Flusher).Flush()
		// The rest comes after both defaults have passed.
		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.Write([]byte("second half"))
	}))
	t.Cleanup(srv.Close)
	var got bytes.Buffer
	if _, err := New(srv.URL).Download(context.Background(), "/api/file", &got); err != nil {
		t.Fatalf("a streaming download was cut: %v", err)
	}
	if got.String() != "first half,second half" {
		t.Fatalf("downloaded %q", got.String())
	}
}
