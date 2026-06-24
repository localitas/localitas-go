package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	client "github.com/localitas/localitas-go"
	_ "github.com/mattn/go-sqlite3"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "migrate":
		if len(os.Args) < 3 {
			printMigrateUsage()
			os.Exit(1)
		}
		switch os.Args[2] {
		case "new":
			if len(os.Args) < 4 {
				fmt.Fprintln(os.Stderr, "Usage: localitas migrate new <name>")
				os.Exit(1)
			}
			migrateNew(os.Args[3])
		case "check":
			migrateCheck()
		case "status":
			migrateStatus()
		case "run":
			migrateRun()
		default:
			printMigrateUsage()
			os.Exit(1)
		}
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage: localitas <command>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  migrate new <name>   Create a new migration file")
	fmt.Fprintln(os.Stderr, "  migrate check        Syntax-check all migration SQL files")
	fmt.Fprintln(os.Stderr, "  migrate status       Show applied vs pending migrations")
	fmt.Fprintln(os.Stderr, "  migrate run          Run all pending migrations")
}

func printMigrateUsage() {
	fmt.Fprintln(os.Stderr, "Usage: localitas migrate <subcommand>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  new <name>   Create migrations/YYYYMMDD-HHMMSS-000-name.sql")
	fmt.Fprintln(os.Stderr, "  check        Syntax-check all SQL files in migrations/")
	fmt.Fprintln(os.Stderr, "  status       Show applied vs pending (requires running server)")
	fmt.Fprintln(os.Stderr, "  run          Run all pending migrations (requires running server)")
}

func migrateNew(name string) {
	name = sanitizeName(name)
	if name == "" {
		fmt.Fprintln(os.Stderr, "Error: name is required")
		os.Exit(1)
	}

	if err := os.MkdirAll("migrations", 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating migrations dir: %v\n", err)
		os.Exit(1)
	}

	now := time.Now().UTC()
	datePrefix := now.Format("20060102") + "-" + now.Format("150405")

	seq := 0
	if entries, err := os.ReadDir("migrations"); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), datePrefix) {
				if m := timestampPattern.FindStringSubmatch(e.Name()); m != nil {
					n := 0
					fmt.Sscanf(m[3], "%d", &n)
					if n >= seq {
						seq = n + 1
					}
				}
			}
		}
	}

	version := fmt.Sprintf("%s-%03d", datePrefix, seq)
	filename := fmt.Sprintf("%s-%s.sql", version, name)
	path := filepath.Join("migrations", filename)

	content := fmt.Sprintf("-- Migration: %s\n-- Created: %s\n\n", name, now.Format(time.RFC3339))

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created %s\n", path)
}

func migrateCheck() {
	files, err := findMigrationFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(files) == 0 {
		fmt.Println("No migration files found in migrations/")
		return
	}

	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening in-memory db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	hasErrors := false
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  FAIL  %s: %v\n", filepath.Base(f), err)
			hasErrors = true
			continue
		}

		stmts := splitSQL(string(data))
		fileOk := true
		for _, stmt := range stmts {
			if _, err := db.Exec(stmt); err != nil {
				fmt.Fprintf(os.Stderr, "  FAIL  %s: %v\n", filepath.Base(f), err)
				hasErrors = true
				fileOk = false
				break
			}
		}
		if fileOk {
			fmt.Printf("  OK    %s\n", filepath.Base(f))
		}
	}

	if hasErrors {
		os.Exit(1)
	}
	fmt.Printf("\nAll %d migrations pass syntax check.\n", len(files))
}

func migrateStatus() {
	appName := detectAppName()
	dbID := "sys_" + appName
	c := newClient()
	ctx := context.Background()

	migrations, err := c.ListDatabaseMigrations(ctx, dbID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing migrations: %v\n", err)
		os.Exit(1)
	}

	applied := make(map[string]bool)
	for _, m := range migrations {
		applied[m.Version] = true
	}

	files, err := findMigrationFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	pending := 0
	for _, f := range files {
		version, _, _ := parseFilename(filepath.Base(f))
		status := "applied"
		if !applied[version] {
			status = "pending"
			pending++
		}
		fmt.Printf("  %-10s %s\n", status, filepath.Base(f))
	}

	fmt.Printf("\n%d applied, %d pending\n", len(files)-pending, pending)
}

func migrateRun() {
	appName := detectAppName()
	dbID := "sys_" + appName
	c := newClient()
	ctx := context.Background()

	applied, err := c.ListDatabaseMigrations(ctx, dbID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing migrations: %v\n", err)
		os.Exit(1)
	}
	appliedSet := make(map[string]bool)
	for _, m := range applied {
		appliedSet[m.Version] = true
	}

	files, err := findMigrationFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ran := 0
	for _, f := range files {
		version, name, err := parseFilename(filepath.Base(f))
		if err != nil {
			fmt.Fprintf(os.Stderr, "  SKIP  %s: %v\n", filepath.Base(f), err)
			continue
		}
		if appliedSet[version] {
			continue
		}

		data, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  FAIL  %s: %v\n", filepath.Base(f), err)
			os.Exit(1)
		}

		_, err = c.ApplyDatabaseMigration(ctx, dbID, version, name, string(data), "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "  FAIL  %s: %v\n", filepath.Base(f), err)
			os.Exit(1)
		}

		fmt.Printf("  OK    %s\n", filepath.Base(f))
		ran++
	}

	if ran == 0 {
		fmt.Println("No pending migrations.")
	} else {
		fmt.Printf("\n%d migration(s) applied.\n", ran)
	}
}

func newClient() *client.Client {
	return client.New(client.DefaultCoreURL()).WithToken(client.DefaultToken())
}

func detectAppName() string {
	data, err := os.ReadFile("go.mod")
	if err == nil {
		lines := strings.Split(string(data), "\n")
		if len(lines) > 0 {
			mod := strings.TrimPrefix(lines[0], "module ")
			mod = strings.TrimSpace(mod)
			parts := strings.Split(mod, "/")
			name := parts[len(parts)-1]
			name = strings.TrimPrefix(name, "localitas-app-")
			name = strings.TrimPrefix(name, "localitas-")
			return name
		}
	}

	dir, _ := os.Getwd()
	base := filepath.Base(dir)
	base = strings.TrimPrefix(base, "localitas-app-")
	base = strings.TrimPrefix(base, "localitas-")
	return base
}

func findMigrationFiles() ([]string, error) {
	if _, err := os.Stat("migrations"); os.IsNotExist(err) {
		return nil, fmt.Errorf("no migrations/ directory found")
	}

	entries, err := os.ReadDir("migrations")
	if err != nil {
		return nil, err
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, filepath.Join("migrations", e.Name()))
		}
	}
	return files, nil
}

var (
	timestampPattern = regexp.MustCompile(`^(\d{8})-(\d{6})-(\d{3})-(.+)\.sql$`)
	legacyPattern    = regexp.MustCompile(`^(\d+)_(.+)\.sql$`)
)

func parseFilename(filename string) (string, string, error) {
	if matches := timestampPattern.FindStringSubmatch(filename); matches != nil {
		version := fmt.Sprintf("%s-%s-%s", matches[1], matches[2], matches[3])
		return version, matches[4], nil
	}
	if matches := legacyPattern.FindStringSubmatch(filename); matches != nil {
		return matches[1], matches[2], nil
	}
	return "", "", fmt.Errorf("invalid migration filename: %s", filename)
}

func sanitizeName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "-", "_")
	return name
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
