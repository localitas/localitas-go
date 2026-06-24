package client

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Migration represents a database schema migration loaded from an embedded SQL file.
type Migration struct {
	Version string
	Name    string
	SQL     string
}

// Migrator runs schema migrations against any *sql.DB, including the localitas
// database/sql driver. Migrations are loaded from an embedded filesystem and
// tracked in a schema_migrations table.
//
// Usage:
//
//	//go:embed migrations/*.sql
//	var migrationsFS embed.FS
//
//	db, _ := sql.Open("localitas", "http://localhost:8080?database_id=sys_myapp&token=...")
//	m, _ := client.NewMigrator(db, migrationsFS, "myapp")
//	m.Run(context.Background())
type Migrator struct {
	db         *sql.DB
	migrations []Migration
}

var (
	timestampPattern = regexp.MustCompile(`^(\d{8})-(\d{6})-(\d{3})-(.+)\.sql$`)
	legacyPattern    = regexp.MustCompile(`^(\d+)_(.+)\.sql$`)
)

// NewMigrator creates a Migrator from an embedded migrations directory.
// The migrationsFS should contain a "migrations/" directory with SQL files
// named using the timestamp format: YYYYMMDD-HHMMSS-MMM-description.sql
func NewMigrator(db *sql.DB, migrationsFS embed.FS, appName string) (*Migrator, error) {
	migrations, err := loadMigrationsFromFS(migrationsFS)
	if err != nil {
		return nil, fmt.Errorf("failed to load migrations: %w", err)
	}
	return &Migrator{db: db, migrations: migrations}, nil
}

// Run executes all pending migrations. Idempotent — already-applied migrations
// are skipped. Each migration SQL is split into individual statements and
// executed sequentially. The schema_migrations table is created automatically.
func (m *Migrator) Run(ctx context.Context) error {
	if _, err := m.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	rows, err := m.db.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("failed to get applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		rows.Scan(&v)
		applied[v] = true
	}

	for _, migration := range m.migrations {
		if applied[migration.Version] {
			continue
		}
		log.Printf("Applying migration %s_%s...", migration.Version, migration.Name)

		stmts := splitSQL(migration.SQL)
		for _, stmt := range stmts {
			if _, err := m.db.ExecContext(ctx, stmt); err != nil {
				if !isMigrationIdempotentError(err) {
					return fmt.Errorf("migration %s_%s failed: %w", migration.Version, migration.Name, err)
				}
				log.Printf("⚠️  Migration %s_%s: %s (skipping)", migration.Version, migration.Name, err.Error())
			}
		}

		if _, err := m.db.ExecContext(ctx, `
			INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)
		`, migration.Version, migration.Name, time.Now().UTC().Unix()); err != nil {
			return fmt.Errorf("failed to record migration %s: %w", migration.Version, err)
		}

		log.Printf("✅ Applied migration %s_%s", migration.Version, migration.Name)
	}

	return nil
}

func loadMigrationsFromFS(migrationsFS embed.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations directory: %w", err)
	}

	var migrations []Migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		version, name, err := parseMigrationFilename(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("invalid migration filename %s: %w", entry.Name(), err)
		}

		sqlBytes, err := fs.ReadFile(migrationsFS, filepath.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("failed to read migration %s: %w", entry.Name(), err)
		}

		migrations = append(migrations, Migration{
			Version: version,
			Name:    name,
			SQL:     string(sqlBytes),
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	return migrations, nil
}

func parseMigrationFilename(filename string) (string, string, error) {
	if matches := timestampPattern.FindStringSubmatch(filename); matches != nil {
		version := fmt.Sprintf("%s-%s-%s", matches[1], matches[2], matches[3])
		return version, matches[4], nil
	}
	if matches := legacyPattern.FindStringSubmatch(filename); matches != nil {
		return matches[1], matches[2], nil
	}
	return "", "", fmt.Errorf("filename must match YYYYMMDD-HHMMSS-MMM-name.sql or NNN_name.sql")
}

func splitSQL(raw string) []string {
	var stmts []string
	var buf strings.Builder
	depth := 0
	inString := false
	var stringChar byte

	for i := 0; i < len(raw); i++ {
		ch := raw[i]

		if inString {
			buf.WriteByte(ch)
			if ch == stringChar {
				if i+1 < len(raw) && raw[i+1] == stringChar {
					buf.WriteByte(raw[i+1])
					i++
				} else {
					inString = false
				}
			}
			continue
		}

		if ch == '\'' || ch == '"' {
			inString = true
			stringChar = ch
			buf.WriteByte(ch)
			continue
		}

		if ch == '-' && i+1 < len(raw) && raw[i+1] == '-' {
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			buf.WriteByte('\n')
			continue
		}

		upper := strings.ToUpper(raw[i:])
		if strings.HasPrefix(upper, "BEGIN") && (i+5 >= len(raw) || !isIdentChar(raw[i+5])) {
			depth++
		}
		if strings.HasPrefix(upper, "END") && (i+3 >= len(raw) || !isIdentChar(raw[i+3])) && depth > 0 {
			depth--
		}

		if ch == ';' && depth == 0 {
			s := strings.TrimSpace(buf.String())
			if s != "" {
				stmts = append(stmts, s)
			}
			buf.Reset()
			continue
		}

		buf.WriteByte(ch)
	}

	if s := strings.TrimSpace(buf.String()); s != "" {
		stmts = append(stmts, s)
	}
	return stmts
}

func isIdentChar(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_'
}

func isMigrationIdempotentError(err error) bool {
	s := err.Error()
	return strings.Contains(s, "duplicate column name") ||
		(strings.Contains(s, "table") && strings.Contains(s, "already exists")) ||
		(strings.Contains(s, "index") && strings.Contains(s, "already exists"))
}
