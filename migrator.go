package client

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"path"
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
func NewMigrator(db *sql.DB, migrationsFS fs.FS, appName string) (*Migrator, error) {
	migrations, err := loadMigrationsFromFS(migrationsFS)
	if err != nil {
		return nil, fmt.Errorf("failed to load migrations: %w", err)
	}
	return &Migrator{db: db, migrations: migrations}, nil
}

// Run applies every pending migration. Each migration is one transaction
// that records its version before running its statements, so it applies
// whole or not at all, and of several nodes migrating the same database at
// once exactly one applies it: the others' transactions fail on the
// version's primary key and change nothing.
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

	applied, err := m.appliedVersions(ctx)
	if err != nil {
		return err
	}

	for _, migration := range m.migrations {
		if applied[migration.Version] {
			continue
		}
		err := m.apply(ctx, migration)
		switch {
		case err == nil:
			log.Printf("✅ Applied migration %s_%s", migration.Version, migration.Name)
		case isVersionTaken(err):
			log.Printf("Migration %s_%s was applied by another node", migration.Version, migration.Name)
		case isMigrationIdempotentError(err):
			// The schema change is already in place without its version
			// row (a database migrated before migrations were atomic).
			if err := m.repair(ctx, migration); err != nil {
				return err
			}
			log.Printf("✅ Recorded migration %s_%s whose schema was already in place", migration.Version, migration.Name)
		default:
			return fmt.Errorf("migration %s_%s failed: %w", migration.Version, migration.Name, err)
		}
	}

	return nil
}

func (m *Migrator) appliedVersions(ctx context.Context) (map[string]bool, error) {
	rows, err := m.db.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("failed to get applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("failed to read applied migrations: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read applied migrations: %w", err)
	}
	return applied, nil
}

// apply runs migration as one transaction whose first statement records its
// version.
func (m *Migrator) apply(ctx context.Context, migration Migration) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)
	`, migration.Version, migration.Name, time.Now().UTC().Unix()); err != nil {
		tx.Rollback()
		return err
	}
	for _, stmt := range splitSQL(migration.SQL) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// repair applies migration statement by statement, skipping schema changes
// that are already in place, and records its version.
func (m *Migrator) repair(ctx context.Context, migration Migration) error {
	for _, stmt := range splitSQL(migration.SQL) {
		if _, err := m.db.ExecContext(ctx, stmt); err != nil && !isMigrationIdempotentError(err) {
			return fmt.Errorf("migration %s_%s failed: %w", migration.Version, migration.Name, err)
		}
	}
	if _, err := m.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)
	`, migration.Version, migration.Name, time.Now().UTC().Unix()); err != nil {
		return fmt.Errorf("failed to record migration %s: %w", migration.Version, err)
	}
	return nil
}

// isVersionTaken reports whether a migration's transaction failed because
// another node recorded the same version first.
func isVersionTaken(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed: schema_migrations.version")
}

func loadMigrationsFromFS(migrationsFS fs.FS) ([]Migration, error) {
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

		sqlBytes, err := fs.ReadFile(migrationsFS, path.Join("migrations", entry.Name()))
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
