package client

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDriver_QueryAndExec(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /apps/data/api/databases/testdb/query", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SQLQueryResult{
			Columns: []string{"id", "name"},
			Types:   []string{"INTEGER", "TEXT"},
			Rows: [][]interface{}{
				{float64(1), "Alice"},
				{float64(2), "Bob"},
			},
		})
	})

	mux.HandleFunc("POST /apps/data/api/databases/testdb/exec", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SQLExecResult{
			LastInsertID: 3,
			RowsAffected: 1,
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	dsn := srv.URL + "?database_id=testdb&token=test123"
	db, err := sql.Open("localitas", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	rows, err := db.Query("SELECT id, name FROM people")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if count == 0 && id != 1 {
			t.Errorf("expected first id=1, got %d", id)
		}
		count++
	}
	if count != 2 {
		t.Errorf("expected 2 rows, got %d", count)
	}

	result, err := db.Exec("INSERT INTO people (name) VALUES (?)", "Carol")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	lastID, _ := result.LastInsertId()
	if lastID != 3 {
		t.Errorf("expected lastInsertId=3, got %d", lastID)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		t.Errorf("expected rowsAffected=1, got %d", affected)
	}
}

func TestDriver_TypeAwareConversion(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /apps/data/api/databases/testdb/query", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SQLQueryResult{
			Columns: []string{"id", "score", "name"},
			Types:   []string{"INTEGER", "REAL", "TEXT"},
			Rows: [][]interface{}{
				{float64(42), float64(9.0), "alice"},
			},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	db, _ := sql.Open("localitas", srv.URL+"?database_id=testdb")
	defer db.Close()

	rows, err := db.Query("SELECT id, score, name FROM items")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()

	rows.Next()

	var id int64
	var score float64
	var name string
	if err := rows.Scan(&id, &score, &name); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if id != 42 {
		t.Errorf("expected id=42 (int64), got %d", id)
	}
	// score=9.0 is a whole number but column type is REAL → must stay float64
	if score != 9.0 {
		t.Errorf("expected score=9.0 (float64), got %f", score)
	}
	if name != "alice" {
		t.Errorf("expected name=alice, got %q", name)
	}
}

func TestDriver_ColumnTypeDatabaseTypeName(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /apps/data/api/databases/testdb/query", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SQLQueryResult{
			Columns: []string{"id", "data", "score"},
			Types:   []string{"INTEGER", "TEXT", "REAL"},
			Rows:    [][]interface{}{},
		})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	db, _ := sql.Open("localitas", srv.URL+"?database_id=testdb")
	defer db.Close()

	rows, err := db.Query("SELECT id, data, score FROM items")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()

	colTypes, err := rows.ColumnTypes()
	if err != nil {
		t.Fatalf("ColumnTypes: %v", err)
	}
	if len(colTypes) != 3 {
		t.Fatalf("expected 3 column types, got %d", len(colTypes))
	}

	expected := []string{"INTEGER", "TEXT", "REAL"}
	for i, ct := range colTypes {
		if ct.DatabaseTypeName() != expected[i] {
			t.Errorf("column %d: expected type %q, got %q", i, expected[i], ct.DatabaseTypeName())
		}
	}
}

func TestDriver_Transaction(t *testing.T) {
	var receivedStmts []map[string]interface{}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /apps/data/api/databases/testdb/exec", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if stmts, ok := body["statements"].([]interface{}); ok {
			for _, s := range stmts {
				receivedStmts = append(receivedStmts, s.(map[string]interface{}))
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(SQLExecResult{RowsAffected: int64(len(stmts))})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SQLExecResult{RowsAffected: 1})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	db, err := sql.Open("localitas", srv.URL+"?database_id=testdb")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	tx.Exec("INSERT INTO orders (id) VALUES (?)", "o1")
	tx.Exec("INSERT INTO items (order_id, name) VALUES (?, ?)", "o1", "widget")

	if len(receivedStmts) != 0 {
		t.Error("statements should be buffered, not sent before Commit")
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if len(receivedStmts) != 2 {
		t.Fatalf("expected 2 statements sent on Commit, got %d", len(receivedStmts))
	}
	if receivedStmts[0]["sql"] != "INSERT INTO orders (id) VALUES (?)" {
		t.Errorf("stmt[0] sql = %v", receivedStmts[0]["sql"])
	}
	if receivedStmts[1]["sql"] != "INSERT INTO items (order_id, name) VALUES (?, ?)" {
		t.Errorf("stmt[1] sql = %v", receivedStmts[1]["sql"])
	}
}

func TestDriver_TransactionRollback(t *testing.T) {
	sent := false
	mux := http.NewServeMux()
	mux.HandleFunc("POST /apps/data/api/databases/testdb/exec", func(w http.ResponseWriter, r *http.Request) {
		sent = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SQLExecResult{RowsAffected: 1})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	db, _ := sql.Open("localitas", srv.URL+"?database_id=testdb")
	defer db.Close()

	tx, _ := db.Begin()
	tx.Exec("INSERT INTO orders (id) VALUES (?)", "o1")
	tx.Rollback()

	if sent {
		t.Error("Rollback should discard buffered statements, not send them")
	}
}

func TestDriver_TransactionEmpty(t *testing.T) {
	sent := false
	mux := http.NewServeMux()
	mux.HandleFunc("POST /apps/data/api/databases/testdb/exec", func(w http.ResponseWriter, r *http.Request) {
		sent = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SQLExecResult{})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	db, _ := sql.Open("localitas", srv.URL+"?database_id=testdb")
	defer db.Close()

	tx, _ := db.Begin()
	if err := tx.Commit(); err != nil {
		t.Fatalf("empty Commit should succeed: %v", err)
	}

	if sent {
		t.Error("empty transaction should not send any request")
	}
}

func TestDriver_InvalidDSN(t *testing.T) {
	_, err := sql.Open("localitas", "not-a-url")
	if err != nil {
		t.Fatalf("sql.Open should not fail on registration: %v", err)
	}
	// The actual error happens on first use since sql.Open is lazy.
	// Ping forces a connection attempt.
	db, _ := sql.Open("localitas", "http://localhost:1/?database_id=")
	err = db.Ping()
	if err == nil {
		t.Error("expected error for empty database_id")
	}
}
