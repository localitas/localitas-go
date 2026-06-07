// Package client is the Go SDK for the Localitas data app HTTP API.
//
// It is the contract between the platform and apps built on top of it. Apps call
// methods on Client; Client issues HTTP requests with a bearer token forwarded from
// the incoming request. The SDK never stores credentials or validates tokens — that
// is the responsibility of the upstream reverse proxy.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultCorePort = "8090"
)

const defaultTokenPath = ".localitas/api-token"

// DefaultToken reads the API token from ~/.localitas/api-token.
// Returns empty string if the file doesn't exist.
func DefaultToken() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(home + "/" + defaultTokenPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func DefaultCoreURL() string {
	if isContainer() {
		return "http://host.docker.internal:" + DefaultCorePort
	}
	return "http://localhost:" + DefaultCorePort
}

func isContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	data, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(data)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd") || strings.Contains(s, "kubepods")
}

// Client talks to the data app HTTP API. Safe for concurrent use: token is per-call
// via WithToken, never stored on the shared instance.
type Client struct {
	baseURL string
	http    *http.Client
	token   string
}

// New returns a Client bound to the given data-app base URL (e.g. "http://localhost:9090").
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    http.DefaultClient,
	}
}

// WithHTTPClient returns a shallow copy using the supplied http.Client.
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	clone := *c
	clone.http = h
	return &clone
}

// WithToken returns a shallow copy that will send the supplied bearer token on each
// request. Call per incoming HTTP request with the token lifted off that request's
// Authorization header.
func (c *Client) WithToken(bearer string) *Client {
	clone := *c
	clone.token = bearer
	return &clone
}

// ----- Databases -------------------------------------------------------------

