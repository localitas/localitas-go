package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestDefault_WithoutCoreURLFailsClearly(t *testing.T) {
	t.Setenv("CORE_URL", "")
	t.Setenv("LOCALITAS_HOME", t.TempDir())
	t.Setenv("LOCALITAS_API_TOKEN", "lt_app")
	_, err := Default()
	if !errors.Is(err, ErrNoCoreURL) || !strings.Contains(err.Error(), "CORE_URL") {
		t.Fatalf("Default() without CORE_URL = %v, want ErrNoCoreURL naming CORE_URL", err)
	}
}

func TestDefault_WithoutTokenFailsClearly(t *testing.T) {
	t.Setenv("CORE_URL", "http://core.test:1234")
	t.Setenv("LOCALITAS_HOME", t.TempDir())
	t.Setenv("LOCALITAS_API_TOKEN", "")
	if _, err := Default(); err == nil || !strings.Contains(err.Error(), "LOCALITAS_API_TOKEN") {
		t.Fatalf("Default() without a token = %v, want an error naming LOCALITAS_API_TOKEN", err)
	}
}

func TestDefault_RefusesACoreURLThatIsNotAnAddress(t *testing.T) {
	t.Setenv("LOCALITAS_API_TOKEN", "lt_app")
	for _, u := range []string{"localhost:8090", "/apps", "ftp://core.test", "http://"} {
		t.Setenv("CORE_URL", u)
		if _, err := Default(); err == nil || !strings.Contains(err.Error(), "CORE_URL") {
			t.Fatalf("Default() with CORE_URL %q = %v, want an error naming CORE_URL", u, err)
		}
	}
}

func TestDefault_UsesCoreURLAndToken(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	t.Setenv("CORE_URL", srv.URL+"/")
	t.Setenv("LOCALITAS_API_TOKEN", "lt_app")
	c, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListDatabases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer lt_app" || gotPath != "/apps/data/api/databases" {
		t.Fatalf("request %s with %q, want /apps/data/api/databases with the app's token", gotPath, gotAuth)
	}
}

// A client with no core address fails every call with the same clear error,
// never a request to a guessed or relative address.
func TestNoCoreURL_EveryCallFailsClearly(t *testing.T) {
	t.Setenv("CORE_URL", "")
	t.Setenv("LOCALITAS_HOME", t.TempDir())
	c := New(DefaultCoreURL()).WithToken("lt_app")
	if _, err := c.ListDatabases(context.Background()); !errors.Is(err, ErrNoCoreURL) {
		t.Fatalf("call with no core URL = %v, want ErrNoCoreURL", err)
	}
	if _, err := c.Download(context.Background(), "/x", &bytes.Buffer{}); !errors.Is(err, ErrNoCoreURL) {
		t.Fatalf("download with no core URL = %v, want ErrNoCoreURL", err)
	}
}

// guessedAddresses are literals that would make the SDK send a request to
// an address nobody gave it.
var guessedAddresses = []string{"8090", "8080", "localhost", "127.0.0.1", "host.docker.internal", "ws://", "wss://"}

// allowedAddressLiterals are the SDK's own addresses that are not guesses,
// by file and literal.
var allowedAddressLiterals = map[string][]string{
	// The app tells core where it listens on its own machine (Announce).
	"announce.go": {"http://localhost:%d"},
	// A container reaches core's machine by Docker's name for it; a loopback
	// core URL is recognized to be rewritten to it (ContainerURL), not dialed.
	"core_env.go": {"host.docker.internal", "localhost"},
}

// No string literal outside the allowlist names a host, a port or a socket
// scheme: the SDK reaches core only at the URL it was given.
func TestNoGuessedAddressesInSource(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for _, g := range guessedAddresses {
				if strings.Contains(s, g) && !slices.Contains(allowedAddressLiterals[name], s) {
					t.Errorf("%s: literal %q names an address (%s)", name, s, g)
				}
			}
			return true
		})
	}
}

func TestApp_CallsTheAppThroughCoresProxyWithTheClientsToken(t *testing.T) {
	type seen struct{ method, uri, auth, body string }
	got := make(chan seen, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		b.ReadFrom(r.Body)
		got <- seen{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), b.String()}
		w.Write([]byte(`{"verdict":"clean"}`))
	}))
	defer srv.Close()
	app := New(srv.URL).WithToken("lt_email").App("antivirus")

	var out struct {
		Verdict string `json:"verdict"`
	}
	if err := app.Post(context.Background(), "/api/scan?deep=1", map[string]string{"file": "a.txt"}, &out); err != nil {
		t.Fatal(err)
	}
	s := <-got
	if s.method != "POST" || s.uri != "/apps/ext/antivirus/api/scan?deep=1" || s.auth != "Bearer lt_email" || s.body != `{"file":"a.txt"}` {
		t.Fatalf("app call = %+v", s)
	}
	if out.Verdict != "clean" {
		t.Fatalf("answer = %+v", out)
	}
	for _, call := range []func() error{
		func() error { return app.Get(context.Background(), "/health.json", nil) },
		func() error { return app.Put(context.Background(), "/api/x", struct{}{}, nil) },
		func() error { return app.Delete(context.Background(), "/api/x", nil) },
	} {
		if err := call(); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"GET /apps/ext/antivirus/health.json", "PUT /apps/ext/antivirus/api/x", "DELETE /apps/ext/antivirus/api/x"} {
		s := <-got
		if s.method+" "+s.uri != want || s.auth != "Bearer lt_email" {
			t.Fatalf("app call = %+v, want %s", s, want)
		}
	}
}

