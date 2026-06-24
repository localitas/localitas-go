// Package client is the Go SDK for the Localitas platform HTTP API.
//
// It is the contract between the platform and apps built on top of it. Apps call
// methods on Client; Client issues HTTP requests with a bearer token. The SDK
// never stores credentials or validates tokens — that is the responsibility of
// the upstream reverse proxy.
//
// Quick start:
//
//	c := client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
//	secrets, _ := c.VaultGetSecrets(ctx, "my-credential-id")
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
	// DefaultCorePort is the default HTTP port for the Localitas core server.
	DefaultCorePort = "8090"
)

// DefaultToken reads the API token from ~/.localitas/config-core.yaml
// (core.auth.api_token). Returns empty string if the file is missing
// or does not contain a valid token (must start with "lt_").
func DefaultToken() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return readAPITokenFromConfig(home + "/.localitas/config-core.yaml")
}

func readAPITokenFromConfig(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "api_token:") {
			val := strings.TrimPrefix(trimmed, "api_token:")
			val = strings.TrimSpace(val)
			val = strings.Trim(val, "\"'")
			if strings.HasPrefix(val, "lt_") {
				return val
			}
		}
	}
	return ""
}

// DefaultCoreURL returns the base URL for the Localitas core server.
// Inside Docker containers it returns http://host.docker.internal:8090,
// otherwise http://localhost:8090.
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

// Client is the Localitas platform API client. It is safe for concurrent use.
// Use WithToken to set per-request authentication.
type Client struct {
	baseURL string
	http    *http.Client
	token   string
}

// New creates a Client bound to the given base URL (e.g. "http://localhost:8090").
// The returned client has no token set — call WithToken before making authenticated requests.
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    http.DefaultClient,
	}
}

// WithHTTPClient returns a shallow copy of the client using the given http.Client.
// Use this to set custom timeouts or transport for specific operations.
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	clone := *c
	clone.http = h
	return &clone
}

// WithToken returns a shallow copy of the client that sends the given bearer
// token on every request. Typically called per incoming HTTP request:
//
//	appClient := client.New(url).WithToken(client.TokenFromRequest(r))
func (c *Client) WithToken(bearer string) *Client {
	clone := *c
	clone.token = bearer
	return &clone
}

// ----- Databases -------------------------------------------------------------

