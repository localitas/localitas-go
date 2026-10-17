package client

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	_ "github.com/mattn/go-sqlite3"
)

func migrationsFS(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, sql := range files {
		fsys["migrations/"+name] = &fstest.MapFile{Data: []byte(sql)}
	}
	return fsys
}

func openMigratorDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=10000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func queryString(t *testing.T, db *sql.DB, q string) string {
	t.Helper()
	rows, err := db.Query(q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return strings.Join(out, ",")
}

var seedMigrations = map[string]string{
	"20260101-000000-000-tables.sql": `CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT);`,
	"20260102-000000-000-seed.sql":   `INSERT INTO items (name) VALUES ('default');`,
	"20260103-000000-000-column.sql": `ALTER TABLE items ADD COLUMN color TEXT;`,
}

// Several nodes booting at once migrate the same database: each migration,
// including a non-idempotent data migration, is applied exactly once, and
// every node's Run succeeds.
func TestMigrator_ConcurrentRunsApplyEachMigrationOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.sqlitedb")
	fsys := migrationsFS(seedMigrations)
	if _, err := openMigratorDB(t, path).Exec("SELECT 1"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		db := openMigratorDB(t, path)
		m, err := NewMigrator(db, fsys, "app")
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = m.Run(context.Background())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("runner %d: %v", i, err)
		}
	}
	db := openMigratorDB(t, path)
	if got := queryString(t, db, "SELECT name FROM items"); got != "default" {
		t.Fatalf("seed rows = %q, want exactly one 'default'", got)
	}
	if got := queryString(t, db, "SELECT version FROM schema_migrations ORDER BY version"); got != "20260101-000000-000,20260102-000000-000,20260103-000000-000" {
		t.Fatalf("recorded versions = %q", got)
	}
}

// A migration whose later statement fails leaves nothing behind: no partial
// schema change and no version row, so the fixed migration applies cleanly.
func TestMigrator_FailedMigrationChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.sqlitedb")
	db := openMigratorDB(t, path)
	broken := migrationsFS(map[string]string{
		"20260101-000000-000-tables.sql": `CREATE TABLE items (id INTEGER PRIMARY KEY);`,
		"20260102-000000-000-two.sql":    `ALTER TABLE items ADD COLUMN a TEXT; INSERT INTO missing_table VALUES (1);`,
	})
	m, _ := NewMigrator(db, broken, "app")
	err := m.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no such table: missing_table") {
		t.Fatalf("got %v, want the failing statement's error", err)
	}
	if got := queryString(t, db, "SELECT name FROM pragma_table_info('items') ORDER BY name"); got != "id" {
		t.Fatalf("columns after failed migration = %q, want the first statement rolled back", got)
	}
	if got := queryString(t, db, "SELECT version FROM schema_migrations"); got != "20260101-000000-000" {
		t.Fatalf("versions after failed migration = %q", got)
	}

	fixed := migrationsFS(map[string]string{
		"20260101-000000-000-tables.sql": `CREATE TABLE items (id INTEGER PRIMARY KEY);`,
		"20260102-000000-000-two.sql":    `ALTER TABLE items ADD COLUMN a TEXT;`,
	})
	m, _ = NewMigrator(db, fixed, "app")
	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("fixed migration: %v", err)
	}
	if got := queryString(t, db, "SELECT name FROM pragma_table_info('items') ORDER BY name"); got != "a,id" {
		t.Fatalf("columns = %q", got)
	}
}

// A database whose schema change landed without its version row (migrated
// before migrations were atomic) gets the version recorded, not an error.
func TestMigrator_RecordsSchemaAlreadyInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.sqlitedb")
	db := openMigratorDB(t, path)
	if _, err := db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, color TEXT)`); err != nil {
		t.Fatal(err)
	}
	m, _ := NewMigrator(db, migrationsFS(map[string]string{
		"20260101-000000-000-tables.sql": `CREATE TABLE IF NOT EXISTS items (id INTEGER PRIMARY KEY, name TEXT);`,
		"20260103-000000-000-column.sql": `ALTER TABLE items ADD COLUMN color TEXT;`,
	}), "app")
	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := queryString(t, db, "SELECT version FROM schema_migrations ORDER BY version"); got != "20260101-000000-000,20260103-000000-000" {
		t.Fatalf("versions = %q", got)
	}
	if err := m.Run(context.Background()); err != nil {
		t.Fatalf("second run must be a no-op: %v", err)
	}
}

// A statement that is wrong, not merely already applied, fails the run with
// the database's own message.
func TestMigrator_ReportsBadSQL(t *testing.T) {
	db := openMigratorDB(t, filepath.Join(t.TempDir(), "app.sqlitedb"))
	m, _ := NewMigrator(db, migrationsFS(map[string]string{
		"20260101-000000-000-bad.sql": `CREATE TABLEX items (id INTEGER PRIMARY KEY);`,
	}), "app")
	err := m.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "20260101-000000-000_bad") || !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("got %v, want the migration name and SQLite's syntax error", err)
	}
}

// The race between booting nodes, made deterministic: a node that read the
// applied versions before a peer applied a migration then applies it too.
// Its transaction fails on the version and changes nothing, and Run treats
// the migration as done.
func TestMigrator_StaleNodeDoesNotReapply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.sqlitedb")
	fsys := migrationsFS(seedMigrations)
	first, _ := NewMigrator(openMigratorDB(t, path), fsys, "app")
	if err := first.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	stale, _ := NewMigrator(openMigratorDB(t, path), fsys, "app")
	for _, mig := range stale.migrations {
		err := stale.apply(context.Background(), mig)
		if err == nil || !isVersionTaken(err) {
			t.Fatalf("re-applying %s: got %v, want the version already taken", mig.Version, err)
		}
	}
	db := openMigratorDB(t, path)
	if got := queryString(t, db, "SELECT name FROM items"); got != "default" {
		t.Fatalf("seed rows = %q, want exactly one", got)
	}
	if got := queryString(t, db, "SELECT COUNT(*) FROM pragma_table_info('items') WHERE name = 'color'"); got != "1" {
		t.Fatalf("color columns = %q", got)
	}
}
