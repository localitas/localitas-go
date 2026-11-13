package httputil

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// flaky stands in for a public API: it fails its first failures requests
// with status, then answers 200 (or 404 for /missing).
func flaky(t *testing.T, failures int, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if int(calls.Add(1)) <= failures {
			http.Error(w, "down", status)
			return
		}
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(r.Header.Get("User-Agent")))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

var quick = Upstream{UserAgent: "Localitas Test/1.0", Delay: time.Millisecond}

// A service that stalls a request rather than failing it is not waited on
// for ever: the stalled try is given up after AttemptTimeout and the next
// one answered, well before the caller would give up.
func TestUpstream_AStalledTryIsGivenUpAndAskedAgain(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		w.Write([]byte("forecast"))
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	up := Upstream{Delay: time.Millisecond, AttemptTimeout: 100 * time.Millisecond}
	start := time.Now()
	resp, err := up.Get(ctx, "forecast", srv.URL)
	if err != nil {
		t.Fatalf("a stalled try was not asked again: %v after %s", err, time.Since(start))
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "forecast" || calls.Load() != 2 {
		t.Errorf("answer %q after %d tries", b, calls.Load())
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("the stalled try was waited on for %s", time.Since(start))
	}
}

// With no AttemptTimeout, a try is given up after the default's ten
// seconds, so an app's request is never left hanging on a public service.
func TestUpstream_TriesHaveADefaultTimeout(t *testing.T) {
	if (Upstream{}).attemptTimeout() != 10*time.Second {
		t.Errorf("default try timeout: %s", (Upstream{}).attemptTimeout())
	}
}

// A service failing for a moment is asked again and its answer returned.
func TestUpstream_AMomentaryFailureIsTriedAgain(t *testing.T) {
	srv, calls := flaky(t, 2, http.StatusBadGateway)

	resp, err := quick.Get(context.Background(), "forecast", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || calls.Load() != 3 {
		t.Errorf("status %d after %d calls, want 200 after 3", resp.StatusCode, calls.Load())
	}
}

// A service that keeps failing is an UpstreamError naming it, after the
// set number of tries.
func TestUpstream_AServiceThatKeepsFailingIsAnUpstreamError(t *testing.T) {
	srv, calls := flaky(t, 100, http.StatusServiceUnavailable)

	_, err := quick.Get(context.Background(), "forecast", srv.URL)
	var up *UpstreamError
	if !errors.As(err, &up) || up.Service != "forecast" {
		t.Fatalf("err = %v, want an UpstreamError for forecast", err)
	}
	if calls.Load() != 3 {
		t.Errorf("asked %d times, want 3", calls.Load())
	}

	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	if _, err := quick.Get(context.Background(), "forecast", gone.URL); !errors.As(err, &up) {
		t.Errorf("an unreachable service: %v, want an UpstreamError", err)
	}
}

// An answer below 500 is the service's answer, returned at once.
func TestUpstream_ANotFoundIsReturnedAtOnce(t *testing.T) {
	srv, calls := flaky(t, 0, 0)

	resp, err := quick.Get(context.Background(), "dictionary", srv.URL+"/missing")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || calls.Load() != 1 {
		t.Errorf("status %d after %d calls, want 404 after 1", resp.StatusCode, calls.Load())
	}
}

// A cancelled request stops asking.
func TestUpstream_ACancelledRequestStopsAsking(t *testing.T) {
	srv, calls := flaky(t, 100, http.StatusBadGateway)
	ctx, cancel := context.WithCancel(context.Background())
	slow := Upstream{Delay: time.Hour}

	done := make(chan error, 1)
	go func() {
		_, err := slow.Get(ctx, "forecast", srv.URL)
		done <- err
	}()
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want cancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Get kept waiting after its request was cancelled")
	}
	if calls.Load() != 1 {
		t.Errorf("asked %d times, want 1", calls.Load())
	}
}

// The user agent set is sent.
func TestUpstream_SendsTheUserAgent(t *testing.T) {
	srv, _ := flaky(t, 0, 0)

	resp, err := quick.Get(context.Background(), "forecast", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	if string(buf[:n]) != "Localitas Test/1.0" {
		t.Errorf("user agent sent = %q", buf[:n])
	}
}
