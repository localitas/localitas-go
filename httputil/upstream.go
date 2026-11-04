package httputil

import (
	"context"
	"fmt"
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
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		if up.UserAgent != "" {
			req.Header.Set("User-Agent", up.UserAgent)
		}
		resp, err := client.Do(req)
		if err != nil {
			last = err
			continue
		}
		if resp.StatusCode >= http.StatusInternalServerError {
			resp.Body.Close()
			last = fmt.Errorf("returned %d", resp.StatusCode)
			continue
		}
		return resp, nil
	}
	return nil, &UpstreamError{Service: service, Err: last}
}
