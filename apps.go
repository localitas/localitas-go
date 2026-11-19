package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
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

// AppChange is core's hint that app was registered ("registered") or taken
// out ("removed"). It is a hint, not the truth: any user with write scope
// can publish to the channel, so read the app list (or the app) for what is
// so, and never act on a hint alone.
type AppChange struct {
	App    string `json:"app"`
	Change string `json:"change"`
}

// AppsCache and AppChangesChannel are where core announces app changes.
const (
	AppsCache         = "localitas_apps"
	AppChangesChannel = "changes"
)

// WatchAppChanges calls onChange for every app change core announces from
// now on, until the returned watch is closed; it reconnects by itself and
// misses none. Call it before reading the current apps (ListExternalApps):
// a change is then either in what is read or announced after.
func (c *Client) WatchAppChanges(ctx context.Context, onChange func(AppChange)) (*PubSubWS, error) {
	noted, err := c.Cache(AppsCache).PubSub(AppChangesChannel, PubSubOpts{}).LastSeq(ctx)
	if err != nil {
		return nil, fmt.Errorf("note the app changes channel: %w", err)
	}
	ps := c.Cache(AppsCache).Watch(AppChangesChannel, noted, func(msg PubSubMessage) {
		var change AppChange
		if err := json.Unmarshal([]byte(msg.Value), &change); err == nil && change.App != "" {
			onChange(change)
		}
	})
	return ps, nil
}
