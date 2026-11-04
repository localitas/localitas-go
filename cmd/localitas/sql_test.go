package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	client "github.com/localitas/localitas-go"
)

// fakeDataAPI stands in for core's data API: it records which endpoint each
// statement reached and answers as core does, refusing a write sent to query.
type fakeDataAPI struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeDataAPI) client(t *testing.T) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /apps/data/api/databases/{db}/query", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ SQL string }
		json.NewDecoder(r.Body).Decode(&req)
		f.record("query " + r.PathValue("db") + ": " + req.SQL)
		if strings.Contains(strings.ToUpper(req.SQL), "INSERT") {
			http.Error(w, `{"error":"write statements must be sent to the exec endpoint, which replicates them"}`, http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(client.SQLQueryResult{
			Columns: []string{"id", "name", "size"},
			Rows:    [][]interface{}{{float64(1), "a.txt", nil}, {float64(2), "b.txt", 2.5}},
		})
	})
	mux.HandleFunc("POST /apps/data/api/databases/{db}/exec", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ SQL string }
		json.NewDecoder(r.Body).Decode(&req)
		f.record("exec " + r.PathValue("db") + ": " + req.SQL)
		if strings.Contains(req.SQL, "nosuch") {
			http.Error(w, `{"error":"no such table: nosuch"}`, http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(client.SQLExecResult{RowsAffected: 3})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return client.New(srv.URL).WithToken("admin-token")
}

func (f *fakeDataAPI) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

// A read goes to query and prints a table; a write goes to exec, which
// replicates it, and prints the rows it changed.
func TestSQL_ReadsAreQueriedAndWritesExecuted(t *testing.T) {
	f := &fakeDataAPI{}
	c := f.client(t)
	var out bytes.Buffer

	if err := runStatements(context.Background(), c, "sys_filesystem", "SELECT id, name, size FROM files; DROP TABLE IF EXISTS s3_credentials", &out); err != nil {
		t.Fatal(err)
	}
	want := []string{"query sys_filesystem: SELECT id, name, size FROM files", "exec sys_filesystem: DROP TABLE IF EXISTS s3_credentials"}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
	for _, line := range []string{"id  name   size", "1   a.txt  NULL", "2   b.txt  2.5", "(2 rows)", "OK (3 rows affected)"} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output lacks %q:\n%s", line, out.String())
		}
	}
}

// A statement that starts like a read but writes (a CTE that inserts) is
// sent to exec once query refuses it.
func TestSQL_AWritingCTEGoesToExec(t *testing.T) {
	f := &fakeDataAPI{}
	var out bytes.Buffer

	if err := runStatements(context.Background(), f.client(t), "db_1", "WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x", &out); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || !strings.HasPrefix(f.calls[0], "query") || !strings.HasPrefix(f.calls[1], "exec") {
		t.Errorf("calls = %q, want query then exec", f.calls)
	}
	if !strings.Contains(out.String(), "OK (3 rows affected)") {
		t.Errorf("output = %q", out.String())
	}
}

// Statements piped in run as each one's ';' arrives, across lines; the
// first failure stops a piped run.
func TestSQLShell_RunsPipedStatementsAndStopsAtAFailure(t *testing.T) {
	f := &fakeDataAPI{}
	in := strings.NewReader("UPDATE files\n  SET name = 'x';\nDELETE FROM nosuch;\nDELETE FROM files;\n")
	var out bytes.Buffer

	err := runSQLShell(context.Background(), f.client(t), "sys_filesystem", in, &out, false)
	if err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("err = %v, want the failed statement's error", err)
	}
	want := []string{"exec sys_filesystem: UPDATE files\n  SET name = 'x'", "exec sys_filesystem: DELETE FROM nosuch"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
}

// Interactively a failed statement is reported and the shell goes on.
func TestSQLShell_InteractiveKeepsGoingAfterAFailure(t *testing.T) {
	f := &fakeDataAPI{}
	in := strings.NewReader("DELETE FROM nosuch;\nSELECT id FROM files;\n")
	var out bytes.Buffer

	if err := runSQLShell(context.Background(), f.client(t), "sys_filesystem", in, &out, true); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || !strings.Contains(out.String(), "Error:") || !strings.Contains(out.String(), "(2 rows)") || !strings.Contains(out.String(), "sys_filesystem> ") {
		t.Errorf("calls %q, output:\n%s", f.calls, out.String())
	}
}