// ListDatabases returns all databases accessible to the authenticated user.
func (c *Client) ListDatabases(ctx context.Context) ([]Database, error) {
	var out []Database
	if err := c.do(ctx, "GET", "/apps/data/api/databases", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateDatabase creates a new user-owned database with the given name.
func (c *Client) CreateDatabase(ctx context.Context, name string) (*Database, error) {
	var out Database
	if err := c.do(ctx, "POST", "/apps/data/api/databases", map[string]any{"name": name}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateSystemDatabase creates a system database visible to all users.
// Idempotent by name. Requires admin role.
func (c *Client) CreateSystemDatabase(ctx context.Context, name string) (*Database, error) {
	var out Database
	if err := c.do(ctx, "POST", "/apps/data/api/databases", map[string]any{"name": name, "system": true}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetDatabase returns a single database by ID.
func (c *Client) GetDatabase(ctx context.Context, id string) (*Database, error) {
	var out Database
	if err := c.do(ctx, "GET", "/apps/data/api/databases/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteDatabase permanently deletes a database and all its tables/rows.
func (c *Client) DeleteDatabase(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/apps/data/api/databases/"+url.PathEscape(id), nil, nil)
}

// ----- Migrations ------------------------------------------------------------

// ListDatabaseMigrations returns all migrations applied to a database.
func (c *Client) ListDatabaseMigrations(ctx context.Context, dbID string) ([]DatabaseMigration, error) {
	var out []DatabaseMigration
	path := fmt.Sprintf("/apps/data/api/databases/%s/migrations", url.PathEscape(dbID))
	if err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ApplyDatabaseMigration applies a schema migration to the database.
// Idempotent by (dbID, version) — re-applying the same version is a no-op.
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

// ListTables returns all tables in a database.
func (c *Client) ListTables(ctx context.Context, dbID string) ([]Table, error) {
	var out []Table
	path := fmt.Sprintf("/apps/data/api/databases/%s/tables", url.PathEscape(dbID))
	if err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// InsertRow inserts a new row into a table with the given column values.
func (c *Client) InsertRow(ctx context.Context, dbID, tableID string, values map[string]interface{}) (*Row, error) {
	var out Row
	path := fmt.Sprintf("/apps/data/api/databases/%s/tables/%s/rows",
		url.PathEscape(dbID), url.PathEscape(tableID))
	if err := c.do(ctx, "POST", path, map[string]any{"values": values}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateRow updates an existing row's column values.
func (c *Client) UpdateRow(ctx context.Context, dbID, tableID, rowID string, values map[string]interface{}) error {
	path := fmt.Sprintf("/apps/data/api/databases/%s/tables/%s/rows/%s",
		url.PathEscape(dbID), url.PathEscape(tableID), url.PathEscape(rowID))
	return c.do(ctx, "PUT", path, map[string]any{"values": values}, nil)
}

// DeleteRow permanently deletes a row by ID.
func (c *Client) DeleteRow(ctx context.Context, dbID, tableID, rowID string) error {
	path := fmt.Sprintf("/apps/data/api/databases/%s/tables/%s/rows/%s",
		url.PathEscape(dbID), url.PathEscape(tableID), url.PathEscape(rowID))
	return c.do(ctx, "DELETE", path, nil, nil)
}

// ListRows returns rows from a table with pagination.
func (c *Client) ListRows(ctx context.Context, dbID, tableID string, limit, offset int) ([]Row, error) {
	var out []Row
	path := fmt.Sprintf("/apps/data/api/databases/%s/tables/%s/rows?limit=%d&offset=%d",
		url.PathEscape(dbID), url.PathEscape(tableID), limit, offset)
	if err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetRow returns a single row by ID.
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

// SQLExec executes a write SQL statement (INSERT, UPDATE, DELETE, CREATE TABLE, etc.)
// against the database. Returns the number of rows affected.
func (c *Client) SQLExec(ctx context.Context, dbID, sql string, args ...interface{}) (*SQLExecResult, error) {
	var out SQLExecResult
	path := fmt.Sprintf("/apps/data/api/databases/%s/exec", url.PathEscape(dbID))
	body := map[string]any{"sql": sql, "args": args}
	if err := c.do(ctx, "POST", path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SQLQuery executes a read SQL statement (SELECT) against the database.
// Returns columns, types, and row data.
func (c *Client) SQLQuery(ctx context.Context, dbID, sql string, args ...interface{}) (*SQLQueryResult, error) {
	var out SQLQueryResult
	path := fmt.Sprintf("/apps/data/api/databases/%s/query", url.PathEscape(dbID))
	body := map[string]any{"sql": sql, "args": args}
	if err := c.do(ctx, "POST", path, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SQLTransaction executes multiple SQL statements as an atomic transaction.
// Either all statements succeed or none are applied.
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

// SearchOptions configures a search call. Zero values mean "no restriction".
type SearchOptions struct {
	// DatabaseID restricts results to rows owned by this database. Empty = all databases.
	DatabaseID string
	// Limit caps the number of returned results. <= 0 means server default (100).
	Limit int
}

// SearchFTS runs a full-text keyword search (FTS5) over the authenticated user's data.
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

// SearchHybrid runs hybrid search combining full-text and vector similarity (RRF-merged).
// Falls back to FTS when no embedder is configured on the server.
// The search mode is reported in SearchResponse.Mode.
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

// RegisterExternalApp registers an external app with the platform so it
// appears in the app selector UI. The app must be reachable at appURL.
func (c *Client) RegisterExternalApp(ctx context.Context, name, displayName, appURL, icon string) error {
	body := map[string]string{
		"name":         name,
		"display_name": displayName,
		"url":          appURL,
		"icon":         icon,
	}
	return c.do(ctx, "POST", "/apps/ext", body, nil)
}

// RegisterService registers a named service URL in the platform's service registry.
// Other apps can discover it via DiscoverService. Idempotent by name.
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

// DiscoverService looks up a service URL by name from the platform's service registry.
// Returns an error if the service is not registered.
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

// ListServices returns all registered services in the platform's service registry.
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

// ResourcePermission represents an access control entry for a shared resource.
type ResourcePermission struct {
	App          string `json:"app"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	OwnerID      string `json:"owner_id"`
	Permission   string `json:"permission"`
}

// ResourceMember represents a user or group with a permission level on a resource.
type ResourceMember struct {
	UserID     string `json:"user_id,omitempty"`
	GroupID    string `json:"group_id,omitempty"`
	Permission string `json:"permission"`
}

// SetResourceOwner sets the owner of a resource. The owner has implicit admin access.
func (c *Client) SetResourceOwner(ctx context.Context, app, resourceType, resourceID, ownerID string) error {
	return c.do(ctx, "POST", "/api/permissions/set-owner", map[string]string{
		"app": app, "resource_type": resourceType, "resource_id": resourceID, "owner_id": ownerID,
	}, nil)
}

// CheckPermission returns the effective permission level (read, write, admin, or "")
// that a user has on a resource, combining direct user and group permissions.
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

// ListResourceMembers returns all users and groups that have been granted
// access to a resource.
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

// AddResourceMember grants a user or group access to a resource.
// Set either UserID or GroupID (not both) on the member.
func (c *Client) AddResourceMember(ctx context.Context, app, resourceType, resourceID string, member ResourceMember) error {
	path := fmt.Sprintf("/api/permissions/%s/%s/%s/members",
		url.PathEscape(app), url.PathEscape(resourceType), url.PathEscape(resourceID))
	return c.do(ctx, "POST", path, member, nil)
}

// RemoveResourceMember revokes a user's or group's access to a resource.
// Provide either userID or groupID (not both).
func (c *Client) RemoveResourceMember(ctx context.Context, app, resourceType, resourceID, userID, groupID string) error {
	path := fmt.Sprintf("/api/permissions/%s/%s/%s/members",
		url.PathEscape(app), url.PathEscape(resourceType), url.PathEscape(resourceID))
	return c.do(ctx, "DELETE", path, map[string]string{"user_id": userID, "group_id": groupID}, nil)
}

// GetResourceOwner returns the owner ID of a resource.
func (c *Client) GetResourceOwner(ctx context.Context, app, resourceType, resourceID string) (string, error) {
	var result struct {
		OwnerID string `json:"owner_id"`
	}
	path := fmt.Sprintf("/api/permissions/%s/%s/%s/owner",
		url.PathEscape(app), url.PathEscape(resourceType), url.PathEscape(resourceID))
	if err := c.do(ctx, "GET", path, nil, &result); err != nil {
		return "", err
	}
	return result.OwnerID, nil
}

// DeleteResourcePermissions removes all permission entries for a resource.
func (c *Client) DeleteResourcePermissions(ctx context.Context, app, resourceType, resourceID string) error {
	path := fmt.Sprintf("/api/permissions/%s/%s/%s",
		url.PathEscape(app), url.PathEscape(resourceType), url.PathEscape(resourceID))
	return c.do(ctx, "DELETE", path, nil, nil)
}

// ListAccessibleResources returns all resources of a given type that the
// authenticated user can access (owned + shared via user/group grants).
func (c *Client) ListAccessibleResources(ctx context.Context, app, resourceType string) ([]ResourcePermission, error) {
	var result struct {
		Resources []ResourcePermission `json:"resources"`
	}
	path := fmt.Sprintf("/api/permissions/accessible?app=%s&resource_type=%s",
		url.QueryEscape(app), url.QueryEscape(resourceType))
	if err := c.do(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}
	return result.Resources, nil
}

// GetUserGroupIDs returns all group IDs that a user belongs to.
func (c *Client) GetUserGroupIDs(ctx context.Context, userID string) ([]string, error) {
	var result struct {
		GroupIDs []string `json:"group_ids"`
	}
	path := fmt.Sprintf("/api/users/%s/groups", url.PathEscape(userID))
	if err := c.do(ctx, "GET", path, nil, &result); err != nil {
		return nil, err
	}
	return result.GroupIDs, nil
}

// UserSummary represents basic user info.
type UserSummary struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// ListUsers returns all users in the system.
func (c *Client) ListUsers(ctx context.Context) ([]UserSummary, error) {
	var result struct {
		Users []UserSummary `json:"users"`
	}
	if err := c.do(ctx, "GET", "/api/users", nil, &result); err != nil {
		return nil, err
	}
	return result.Users, nil
}

// UserGroup represents a user group.
type UserGroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListGroups returns all user groups.
func (c *Client) ListGroups(ctx context.Context) ([]UserGroup, error) {
	var result struct {
		Groups []UserGroup `json:"groups"`
	}
	if err := c.do(ctx, "GET", "/api/groups", nil, &result); err != nil {
		return nil, err
	}
	return result.Groups, nil
}

// ----- Vault -----------------------------------------------------------------

// VaultCredentialSummary is a credential's metadata without its secret values.
type VaultCredentialSummary struct {
	PublicID string `json:"public_id"`
	Name     string `json:"name"`
}

// VaultListCredentials returns all credentials accessible to the authenticated user.
func (c *Client) VaultListCredentials(ctx context.Context) ([]VaultCredentialSummary, error) {
	var out struct {
		Credentials []VaultCredentialSummary `json:"credentials"`
	}
	if err := c.do(ctx, "GET", "/apps/vault/api/credentials", nil, &out); err != nil {
		return nil, err
	}
	return out.Credentials, nil
}

// VaultGetSecrets returns the decrypted key-value pairs for a credential.
// The credential is identified by its public ID.
func (c *Client) VaultGetSecrets(ctx context.Context, publicID string) (map[string]string, error) {
	var out map[string]string
	if err := c.do(ctx, "GET", "/apps/vault/api/credentials/"+publicID+"/secrets", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// VaultCreateCredential creates a new encrypted credential.
func (c *Client) VaultCreateCredential(ctx context.Context, name, credURL string, keychainSync bool, data interface{}) (*VaultCredentialSummary, error) {
	var out VaultCredentialSummary
	if err := c.do(ctx, "POST", "/apps/vault/api/credentials", map[string]interface{}{
		"name": name, "url": credURL, "keychain_sync": keychainSync, "data": data,
	}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// VaultUpdateCredential updates an existing credential's name, URL, and secret data.
func (c *Client) VaultUpdateCredential(ctx context.Context, publicID, name, credURL string, keychainSync bool, data interface{}) error {
	return c.do(ctx, "PUT", "/apps/vault/api/credentials/"+url.PathEscape(publicID), map[string]interface{}{
		"name": name, "url": credURL, "keychain_sync": keychainSync, "data": data,
	}, nil)
}

// VaultDeleteCredential deletes a credential by its public ID.
func (c *Client) VaultDeleteCredential(ctx context.Context, publicID string) error {
	return c.do(ctx, "DELETE", "/apps/vault/api/credentials/"+url.PathEscape(publicID), nil, nil)
}

// ----- Metrics ---------------------------------------------------------------

// MetricPoint represents a single time series data point for TSDB ingestion.
type MetricPoint struct {
	// Name is the metric name (e.g. "http.request.duration").
	Name string `json:"name"`
	// Value is the numeric value of the metric.
	Value float64 `json:"value"`
	// Type is the metric type: "gauge", "counter", "histogram", etc.
	Type string `json:"type,omitempty"`
	// Tags are key-value labels for the metric (e.g. {"method": "GET", "status": "200"}).
	Tags map[string]string `json:"tags,omitempty"`
	// Timestamp is the metric timestamp. If nil, the server uses the current time.
	Timestamp *time.Time `json:"timestamp,omitempty"`
}

// IngestMetrics sends structured metric points to the TSDB.
// Returns the number of accepted points.
func (c *Client) IngestMetrics(ctx context.Context, metrics []MetricPoint) (int, error) {
	var out struct {
		Accepted int `json:"accepted"`
	}
	if err := c.do(ctx, "POST", "/apps/tsdb/api/ingest", map[string]interface{}{"metrics": metrics}, &out); err != nil {
		return 0, err
	}
	return out.Accepted, nil
}

// IngestDogStatsD sends metrics in DogStatsD text format.
// Each line follows the format: metric.name:value|type|#tag1:val1,tag2:val2
// Returns the number of accepted metric lines.
func (c *Client) IngestDogStatsD(ctx context.Context, lines string) (int, error) {
	reqURL := c.baseURL + "/apps/tsdb/api/ingest"
	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, strings.NewReader(lines))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "text/plain")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("ingest failed (%d): %s", resp.StatusCode, string(body))
	}

	var out struct {
		Accepted int `json:"accepted"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return out.Accepted, nil
}

// ----- Transport -------------------------------------------------------------

// Do makes an authenticated API call to an arbitrary endpoint. Use this for
// app-specific endpoints not covered by named methods. body is JSON-marshalled
// if non-nil. out is JSON-decoded from the response body if non-nil.
//
//	var functions []FaaSFunction
//	err := c.Do(ctx, "GET", "/apps/faas/api/functions", nil, &functions)
func (c *Client) Do(ctx context.Context, method, path string, body any, out any) error {
	return c.do(ctx, method, path, body, out)
}

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

// APIError is returned when the server responds with HTTP 4xx or 5xx.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

// Error returns a human-readable error string including the HTTP method, path,
// status code, and response body.
func (e *APIError) Error() string {
	return fmt.Sprintf("%s %s: %d %s", e.Method, e.Path, e.StatusCode, e.Body)
}
