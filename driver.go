// Package client provides a database/sql driver for Localitas databases.
//
// Usage:
//
//	c, _ := client.Default()
//	db, err := c.DB(ctx, "myapp") // the app's system database, by name
//	rows, err := db.QueryContext(ctx, "SELECT id, data FROM contacts WHERE id = ?", id)
//
// The DSN form sql.Open("localitas", "<core URL>?database_id=<id>&token=<t>")
// still works until the cleanup slice.
package client

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sync"
)

func init() {
	sql.Register("localitas", &localitasDriver{})
}

type localitasDriver struct{}

// DB is the system database named name (the app's own, created on first
// use by core, idempotently by name), as a *sql.DB on this client: its core
// and its token, no DSN. The name is resolved to the database's ID now when
// core answers; when core cannot yet (no leader, not listening, a 5xx) the
// DB is returned all the same and resolves at its first use, so an app
// started while core's data shard elects keeps going. A refusal (a 4xx, such
// as a token that may not create system databases) is the error.
func (c *Client) DB(ctx context.Context, name string) (*sql.DB, error) {
	if name == "" {
		return nil, errors.New("DB needs the database's name")
	}
	k := &dbConnector{client: c, name: name}
	if _, err := k.databaseID(ctx); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 && apiErr.StatusCode != 429 {
			return nil, fmt.Errorf("database %s: %w", name, err)
		}
		if errors.Is(err, ErrNoCoreURL) {
			return nil, err
		}
	}
	return sql.OpenDB(k), nil
}

// OpenDatabase is the database with ID id as a *sql.DB on this client. It
// makes no call until it is used.
func (c *Client) OpenDatabase(id string) *sql.DB {
	return sql.OpenDB(&dbConnector{client: c, id: id})
}

// dbConnector opens connections to one database on a client, its ID known
// or resolved from its name once.
type dbConnector struct {
	client *Client
	name   string

	mu sync.Mutex
	id string
}

func (k *dbConnector) databaseID(ctx context.Context) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.id != "" {
		return k.id, nil
	}
	db, err := k.client.CreateSystemDatabase(ctx, k.name)
	if err != nil {
		return "", err
	}
	if db.ID == "" {
		return "", fmt.Errorf("database %s: core named no ID", k.name)
	}
	k.id = db.ID
	return k.id, nil
}

func (k *dbConnector) Connect(ctx context.Context) (driver.Conn, error) {
	id, err := k.databaseID(ctx)
	if err != nil {
		return nil, err
	}
	return &localitasConn{client: k.client, dbID: id}, nil
}

func (k *dbConnector) Driver() driver.Driver { return &localitasDriver{} }

// clientCache holds one *Client per (baseURL, token). sql.DB opens a new
// driver.Conn per pooled connection; without this each would build its own
// *Client. Sharing one client per endpoint keeps a single pooled http transport
// and avoids re-creating the client on every connection.
var clientCache sync.Map // key "baseURL\x00token" -> *Client

func sharedClient(baseURL, token string) *Client {
	key := baseURL + "\x00" + token
	if v, ok := clientCache.Load(key); ok {
		return v.(*Client)
	}
	c := New(baseURL)
	if token != "" {
		c = c.WithToken(token)
	}
	actual, _ := clientCache.LoadOrStore(key, c)
	return actual.(*Client)
}

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

	return &localitasConn{client: sharedClient(baseURL, token), dbID: dbID}, nil
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
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

// BeginTx starts a transaction whose statements are sent together on
// Commit, under the precondition WithUnlessExists put on ctx, if any.
func (c *localitasConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	tx := &localitasTx{conn: c, ctx: ctx}
	if u, ok := ctx.Value(unlessExistsKey{}).(SQLStatement); ok {
		tx.unlessExists = &u
	}
	c.activeTx = tx
	return tx, nil
}

// ErrSkipped is what Commit returns for a transaction begun under
// WithUnlessExists whose query found a row: nothing was applied, and nothing
// failed.
var ErrSkipped = errors.New("transaction skipped: its unless-exists query found a row")

type unlessExistsKey struct{}

// WithUnlessExists returns a context under which a transaction begun on a
// localitas connection (db.BeginTx) applies only unless query returns a
// row. Core evaluates the query inside the replicated transaction on every
// node, so of nodes committing the same conditional transaction at once
// exactly one applies it; the others' Commit returns ErrSkipped. Drivers
// other than localitas ignore it.
func WithUnlessExists(ctx context.Context, query string, args ...any) context.Context {
	return context.WithValue(ctx, unlessExistsKey{}, SQLStatement{SQL: query, Args: args})
}

type localitasTx struct {
	conn *localitasConn
	// ctx is the context the transaction began with: database/sql's Commit
	// takes none, so the commit, which sends every statement, ends with it.
	ctx          context.Context
	stmts        []SQLStatement
	unlessExists *SQLStatement
}

func (tx *localitasTx) buffer(sql string, args []interface{}) {
	tx.stmts = append(tx.stmts, SQLStatement{SQL: sql, Args: args})
}

func (tx *localitasTx) Commit() error {
	tx.conn.activeTx = nil
	if len(tx.stmts) == 0 {
		return nil
	}
	stmts := tx.stmts
	tx.stmts = nil
	if tx.unlessExists == nil {
		_, err := tx.conn.client.SQLTransaction(tx.ctx, tx.conn.dbID, stmts)
		return err
	}
	res, err := tx.conn.client.SQLTransactionUnlessExists(tx.ctx, tx.conn.dbID, *tx.unlessExists, stmts)
	if err != nil {
		return err
	}
	if res.Skipped {
		return ErrSkipped
	}
	return nil
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
