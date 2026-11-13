package httputil

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Upstream asks a public service an app depends on (a forecast, a
// dictionary, a map), asking again after a pause when the service fails for
// a moment, as public APIs now and then do.
type Upstream struct {
	// Client sends the requests; nil is http.DefaultClient.
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
	Err     error
}

func (e *UpstreamError) Error() string { return e.Service + ": " + e.Err.Error() }
func (e *UpstreamError) Unwrap() error { return e.Err }

// Get GETs u from service. A network error or a 5xx is tried again; a
// response Get returns has a status below 500, and a service that failed
// every try is an *UpstreamError.
func (up Upstream) Get(ctx context.Context, service, u string) (*http.Response, error) {
	client := up.Client
	if client == nil {
		client = http.DefaultClient
	}
	attempts := up.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	delay := up.Delay
	if delay <= 0 {
		delay = 500 * time.Millisecond
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, &UpstreamError{Service: service, Err: ctx.Err()}
			case <-time.After(delay * time.Duration(attempt)):
			}
		}
		tryCtx, cancel := context.WithTimeout(ctx, up.attemptTimeout())
		req, err := http.NewRequestWithContext(tryCtx, http.MethodGet, u, nil)
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
			if ctx.Err() != nil {
				return nil, &UpstreamError{Service: service, Err: ctx.Err()}
			}
			last = err
			continue
		}
		if resp.StatusCode >= http.StatusInternalServerError {
			resp.Body.Close()
			cancel()
			last = fmt.Errorf("returned %d", resp.StatusCode)
			continue
		}
		resp.Body = cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	}
	return nil, &UpstreamError{Service: service, Err: last}
}
