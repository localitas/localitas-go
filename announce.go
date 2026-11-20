package client

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"time"
)

const withdrawTimeout = 5 * time.Second

// Announcement is an app's registration with core, made by Announce.
type Announcement struct {
	c *Client
	// stop ends the wait for core; abort ends a registration still in flight.
	stop  context.CancelFunc
	abort context.CancelFunc
	done  chan struct{}
	name  string // the app core registered, once it has
}

// Announce registers the app serving on ln with core, so core proxies to it
// and runs its jobs. The app tells core only where it is
// (http://localhost:<port>); core reads the rest from its health.json,
// cron.json and public-paths.json. Announce returns at once and, in the
// background, waits until core is ready (WaitReady) and registers the app
// once, since core reads the app while registering it: call it before
// serving. A registration core answers 503 (not ready: it restarted) waits
// again; one it answers 502 (the app does not answer yet) is asked again
// after a bounded backoff; any other failure is logged at ERROR and not
// tried again. Call Withdraw on shutdown.
//
// In a container it does nothing: core registers app containers itself when
// Docker reports them healthy, and only core knows the port it published.
func (c *Client) Announce(ln net.Listener) *Announcement {
	a := &Announcement{c: c, done: make(chan struct{})}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if isContainer() || !ok {
		close(a.done)
		return a
	}
	appURL := fmt.Sprintf("http://localhost:%d", addr.Port)
	ctx, stop := context.WithCancel(context.Background())
	attempt, abort := context.WithCancel(context.Background())
	a.stop, a.abort = stop, abort
	go func() {
		defer close(a.done)
		defer abort()
		redial := redialFirst
		for {
			if err := c.WaitReady(ctx, PhaseReady); err != nil {
				if ctx.Err() == nil {
					slog.Error("waiting for core to be ready failed; the app is not announced", "url", appURL, "error", err)
				}
				return
			}
			var app struct {
				Name string `json:"name"`
			}
			err := c.do(attempt, "POST", "/apps/ext", struct {
				URL string `json:"url"`
			}{appURL}, &app)
			switch {
			case err == nil:
				a.name = app.Name
				slog.Info("announced to core", "app", app.Name, "url", appURL)
				return
			case ctx.Err() != nil:
				return
			case appNotAnswering(err):
				// The app announces before it serves: nothing tells this
				// loop when it starts answering, so core is asked again
				// after a bounded backoff.
				slog.Info("core cannot reach the app yet; asking again", "url", appURL, "retry_in", redial, "error", err)
				if sleepCtx(ctx, redial) != nil {
					return
				}
				redial = min(2*redial, redialMax)
			case notReady(err):
				slog.Info("core is not ready to take the app; waiting for it", "url", appURL, "error", err)
			default:
				slog.Error("core refused the app; it is not announced", "url", appURL, "error", err)
				return
			}
		}
	}()
	return a
}

// Withdraw stops announcing and takes the app out of core, if core had
// taken it. An attempt in flight may already have registered the app (core
// answers after taking it), and only its answer names the app, so it is
// waited for, up to withdrawTimeout, rather than abandoned.
func (a *Announcement) Withdraw() error {
	if a.stop != nil {
		a.stop()
		select {
		case <-a.done:
		case <-time.After(withdrawTimeout):
			a.abort()
		}
	}
	<-a.done
	if a.name == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), withdrawTimeout)
	defer cancel()
	if err := a.c.do(ctx, "DELETE", "/apps/ext/"+url.PathEscape(a.name), nil, nil); err != nil {
		return err
	}
	a.name = ""
	return nil
}

// Name is the app core registered, or "" while core has not taken it.
func (a *Announcement) Name() string {
	select {
	case <-a.done:
		return a.name
	default:
		return ""
	}
}
