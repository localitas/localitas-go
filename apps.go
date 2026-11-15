package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

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
	ps := NewPubSubWS("ws"+strings.TrimPrefix(c.baseURL, "http")+"/apps/cache/ws/"+AppsCache, c.bearer(ctx))
	ps.Watch(AppChangesChannel, noted, func(msg PubSubMessage) {
		var change AppChange
		if err := json.Unmarshal([]byte(msg.Value), &change); err == nil && change.App != "" {
			onChange(change)
		}
	})
	return ps, nil
}
