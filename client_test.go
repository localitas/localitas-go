package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDefaultCoreURL_ReturnsLocalhost(t *testing.T) {
	u := DefaultCoreURL()
	if runtime.GOOS != "linux" {
		if u != "http://localhost:"+DefaultCorePort {
			t.Fatalf("expected localhost URL on non-linux, got %s", u)
		}
	}
}

func TestIsContainer_NonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("skipping on linux where detection may vary")
	}
	if isContainer() {
		t.Fatal("expected false on non-linux host")
	}
}

func TestDefaultToken_ReadsFile(t *testing.T) {
	dir := t.TempDir()
	tokenDir := filepath.Join(dir, ".localitas")
	os.MkdirAll(tokenDir, 0755)
	os.WriteFile(filepath.Join(tokenDir, "api-token"), []byte("lt_test123\n"), 0600)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	token := DefaultToken()
	if token != "lt_test123" {
		t.Fatalf("expected lt_test123, got %q", token)
	}
}

func TestDefaultToken_MissingFile(t *testing.T) {
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	token := DefaultToken()
	if token != "" {
		t.Fatalf("expected empty token, got %q", token)
	}
}

func TestWithToken_SetsAuthorizationHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]Database{})
	}))
	defer srv.Close()

	c := New(srv.URL).WithToken("test-bearer-token")
	_, err := c.ListDatabases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer test-bearer-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer test-bearer-token")
	}
}

func TestWithHTTPClient_UsesCustomClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]Database{})
	}))
	defer srv.Close()

	custom := &http.Client{Timeout: 5 * time.Second}
	c := New(srv.URL).WithHTTPClient(custom)
	_, err := c.ListDatabases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestListDatabases(t *testing.T) {
	dbs := []Database{
		{ID: "db1", Name: "mydb", OwnerID: "u1"},
		{ID: "db2", Name: "other", OwnerID: "u2"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/apps/data/api/databases" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(dbs)
	}))
	defer srv.Close()

	c := New(srv.URL).WithToken("tok")
	got, err := c.ListDatabases(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 databases, got %d", len(got))
	}
	if got[0].ID != "db1" || got[1].Name != "other" {
		t.Errorf("unexpected database data: %+v", got)
	}
}

func TestCreateDatabase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/apps/data/api/databases" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "newdb" {
			t.Errorf("expected name=newdb, got %v", body["name"])
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Database{ID: "db-new", Name: "newdb", OwnerID: "u1"})
	}))
	defer srv.Close()

	c := New(srv.URL).WithToken("tok")
	db, err := c.CreateDatabase(context.Background(), "newdb")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if db.ID != "db-new" {
		t.Errorf("expected ID=db-new, got %s", db.ID)
	}
	if db.Name != "newdb" {
		t.Errorf("expected Name=newdb, got %s", db.Name)
	}
}

func TestSQLQuery(t *testing.T) {
	result := SQLQueryResult{
		Columns: []string{"id", "name"},
		Types:   []string{"INTEGER", "TEXT"},
		Rows:    [][]interface{}{{1, "alice"}, {2, "bob"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/apps/data/api/databases/db1/query" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}))
	defer srv.Close()

	c := New(srv.URL).WithToken("tok")
	got, err := c.SQLQuery(context.Background(), "db1", "SELECT id, name FROM users")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Columns) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(got.Columns))
	}
	if len(got.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got.Rows))
	}
}

func TestSQLExec(t *testing.T) {
	result := SQLExecResult{LastInsertID: 42, RowsAffected: 1}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/apps/data/api/databases/db1/exec" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}))
	defer srv.Close()

	c := New(srv.URL).WithToken("tok")
	got, err := c.SQLExec(context.Background(), "db1", "INSERT INTO users (name) VALUES (?)", "charlie")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.LastInsertID != 42 {
		t.Errorf("expected LastInsertID=42, got %d", got.LastInsertID)
	}
	if got.RowsAffected != 1 {
		t.Errorf("expected RowsAffected=1, got %d", got.RowsAffected)
	}
}

func TestIngestMetrics(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apps/tsdb/api/ingest" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected application/json, got %s", r.Header.Get("Content-Type"))
		}
		var req struct {
			Metrics []MetricPoint `json:"metrics"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"accepted": len(req.Metrics)})
	}))
	defer ts.Close()

	c := New(ts.URL)
	now := time.Now().UTC()
	accepted, err := c.IngestMetrics(context.Background(), []MetricPoint{
		{Name: "test.metric", Value: 42.0, Tags: map[string]string{"host": "web-1"}, Timestamp: &now},
		{Name: "test.other", Value: 10.0},
	})
	if err != nil {
		t.Fatalf("IngestMetrics failed: %v", err)
	}
	if accepted != 2 {
		t.Errorf("expected 2 accepted, got %d", accepted)
	}
}

func TestIngestDogStatsD(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apps/tsdb/api/ingest" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "text/plain" {
			t.Errorf("expected text/plain, got %s", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"accepted": 2})
	}))
	defer ts.Close()

	c := New(ts.URL)
	accepted, err := c.IngestDogStatsD(context.Background(), "cpu.usage:75.5|g|#host:web-1\nmem.free:2048|g|#host:web-1")
	if err != nil {
		t.Fatalf("IngestDogStatsD failed: %v", err)
	}
	if accepted != 2 {
		t.Errorf("expected 2 accepted, got %d", accepted)
	}
}
