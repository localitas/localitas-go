package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"syscall"
	"time"
)

// The phases of a node's lifecycle WaitReady can wait for
// (core/go/docs/design/node-lifecycle.md).
const (
	// PhaseRaftReady: every shard the node runs has a leader.
	PhaseRaftReady = "raft_ready"
	// PhaseServing: core serves its own calls; users still get 503.
	PhaseServing = "serving"
	// PhaseReady: normal operation; every route serves.
	PhaseReady = "ready"
)

const (
	// readyPollTimeout is how long core holds one readiness question before
	// answering that the node is not there yet: under core's shortest HTTP
	// write timeout (30 s), which would cut a longer one.
	readyPollTimeout = 25 * time.Second
	redialFirst      = 100 * time.Millisecond
	redialMax        = 5 * time.Second
)

// ReadyAnswer is core's answer to GET /api/system/ready.
type ReadyAnswer struct {
	NodeID string `json:"node_id"`
	// Phase is the node's phase now.
	Phase string `json:"phase"`
	// Want is the phase asked for.
	Want  string `json:"want"`
	Ready bool   `json:"ready"`
	Error string `json:"error,omitempty"`
}

// WaitReady waits until the node this client talks to is at phase (PhaseReady,
// PhaseServing or PhaseRaftReady) with every hook of it finished. It asks
// core's readiness endpoint, a long poll core answers at the transition
// itself, and asks again when core answers that the node is not there yet:
// the poll's timeout passed, or the node drains (its next boot follows).
// It returns ctx's error when ctx ends, and any other failure, a fault no
// waiting mends, as is.
func (c *Client) WaitReady(ctx context.Context, phase string) error {
	redial := redialFirst
	for {
		ans, retryAfter, err := c.askReady(ctx, phase)
		switch {
		case err == nil && ans.Ready:
			return nil
		case err == nil:
			redial = redialFirst
			if ans.Phase != "draining" && ans.Phase != "stopped" {
				continue
			}
			// A draining node answers at once until its HTTP stops; core
			// says when to ask again.
			if err := sleepCtx(ctx, retryAfter); err != nil {
				return err
			}
		case ctx.Err() != nil:
			return ctx.Err()
		case coreDown(err):
			// Core's process is not running or not yet listening: the one
			// case with no event to wait on, so dial again after a bounded
			// backoff.
			if err := sleepCtx(ctx, redial); err != nil {
				return err
			}
			redial = min(2*redial, redialMax)
		default:
			return err
		}
	}
}

// askReady asks core's readiness endpoint once, held up to readyPollTimeout,
// and returns its answer and, for a not-ready one, its Retry-After.
func (c *Client) askReady(ctx context.Context, phase string) (ReadyAnswer, time.Duration, error) {
	path := "/api/system/ready?" + url.Values{"phase": {phase}, "timeout": {readyPollTimeout.String()}}.Encode()
	target, err := c.urlFor(http.MethodGet, path)
	if err != nil {
		return ReadyAnswer{}, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ReadyAnswer{}, 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ReadyAnswer{}, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ReadyAnswer{}, 0, fmt.Errorf("read GET %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		return ReadyAnswer{}, 0, &APIError{Method: http.MethodGet, Path: path, StatusCode: resp.StatusCode, Body: string(body)}
	}
	var ans ReadyAnswer
	if err := json.Unmarshal(body, &ans); err != nil || ans.Ready != (resp.StatusCode == http.StatusOK) {
		return ReadyAnswer{}, 0, fmt.Errorf("GET %s: %d is not core's readiness answer: %s", path, resp.StatusCode, body)
	}
	retryAfter := time.Second
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s >= 0 {
		retryAfter = time.Duration(s) * time.Second
	}
	return ans, retryAfter, nil
}

// coreDown is whether err says core's process is not there: it refused the
// connection, or dropped it without answering.
func coreDown(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// appNotAnswering is whether core could not take the app because the app did
// not answer it yet (502): its port closed, or health.json not served yet.
func appNotAnswering(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadGateway
}

// notReady is whether a call failed because core could not take it yet: it
// answered 503 (not ready), or its process is not there.
func notReady(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusServiceUnavailable
	}
	return coreDown(err)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
