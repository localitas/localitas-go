// Package client provides a database/sql driver for Localitas databases.
//
// Usage:
//
//	import _ "github.com/localitas/localitas/client" // register driver
//
//	db, err := sql.Open("localitas", "http://localhost:8090?database_id=db_123&token=base64...")
//	rows, err := db.QueryContext(ctx, "SELECT id, data FROM contacts WHERE id = ?", id)
package client

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sync"
)

func init() {
	sql.Register("localitas", &localitasDriver{})
}

type localitasDriver struct{}

func (d *localitasDriver) Open(dsn string) (driver.Conn, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid DSN: %w", err)
	}

	baseURL := fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	dbID := u.Query().Get("database_id")
	token := u.Query().Get("token")

	if dbID == "" {
		return nil, fmt.Errorf("database_id is required in DSN query params")
	}

	c := New(baseURL)
	if token != "" {
		c = c.WithToken(token)
	}

	return &localitasConn{client: c, dbID: dbID}, nil
}

type localitasConn struct {
	client   *Client
	dbID     string
	closed   bool
	mu       sync.Mutex
	activeTx *localitasTx
}

func (c *localitasConn) Prepare(query string) (driver.Stmt, error) {
	return &localitasStmt{conn: c, query: query}, nil
}

func (c *localitasConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *localitasConn) Begin() (driver.Tx, error) {
	tx := &localitasTx{conn: c}
	c.activeTx = tx
	return tx, nil
}

type localitasTx struct {
	conn  *localitasConn
	stmts []SQLStatement
}

func (tx *localitasTx) buffer(sql string, args []interface{}) {
	tx.stmts = append(tx.stmts, SQLStatement{SQL: sql, Args: args})
}

func (tx *localitasTx) Commit() error {
	tx.conn.activeTx = nil
	if len(tx.stmts) == 0 {
		return nil
	}
	_, err := tx.conn.client.SQLTransaction(context.Background(), tx.conn.dbID, tx.stmts)
	tx.stmts = nil
	return err
}

func (tx *localitasTx) Rollback() error {
	tx.conn.activeTx = nil
	tx.stmts = nil
	return nil
}

type localitasStmt struct {
	conn  *localitasConn
	query string
}

func (s *localitasStmt) Close() error { return nil }

func (s *localitasStmt) NumInput() int { return -1 }

func (s *localitasStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), valuesToNamed(args))
}

func (s *localitasStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), valuesToNamed(args))
}

func (s *localitasStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	iArgs := namedToInterface(args)
	if tx := s.conn.activeTx; tx != nil {
		tx.buffer(s.query, iArgs)
		return &localitasResult{}, nil
	}
	res, err := s.conn.client.SQLExec(ctx, s.conn.dbID, s.query, iArgs...)
	if err != nil {
		return nil, err
	}
	return &localitasResult{lastID: res.LastInsertID, affected: res.RowsAffected}, nil
}

func (s *localitasStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	iArgs := namedToInterface(args)
	res, err := s.conn.client.SQLQuery(ctx, s.conn.dbID, s.query, iArgs...)
	if err != nil {
		return nil, err
	}
	return &localitasRows{columns: res.Columns, types: res.Types, data: res.Rows, pos: 0}, nil
}

type localitasResult struct {
	lastID   int64
	affected int64
}

func (r *localitasResult) LastInsertId() (int64, error) { return r.lastID, nil }
func (r *localitasResult) RowsAffected() (int64, error) { return r.affected, nil }

type localitasRows struct {
	columns []string
	types   []string
	data    [][]interface{}
	pos     int
}

func (r *localitasRows) Columns() []string { return r.columns }

func (r *localitasRows) Close() error { return nil }

func (r *localitasRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.data) {
		return io.EOF
	}
	row := r.data[r.pos]
	r.pos++
	for i := range dest {
		if i < len(row) {
			colType := ""
			if i < len(r.types) {
				colType = r.types[i]
			}
			dest[i] = jsonToDriverValue(row[i], colType)
		}
	}
	return nil
}

func (r *localitasRows) ColumnTypeDatabaseTypeName(index int) string {
	if index < len(r.types) {
		return r.types[index]
	}
	return ""
}

// jsonToDriverValue converts a JSON-decoded value to a driver.Value using the
// server-reported column type as the authoritative source. json.Number (from
// UseNumber) preserves the original numeric string, avoiding float64 precision
// loss. The column type disambiguates INTEGER vs REAL unambiguously.
func jsonToDriverValue(v interface{}, colType string) driver.Value {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case json.Number:
		switch colType {
		case "INTEGER", "INT", "BIGINT", "SMALLINT", "TINYINT", "BOOLEAN":
			if i, err := val.Int64(); err == nil {
				return i
			}
		case "REAL", "FLOAT", "DOUBLE":
			if f, err := val.Float64(); err == nil {
				return f
			}
		}
		if i, err := val.Int64(); err == nil {
			return i
		}
		if f, err := val.Float64(); err == nil {
			return f
		}
		return val.String()
	case bool:
		if val {
			return int64(1)
		}
		return int64(0)
	default:
		return val
	}
}

func valuesToNamed(args []driver.Value) []driver.NamedValue {
	named := make([]driver.NamedValue, len(args))
	for i, v := range args {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return named
}

func namedToInterface(args []driver.NamedValue) []interface{} {
	out := make([]interface{}, len(args))
	for i, a := range args {
		out[i] = a.Value
	}
	return out
}