func (c *Client) ListDatabases(ctx context.Context) ([]Database, error) {
	var out []Database
	if err := c.do(ctx, "GET", "/apps/data/api/databases", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateDatabase(ctx context.Context, name string) (*Database, error) {
	var out Database
	if err := c.do(ctx, "POST", "/apps/data/api/databases", map[string]any{"name": name}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateSystemDatabase is idempotent by name. Requires admin role on the caller.
func (c *Client) CreateSystemDatabase(ctx context.Context, name string) (*Database, error) {
	var out Database
	if err := c.do(ctx, "POST", "/apps/data/api/databases", map[string]any{"name": name, "system": true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetDatabase(ctx context.Context, id string) (*Database, error) {
	var out Database
	if err := c.do(ctx, "GET", "/apps/data/api/databases/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) DeleteDatabase(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/apps/data/api/databases/"+url.PathEscape(id), nil, nil)
}

// ----- Migrations ------------------------------------------------------------

func (c *Client) ListDatabaseMigrations(ctx context.Context, dbID string) ([]DatabaseMigration, error) {
	var out []DatabaseMigration
	path := fmt.Sprintf("/apps/data/api/databases/%s/migrations", url.PathEscape(dbID))
	if err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ApplyDatabaseMigration is idempotent by (dbID, version).
func (c *Client) ApplyDatabaseMigration(ctx context.Context, dbID, version, description, upSQL, downSQL string) (*DatabaseMigration, error) {
	var out DatabaseMigration
	path := fmt.Sprintf("/apps/data/api/databases/%s/migrations", url.PathEscape(dbID))
	body := map[string]any{
		"version":     version,
		"description": description,
		"up_sql":      upSQL,
		"down_sql":    downSQL,
	}
	if err := c.do(ctx, "POST", path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ----- Tables & Rows ---------------------------------------------------------

func (c *Client) ListTables(ctx context.Context, dbID string) ([]Table, error) {
	var out []Table
	path := fmt.Sprintf("/apps/data/api/databases/%s/tables", url.PathEscape(dbID))
	if err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) InsertRow(ctx context.Context, dbID, tableID string, values map[string]interface{}) (*Row, error) {
	var out Row
	path := fmt.Sprintf("/apps/data/api/databases/%s/tables/%s/rows",
		url.PathEscape(dbID), url.PathEscape(tableID))
	if err := c.do(ctx, "POST", path, map[string]any{"values": values}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) UpdateRow(ctx context.Context, dbID, tableID, rowID string, values map[string]interface{}) error {
	path := fmt.Sprintf("/apps/data/api/databases/%s/tables/%s/rows/%s",
		url.PathEscape(dbID), url.PathEscape(tableID), url.PathEscape(rowID))
	return c.do(ctx, "PUT", path, map[string]any{"values": values}, nil)
}

func (c *Client) DeleteRow(ctx context.Context, dbID, tableID, rowID string) error {
	path := fmt.Sprintf("/apps/data/api/databases/%s/tables/%s/rows/%s",
		url.PathEscape(dbID), url.PathEscape(tableID), url.PathEscape(rowID))
	return c.do(ctx, "DELETE", path, nil, nil)
}

func (c *Client) ListRows(ctx context.Context, dbID, tableID string, limit, offset int) ([]Row, error) {
	var out []Row
	path := fmt.Sprintf("/apps/data/api/databases/%s/tables/%s/rows?limit=%d&offset=%d",
		url.PathEscape(dbID), url.PathEscape(tableID), limit, offset)
	if err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetRow(ctx context.Context, dbID, tableID, rowID string) (*Row, error) {
	var out Row
	path := fmt.Sprintf("/apps/data/api/databases/%s/tables/%s/rows/%s",
		url.PathEscape(dbID), url.PathEscape(tableID), url.PathEscape(rowID))
	if err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ----- Raw SQL ---------------------------------------------------------------

// SQLExec executes a write SQL statement against the database.
func (c *Client) SQLExec(ctx context.Context, dbID, sql string, args ...interface{}) (*SQLExecResult, error) {
	var out SQLExecResult
	path := fmt.Sprintf("/apps/data/api/databases/%s/exec", url.PathEscape(dbID))
	body := map[string]any{"sql": sql, "args": args}
	if err := c.do(ctx, "POST", path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SQLQuery executes a read SQL statement against the database.
func (c *Client) SQLQuery(ctx context.Context, dbID, sql string, args ...interface{}) (*SQLQueryResult, error) {
	var out SQLQueryResult
	path := fmt.Sprintf("/apps/data/api/databases/%s/query", url.PathEscape(dbID))
	body := map[string]any{"sql": sql, "args": args}
	if err := c.do(ctx, "POST", path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SQLTransaction executes multiple statements as an atomic transaction.
func (c *Client) SQLTransaction(ctx context.Context, dbID string, stmts []SQLStatement) (*SQLExecResult, error) {
	var out SQLExecResult
	path := fmt.Sprintf("/apps/data/api/databases/%s/exec", url.PathEscape(dbID))
	body := map[string]any{"statements": stmts}
	if err := c.do(ctx, "POST", path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ----- Search ----------------------------------------------------------------

// SearchOptions scopes/tunes a global search call. Zero-value means "no restriction".
type SearchOptions struct {
	// DatabaseID restricts results to rows owned by this database. Empty = all databases.
	DatabaseID string
	// Limit caps the number of returned results. <= 0 means server default (100).
	Limit int
}

// SearchFTS runs a global FTS5 keyword search over the authenticated user's rows.
func (c *Client) SearchFTS(ctx context.Context, query string, opts SearchOptions) (*SearchResponse, error) {
	var out SearchResponse
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	path := "/apps/data/api/search?q=" + url.QueryEscape(query) + "&limit=" + strconv.Itoa(limit)
	if opts.DatabaseID != "" {
		path += "&database_id=" + url.QueryEscape(opts.DatabaseID)
	}
	if err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SearchHybrid runs hybrid search (FTS + vector, RRF-merged). Falls back to FTS on the
// server when no embedder is configured — observable via SearchResponse.Mode.
func (c *Client) SearchHybrid(ctx context.Context, query string, opts SearchOptions) (*SearchResponse, error) {
	var out SearchResponse
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}
	body := map[string]any{"q": query, "limit": limit}
	if opts.DatabaseID != "" {
		body["database_id"] = opts.DatabaseID
	}
	if err := c.do(ctx, "POST", "/apps/data/api/search/hybrid", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ----- Service Registry ------------------------------------------------------

const serviceRegistryDB = "service_registry"

func (c *Client) ensureServiceRegistry(ctx context.Context) (string, error) {
	db, err := c.CreateSystemDatabase(ctx, serviceRegistryDB)
	if err != nil {
		return "", fmt.Errorf("create service_registry db: %w", err)
	}
	_, err = c.ApplyDatabaseMigration(ctx, db.ID,
		"20260424-000000-000-init",
		"service registry table",
		`CREATE TABLE IF NOT EXISTS services (
			name TEXT PRIMARY KEY,
			url TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		"DROP TABLE IF EXISTS services",
	)
	if err != nil {
		return "", fmt.Errorf("apply service_registry migration: %w", err)
	}
	return db.ID, nil
}

func (c *Client) RegisterService(ctx context.Context, name, serviceURL string) error {
	dbID, err := c.ensureServiceRegistry(ctx)
	if err != nil {
		return err
	}
	now := fmt.Sprintf("%d", time.Now().UTC().Unix())
	_, err = c.SQLExec(ctx, dbID,
		`INSERT INTO services (name, url, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET url = excluded.url, updated_at = excluded.updated_at`,
		name, serviceURL, now,
	)
	return err
}

func (c *Client) DiscoverService(ctx context.Context, name string) (string, error) {
	dbID, err := c.ensureServiceRegistry(ctx)
	if err != nil {
		return "", err
	}
	result, err := c.SQLQuery(ctx, dbID, "SELECT url FROM services WHERE name = ?", name)
	if err != nil {
		return "", err
	}
	if len(result.Rows) == 0 {
		return "", fmt.Errorf("service %q not found", name)
	}
	u, ok := result.Rows[0][0].(string)
	if !ok {
		return "", fmt.Errorf("unexpected url type for service %q", name)
	}
	return u, nil
}

func (c *Client) ListServices(ctx context.Context) ([]ServiceEntry, error) {
	dbID, err := c.ensureServiceRegistry(ctx)
	if err != nil {
		return nil, err
	}
	result, err := c.SQLQuery(ctx, dbID, "SELECT name, url, updated_at FROM services ORDER BY name")
	if err != nil {
		return nil, err
	}
	out := make([]ServiceEntry, 0, len(result.Rows))
	for _, row := range result.Rows {
		name, _ := row[0].(string)
		u, _ := row[1].(string)
		out = append(out, ServiceEntry{Name: name, URL: u})
	}
	return out, nil
}

// ----- Permissions -----------------------------------------------------------

type ResourceMember struct {
	UserID     string `json:"user_id,omitempty"`
	GroupID    string `json:"group_id,omitempty"`
	Permission string `json:"permission"`
}

func (c *Client) SetResourceOwner(ctx context.Context, app, resourceType, resourceID, ownerID string) error {
	return c.do(ctx, "POST", "/api/permissions/set-owner", map[string]string{
		"app": app, "resource_type": resourceType, "resource_id": resourceID, "owner_id": ownerID,
	}, nil)
}

func (c *Client) CheckPermission(ctx context.Context, app, resourceType, resourceID, userID string) (string, error) {
	var result struct {
		Permission string `json:"permission"`
	}
	body := map[string]string{
		"app": app, "resource_type": resourceType, "resource_id": resourceID,
	}
	if userID != "" {
		body["user_id"] = userID
	}
	if err := c.do(ctx, "POST", "/api/permissions/check", body, &result); err != nil {
		return "", err
	}
	return result.Permission, nil
}

func (c *Client) ListResourceMembers(ctx context.Context, app, resourceType, resourceID string) ([]ResourceMember, error) {
	var result struct {
		OwnerID string           `json:"owner_id"`
		Members []ResourceMember `json:"members"`
	}
	path := fmt.Sprintf("/api/permissions/%s/%s/%s/members",
		url.PathEscape(app), url.PathEscape(resourceType), url.PathEscape(resourceID))
	if err := c.do(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}
	return result.Members, nil
}

func (c *Client) AddResourceMember(ctx context.Context, app, resourceType, resourceID string, member ResourceMember) error {
	path := fmt.Sprintf("/api/permissions/%s/%s/%s/members",
		url.PathEscape(app), url.PathEscape(resourceType), url.PathEscape(resourceID))
	return c.do(ctx, "POST", path, member, nil)
}

func (c *Client) RemoveResourceMember(ctx context.Context, app, resourceType, resourceID, userID, groupID string) error {
	path := fmt.Sprintf("/api/permissions/%s/%s/%s/members",
		url.PathEscape(app), url.PathEscape(resourceType), url.PathEscape(resourceID))
	return c.do(ctx, "DELETE", path, map[string]string{"user_id": userID, "group_id": groupID}, nil)
}

// ----- Transport -------------------------------------------------------------

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return &APIError{
			Method:     method,
			Path:       path,
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(respBody)),
		}
	}
	if out != nil && len(respBody) > 0 {
		dec := json.NewDecoder(bytes.NewReader(respBody))
		dec.UseNumber()
		if err := dec.Decode(out); err != nil {
			return fmt.Errorf("decode %s %s: %w (body=%s)", method, path, err, string(respBody))
		}
	}
	return nil
}

// APIError is returned when the data app responds with a non-2xx status.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s %s: %d %s", e.Method, e.Path, e.StatusCode, e.Body)
}

type VaultCredentialSummary struct {
	PublicID string `json:"public_id"`
	Name     string `json:"name"`
}

func (c *Client) VaultListCredentials(ctx context.Context) ([]VaultCredentialSummary, error) {
	var out struct {
		Credentials []VaultCredentialSummary `json:"credentials"`
	}
	if err := c.do(ctx, "GET", "/apps/vault/api/credentials", nil, &out); err != nil {
		return nil, err
	}
	return out.Credentials, nil
}

func (c *Client) VaultGetSecrets(ctx context.Context, publicID string) (map[string]string, error) {
	var out map[string]string
	if err := c.do(ctx, "GET", "/apps/vault/api/credentials/"+publicID+"/secrets", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
