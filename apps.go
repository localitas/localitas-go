package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrNotAnAppCall is the error of an App call naming no app or no path: an
// app is reached by its name and a path, never an address.
var ErrNotAnAppCall = errors.New("not an app call")

var appNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// AppClient calls another app by name, through core's /apps/ext/<name>
// proxy, which finds the app wherever it runs now (this node's instance,
// else a node holding one). Make one with Client.App.
type AppClient struct {
	client *Client
	name   string
}

// App is the app named name, called with this client's token: core gives
// the app the identity that token has, as for any call through its proxy.
func (c *Client) App(name string) *AppClient {
	return &AppClient{client: c, name: name}
}

// path is core's path for the app's path p, which must be a path ("/api/x",
// a query allowed), never a URL.
func (a *AppClient) path(p string) (string, error) {
	if !appNamePattern.MatchString(a.name) {
		return "", fmt.Errorf("%w: app name %q is not an app's name", ErrNotAnAppCall, a.name)
	}
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, `\`) || strings.Contains(p, "://") {
		return "", fmt.Errorf("%w: %q is not a path of app %s", ErrNotAnAppCall, p, a.name)
	}
	return "/apps/ext/" + url.PathEscape(a.name) + p, nil
}

// Get calls GET path on the app and decodes its JSON answer into out, if
// non-nil.
func (a *AppClient) Get(ctx context.Context, path string, out any) error {
	return a.Do(ctx, "GET", path, nil, out)
}

// Post calls POST path on the app with body as JSON (none when nil).
func (a *AppClient) Post(ctx context.Context, path string, body, out any) error {
	return a.Do(ctx, "POST", path, body, out)
}

// Put calls PUT path on the app with body as JSON (none when nil).
func (a *AppClient) Put(ctx context.Context, path string, body, out any) error {
	return a.Do(ctx, "PUT", path, body, out)
}

// Delete calls DELETE path on the app.
func (a *AppClient) Delete(ctx context.Context, path string, out any) error {
	return a.Do(ctx, "DELETE", path, nil, out)
}

// Do calls method path on the app with body as JSON; a 4xx or 5xx answer is
// an *APIError.
func (a *AppClient) Do(ctx context.Context, method, path string, body, out any) error {
	p, err := a.path(path)
	if err != nil {
		return err
	}
	return a.client.do(ctx, method, p, body, out)
}

// Send calls method path on the app with a raw body of contentType, such as
// a multipart form, and decodes its JSON answer into out, if non-nil.
func (a *AppClient) Send(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	p, err := a.path(path)
	if err != nil {
		return err
	}
	return a.client.send(ctx, method, p, contentType, body, out)
}

// ExternalApp is an app core serves at /apps/ext/<name>/.
type ExternalApp struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	URL         string `json:"url"`
	Icon        string `json:"icon"`
	Enabled     bool   `json:"enabled"`
}

// ListExternalApps lists the apps core serves now.
func (c *Client) ListExternalApps(ctx context.Context) ([]ExternalApp, error) {
	var apps []ExternalApp
	if err := c.do(ctx, "GET", "/apps/ext", nil, &apps); err != nil {
		return nil, err
	}
	return apps, nil
}

// AppChange is a change to app core committed: "registered" (the app is
// there and changed since: registered, or an instance added or removed) or
// "removed" (its last instance went). Read the app list (or the app) for
// what it is now.
type AppChange struct {
	App    string `json:"app"`
	Change string `json:"change"`
}

// AppsCache and AppChangesChannel are where core hints at app changes, once
// and best-effort; WatchAppChanges does not depend on the hints.
const (
	AppsCache         = "localitas_apps"
	AppChangesChannel = "changes"
)

// appChangesHold is how long core holds one question for the app changes
// before answering that nothing changed: under core's 30 s write timeout.
const appChangesHold = 25 * time.Second

// appVersion and appChanges are core's answer to GET /apps/ext/changes: the
// apps' version and each app there with the version of its last change.
type appVersion struct {
	Name    string `json:"name"`
	Version uint64 `json:"version"`
}

type appChanges struct {
	Version uint64       `json:"version"`
	Apps    []appVersion `json:"apps"`
}

// AppChangesWatch is a running WatchAppChanges.
type AppChangesWatch struct {
	stop context.CancelFunc
	done chan struct{}
}

// Close stops the watch and waits for it: onChange is not called after it
// returns.
func (w *AppChangesWatch) Close() {
	w.stop()
	<-w.done
}

// WatchAppChanges calls onChange for every app change core commits from
// now on, until the returned watch is closed. Call it before reading the
// current apps (ListExternalApps): a change is then either in what is read
// or passed to onChange after.
//
// It asks core for the apps' version now, then for the changes after it, a
// long poll any node answers as soon as a registration or removal commits
// past it (or after its hold, when it asks again). The version is the
// cluster's, the same on every node, so no change is missed: not one whose
// hint was lost, nor one made while a question was not in flight. A core
// that cannot answer yet (503, or its process not there) is waited for
// (WaitReady); any other failure is logged and asked again after a bounded
// backoff. ctx bounds the first question only.
func (c *Client) WatchAppChanges(ctx context.Context, onChange func(AppChange)) (*AppChangesWatch, error) {
	var now appChanges
	if err := c.do(ctx, http.MethodGet, "/apps/ext/changes", nil, &now); err != nil {
		return nil, fmt.Errorf("read the app changes version: %w", err)
	}
	watchCtx, stop := context.WithCancel(context.Background())
	w := &AppChangesWatch{stop: stop, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		version, held := now.Version, now.Apps
		redial := redialFirst
		for {
			var ans appChanges
			path := "/apps/ext/changes?" + url.Values{"after": {strconv.FormatUint(version, 10)}, "timeout": {appChangesHold.String()}}.Encode()
			err := c.do(watchCtx, http.MethodGet, path, nil, &ans)
			switch {
			case watchCtx.Err() != nil:
				return
			case err == nil:
				redial = redialFirst
				if ans.Version <= version {
					continue
				}
				changes := appChangesSince(version, held, ans.Apps)
				version, held = ans.Version, ans.Apps
				for _, change := range changes {
					onChange(change)
				}
			case notReady(err):
				if c.WaitReady(watchCtx, PhaseReady) != nil {
					return
				}
			default:
				slog.Error("asking core for the app changes failed; asking again", "retry_in", redial, "error", err)
				if sleepCtx(watchCtx, redial) != nil {
					return
				}
				redial = min(2*redial, redialMax)
			}
		}
	}()
	return w, nil
}

// appChangesSince is what changed from held, the apps at version after, to
// now: each app now there that was not, or whose version is past after, is
// registered; each held app not there now is removed.
func appChangesSince(after uint64, held, now []appVersion) []AppChange {
	was := make(map[string]bool, len(held))
	for _, a := range held {
		was[a.Name] = true
	}
	var changes []AppChange
	for _, a := range now {
		if a.Version > after || !was[a.Name] {
			changes = append(changes, AppChange{App: a.Name, Change: "registered"})
		}
		delete(was, a.Name)
	}
	gone := make([]string, 0, len(was))
	for name := range was {
		gone = append(gone, name)
	}
	sort.Strings(gone)
	for _, name := range gone {
		changes = append(changes, AppChange{App: name, Change: "removed"})
	}
	return changes
}