func TestApp_RefusesAnythingButANameAndAPath(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	c := New(srv.URL)
	cases := []struct{ app, path string }{
		{"", "/x"}, {"Anti", "/x"}, {"a/b", "/x"}, {"../data", "/x"},
		{"antivirus", ""}, {"antivirus", "x"}, {"antivirus", "//evil.test/x"},
		{"antivirus", "http://evil.test/x"}, {"antivirus", `/a\b`},
	}
	for _, tc := range cases {
		if err := c.App(tc.app).Get(context.Background(), tc.path, nil); !errors.Is(err, ErrNotAnAppCall) {
			t.Errorf("App(%q).Get(%q) = %v, want ErrNotAnAppCall", tc.app, tc.path, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("%d refused calls reached core", calls.Load())
	}
}

func TestCacheSocket_SchemeFollowsTheClientsURL(t *testing.T) {
	cases := map[string]string{
		"http://core.test:9090":        "ws://core.test:9090/apps/cache/ws/my%20cache",
		"https://kerabat.example/":     "wss://kerabat.example/apps/cache/ws/my%20cache",
		"https://tunnel.example/base/": "wss://tunnel.example/base/apps/cache/ws/my%20cache",
	}
	for base, want := range cases {
		if got := New(base).cacheSocketURL("my cache"); got != want {
			t.Errorf("socket URL for %s = %s, want %s", base, got, want)
		}
	}
}

// Subscribe connects to the cache's socket on the client's core with its
// token and delivers what is published; later subscriptions on the same
// cache share the one socket.
func TestCacheSubscribe_ConnectsAndDelivers(t *testing.T) {
	type handshake struct{ path, auth string }
	conns := make(chan handshake, 4)
	requests := make(chan wsOutgoing, 8)
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conns <- handshake{r.URL.Path, r.Header.Get("Authorization")}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var req wsOutgoing
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			requests <- req
			if req.Action == "subscribe" {
				conn.WriteJSON(wsIncoming{Type: "message", Channel: req.Channel, Seq: 7, Value: "hello"})
			}
		}
	}))
	defer srv.Close()

	cache := New(srv.URL).WithToken("lt_app").Cache("events")
	got := make(chan PubSubMessage, 1)
	ps := cache.Subscribe("orders", "worker-1", func(m PubSubMessage) { got <- m })
	defer ps.Close()

	h := <-conns
	if h.path != "/apps/cache/ws/events" || h.auth != "Bearer lt_app" {
		t.Fatalf("socket opened %s with %q", h.path, h.auth)
	}
	select {
	case m := <-got:
		if m.Seq != 7 || m.Value != "hello" {
			t.Fatalf("delivered %+v", m)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("nothing delivered")
	}
	if again := cache.WatchLatest("audit", func(PubSubMessage) {}); again != ps {
		t.Fatal("a second subscription on the cache opened another socket")
	}
	for {
		select {
		case req := <-requests:
			if req.Action == "watch" && req.Channel == "audit" && req.FromLatest {
				return
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the latest-watch was not sent on the shared socket")
		}
	}
}

// dataFake serves the data API's system-dbs, query and exec routes over a
// key/value table, counting how often the name was resolved.
type dataFake struct {
	mu       sync.Mutex
	rows     map[string]string
	resolves int
	dbPaths  []string
	auth     []string
}

func (f *dataFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	switch {
	case r.Method == "POST" && r.URL.Path == "/apps/data/api/system-dbs":
		var req struct {
			Name string `json:"name"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		f.resolves++
		json.NewEncoder(w).Encode(Database{ID: "db_" + req.Name, Name: req.Name, System: true})
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/exec"):
		f.dbPaths = append(f.dbPaths, r.URL.Path)
		var req sqlExecRequest
		json.NewDecoder(r.Body).Decode(&req)
		f.rows[req.Args[0].(string)] = req.Args[1].(string)
		json.NewEncoder(w).Encode(SQLExecResult{RowsAffected: 1})
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/query"):
		f.dbPaths = append(f.dbPaths, r.URL.Path)
		var req sqlQueryRequest
		json.NewDecoder(r.Body).Decode(&req)
		res := SQLQueryResult{Columns: []string{"v"}, Types: []string{"TEXT"}}
		if v, ok := f.rows[req.Args[0].(string)]; ok {
			res.Rows = [][]any{{v}}
		}
		json.NewEncoder(w).Encode(res)
	default:
		http.NotFound(w, r)
	}
}

func TestDB_OpensTheNamedSystemDatabase(t *testing.T) {
	fake := &dataFake{rows: map[string]string{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()

	db, err := New(srv.URL).WithToken("lt_app").DB(ctx, "notes")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "INSERT INTO kv (k, v) VALUES (?, ?)", "greeting", "hi"); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := db.QueryRowContext(ctx, "SELECT v FROM kv WHERE k = ?", "greeting").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "hi" {
		t.Fatalf("read %q, want what was written", v)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.resolves != 1 {
		t.Fatalf("name resolved %d times, want once", fake.resolves)
	}
	for _, p := range fake.dbPaths {
		if !strings.HasPrefix(p, "/apps/data/api/databases/db_notes/") {
			t.Fatalf("statement sent to %s, not the named database", p)
		}
	}
	for _, a := range fake.auth {
		if a != "Bearer lt_app" {
			t.Fatalf("call made with %q, want the client's token", a)
		}
	}
}

// A core that cannot answer yet (its data shard electing) does not stop an
// app from opening its database: the name is resolved at first use.
func TestDB_ResolvesLaterWhenCoreIsNotReady(t *testing.T) {
	fake := &dataFake{rows: map[string]string{"k": "v"}}
	var ready atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			http.Error(w, "no leader", http.StatusServiceUnavailable)
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()
	ctx := context.Background()

	db, err := New(srv.URL).WithToken("lt_app").DB(ctx, "notes")
	if err != nil {
		t.Fatalf("DB while core is not ready = %v, want the database to resolve later", err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRowContext(ctx, "SELECT v FROM kv WHERE k = ?", "k").Scan(&v); err == nil {
		t.Fatal("a query before core is ready succeeded")
	}
	ready.Store(true)
	if err := db.QueryRowContext(ctx, "SELECT v FROM kv WHERE k = ?", "k").Scan(&v); err != nil || v != "v" {
		t.Fatalf("query once core is ready = %q, %v", v, err)
	}
}

func TestDB_RefusedNameIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "admin only", http.StatusForbidden)
	}))
	defer srv.Close()
	if _, err := New(srv.URL).WithToken("lt_user").DB(context.Background(), "notes"); err == nil {
		t.Fatal("DB refused by core returned no error")
	}
	if _, err := New(srv.URL).DB(context.Background(), ""); err == nil {
		t.Fatal("DB with no name returned no error")
	}
}

type keyedCompletion struct {
	Path           string `json:"-"`
	Status         string `json:"status"`
	IdempotencyKey string `json:"idempotency_key"`
}

func keyedCompletionServer(t *testing.T) (*Client, <-chan keyedCompletion) {
	t.Helper()
	got := make(chan keyedCompletion, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p keyedCompletion
		json.NewDecoder(r.Body).Decode(&p)
		p.Path = r.URL.Path
		got <- p
		w.Write([]byte(`{"status":"completed"}`))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL), got
}

// RunAsync echoes the Idempotency-Key core called with, so core ends that
// try and no other.
func TestRunAsync_EchoesTheCallsIdempotencyKey(t *testing.T) {
	c, got := keyedCompletionServer(t)
	req := httptest.NewRequest("POST", "/sync", nil)
	req.Header.Set(AutomationRunIDHeader, "run-9")
	req.Header.Set(IdempotencyKeyHeader, "run-9/sync/2")
	if !RunAsync(c, httptest.NewRecorder(), req, func(ctx context.Context) (scanSummary, error) { return scanSummary{}, nil }) {
		t.Fatal("RunAsync did not take the call")
	}
	select {
	case p := <-got:
		if p.Path != "/apps/automation/api/runs/run-9/complete" || p.IdempotencyKey != "run-9/sync/2" {
			t.Fatalf("completion = %+v, want the call's key echoed", p)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no completion")
	}
}

func TestPublishResult_RunOnlyCarriesNoKey(t *testing.T) {
	c, got := keyedCompletionServer(t)
	if err := c.Automation().PublishResult(context.Background(), "run-9", "completed", nil, ""); err != nil {
		t.Fatal(err)
	}
	if p := <-got; p.IdempotencyKey != "" || p.Status != "completed" {
		t.Fatalf("run-only completion = %+v", p)
	}
	call := AsyncCall{RunID: "run-9", IdempotencyKey: "run-9/a/1"}
	if err := c.Automation().Complete(context.Background(), call, "failed", nil, "no"); err != nil {
		t.Fatal(err)
	}
	if p := <-got; p.IdempotencyKey != "run-9/a/1" || p.Status != "failed" {
		t.Fatalf("keyed completion = %+v", p)
	}
}
