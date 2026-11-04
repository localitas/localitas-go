package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"

	client "github.com/localitas/localitas-go"
)

// sqlCommand runs `localitas sql <database_id> [statement]`: the statement
// given, or else the statements read from stdin (a prompt when it is a
// terminal). A database is any data-API database ID, or sys_<app> for a core
// app's own database (admin only). Every write goes through the cluster's
// exec endpoint, so it is replicated through Raft like any other.
func sqlCommand(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: localitas sql <database_id> [statement]")
		os.Exit(1)
	}
	dbID := args[0]
	c := newClient()
	ctx := context.Background()

	if len(args) > 1 {
		if err := runStatements(ctx, c, dbID, strings.Join(args[1:], " "), os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
		return
	}
	stat, _ := os.Stdin.Stat()
	interactive := stat != nil && stat.Mode()&os.ModeCharDevice != 0
	if err := runSQLShell(ctx, c, dbID, os.Stdin, os.Stdout, interactive); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// runSQLShell runs the statements read from in, each once its closing ';'
// is read. Interactively it prompts and keeps going after a failed
// statement; otherwise the first failure stops it.
func runSQLShell(ctx context.Context, c *client.Client, dbID string, in io.Reader, out io.Writer, interactive bool) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var buf strings.Builder
	prompt := func() {
		if !interactive {
			return
		}
		if buf.Len() == 0 {
			fmt.Fprintf(out, "%s> ", dbID)
		} else {
			fmt.Fprint(out, "   ...> ")
		}
	}
	prompt()
	for scanner.Scan() {
		buf.WriteString(scanner.Text())
		buf.WriteByte('\n')
		if strings.HasSuffix(strings.TrimSpace(buf.String()), ";") {
			err := runStatements(ctx, c, dbID, buf.String(), out)
			buf.Reset()
			if err != nil {
				if !interactive {
					return err
				}
				fmt.Fprintln(out, "Error:", err)
			}
		}
		prompt()
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(buf.String()) != "" {
		return runStatements(ctx, c, dbID, buf.String(), out)
	}
	return nil
}

// runStatements runs each statement in raw in order and prints its result.
func runStatements(ctx context.Context, c *client.Client, dbID, raw string, out io.Writer) error {
	for _, stmt := range splitSQL(raw) {
		if err := runStatement(ctx, c, dbID, stmt, out); err != nil {
			return err
		}
	}
	return nil
}

// runStatement sends a read to the query endpoint and prints its rows, and
// anything else to the exec endpoint, which replicates it. A read the server
// finds writes (a CTE that inserts) goes to exec.
func runStatement(ctx context.Context, c *client.Client, dbID, stmt string, out io.Writer) error {
	if readsOnly(stmt) {
		res, err := c.SQLQuery(ctx, dbID, stmt)
		if err == nil {
			printRows(out, res)
			return nil
		}
		var apiErr *client.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest || !strings.Contains(apiErr.Body, "exec endpoint") {
			return err
		}
	}
	res, err := c.SQLExec(ctx, dbID, stmt)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "OK (%d rows affected)\n", res.RowsAffected)
	return nil
}

// readsOnly reports whether stmt starts the way a read does.
func readsOnly(stmt string) bool {
	fields := strings.Fields(stmt)
	if len(fields) == 0 {
		return false
	}
	switch strings.ToUpper(strings.TrimLeft(fields[0], "(")) {
	case "SELECT", "PRAGMA", "EXPLAIN", "WITH", "VALUES":
		return true
	}
	return false
}

// printRows prints a query result as an aligned table and its row count.
func printRows(out io.Writer, res *client.SQLQueryResult) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(res.Columns, "\t"))
	for _, row := range res.Rows {
		cells := make([]string, len(row))
		for i, v := range row {
			cells[i] = formatValue(v)
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	tw.Flush()
	fmt.Fprintf(out, "(%d rows)\n", len(res.Rows))
}

// formatValue prints a JSON-decoded value as SQLite would show it.
func formatValue(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	default:
		return fmt.Sprint(x)
	}
}
