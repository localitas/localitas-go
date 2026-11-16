package httputil

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"
)

// Upstream asks a public service an app depends on (a forecast, a
// dictionary, a map), asking again after a pause when the service fails for
// a moment, as public APIs now and then do.
type Upstream struct {
	// Client sends the requests; nil is a client whose idle HTTP/2
	// connections are checked with pings (upstreamClient).
	Client *http.Client
	// UserAgent is sent with each request when set.
	UserAgent string
	// Attempts is how many times a request is tried; 0 is 3.
	Attempts int
	// Delay is the pause before the second try, and each later try waits
	// one Delay longer; 0 is 500ms.
	Delay time.Duration
	// AttemptTimeout is how long one try may take, its answer read to the
	// end included; a try that stalls is given up and the service asked
	// again. 0 is 10s.
	AttemptTimeout time.Duration
}

// upstreamClient is the default client. A NAT, such as Docker Desktop's, can
// drop a connection without telling either end. HTTP/2 keeps no watch on a
// connection by default, and a request that times out on it leaves it in
// the pool, so every try after it would stall on it too. A connection
// silent for 5 s is pinged, and closed when no answer comes in 3 s: one
// that died is found within a try's 10 s, and the next try dials afresh.
var upstreamClient = newUpstreamClient(http.DefaultTransport.(*http.Transport), 5*time.Second, 3*time.Second)

// newUpstreamClient is a client on a copy of base whose HTTP/2 connections
// are pinged after sendPing of silence and closed when the answer takes
// longer than pingTimeout.
func newUpstreamClient(base *http.Transport, sendPing, pingTimeout time.Duration) *http.Client {
	t := base.Clone()
	t.HTTP2 = &http.HTTP2Config{SendPingTimeout: sendPing, PingTimeout: pingTimeout}
	return &http.Client{Transport: t}
}

func (up Upstream) attemptTimeout() time.Duration {
	if up.AttemptTimeout > 0 {
		return up.AttemptTimeout
	}
	return 10 * time.Second
}

// cancelOnClose ends a try's context when its answer is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// UpstreamError is a failure of a service an app depends on, not of what was
// asked of the app; a handler answers it with 502 Bad Gateway.
type UpstreamError struct {
	Service string
	// Err is the last try's failure, or the caller's own context error when
	// the caller gave up first.
	Err error
	// Tries is how far each try got, in order; Error lists them.
	Tries []UpstreamTry
	// callerDone is set when the caller's context ended the asking.
	callerDone bool
}

// Error names the service and, when tries were made, how far each got:
//
//	Open-Meteo: 3 tries failed: #1 reused idle conn (idle 41s) to
//	51.81.167.44:443, request written, no response in 10s; #2 new conn:
//	dns 12ms, connect 40ms to 51.81.167.44:443, tls stalled after 10s; ...
func (e *UpstreamError) Error() string {
	if len(e.Tries) == 0 {
		return e.Service + ": " + e.Err.Error()
	}
	tries := "1 try"
	if len(e.Tries) > 1 {
		tries = fmt.Sprintf("%d tries", len(e.Tries))
	}
	head := tries + " failed"
	if e.callerDone {
		head = e.Err.Error() + " after " + tries
	}
	parts := make([]string, len(e.Tries))
	for i, t := range e.Tries {
		parts[i] = fmt.Sprintf("#%d %s", i+1, t)
	}
	return e.Service + ": " + head + ": " + strings.Join(parts, "; ")
}

func (e *UpstreamError) Unwrap() error { return e.Err }

// Get GETs u from service. A network error or a 5xx is tried again; a
// response Get returns has a status below 500, and a service that failed
// every try is an *UpstreamError.
func (up Upstream) Get(ctx context.Context, service, u string) (*http.Response, error) {
	client := up.Client
	if client == nil {
		client = upstreamClient
	}
	attempts := up.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	delay := up.Delay
	if delay <= 0 {
		delay = 500 * time.Millisecond
	}
	fail := &UpstreamError{Service: service}
	callerDone := func() error {
		fail.Err, fail.callerDone = ctx.Err(), true
		return fail
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, callerDone()
			case <-time.After(delay * time.Duration(attempt)):
			}
		}
		tryCtx, cancel := context.WithTimeout(ctx, up.attemptTimeout())
		tr := newTryTracer()
		req, err := http.NewRequestWithContext(httptrace.WithClientTrace(tryCtx, tr.clientTrace()), http.MethodGet, u, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		if up.UserAgent != "" {
			req.Header.Set("User-Agent", up.UserAgent)
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			fail.Tries = append(fail.Tries, tr.finish(err, 0))
			if ctx.Err() != nil {
				return nil, callerDone()
			}
			fail.Err = err
			continue
		}
		if resp.StatusCode >= http.StatusInternalServerError {
			resp.Body.Close()
			cancel()
			fail.Err = fmt.Errorf("returned %d", resp.StatusCode)
			fail.Tries = append(fail.Tries, tr.finish(fail.Err, resp.StatusCode))
			continue
		}
		resp.Body = cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	}
	return nil, fail
}
