package client

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// A SQLValue is written as the scalar it holds was written when rows were
// maps of interface{} values, HTML escaping included and left out alike; a
// blob is the text it holds (the bytes were base64).
func TestSQLValue_WiresAsItsScalar(t *testing.T) {
	when := time.Date(2026, 11, 22, 10, 30, 0, 5, time.UTC)
	for _, c := range []struct {
		v      SQLValue
		scalar any
	}{
		{SQLNull(), nil},
		{SQLInt(-42), int64(-42)},
		{SQLInt(9007199254740993), int64(9007199254740993)},
		{SQLReal(2.5), 2.5},
		{SQLReal(3), 3.0},
		{SQLReal(1e21), 1e21},
		{SQLReal(0.000001), 0.000001},
		{SQLText("a<b>&c   é"), "a<b>&c   é"},
		{SQLBlob([]byte("x<y")), "x<y"},
		{SQLBool(true), true},
		{SQLBool(false), false},
		{SQLTime(when), when},
	} {
		for _, escape := range []bool{true, false} {
			if got, want := encode(t, c.v, escape), encode(t, c.scalar, escape); got != want {
				t.Errorf("%#v (escape HTML %v): got %s, want %s", c.scalar, escape, got, want)
			}
		}
	}
}

func encode(t *testing.T, v any, escapeHTML bool) string {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(escapeHTML)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// A JSON value decodes as the SQL scalar it binds as: an integer exactly
// when it is one in range, a real otherwise, text when no float64 holds it.
// An object or array is no SQL value.
func TestSQLValue_DecodesJSONScalars(t *testing.T) {
	for in, want := range map[string]SQLValue{
		`null`:                 SQLNull(),
		`true`:                 SQLBool(true),
		`false`:                SQLBool(false),
		`"hi"`:                 SQLText("hi"),
		"\"\\u003cb\\u003e\"":  SQLText("<b>"),
		`7`:                    SQLInt(7),
		`-9007199254740993`:    SQLInt(-9007199254740993),
		`3.0`:                  SQLReal(3),
		`1e2`:                  SQLReal(100),
		`18446744073709551616`: SQLReal(18446744073709551616),
		`1e400`:                SQLText("1e400"),
	} {
		var got SQLValue
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%s decoded as %#v, want %#v", in, got, want)
		}
	}
	for _, in := range []string{`{"a":1}`, `[1]`} {
		var got SQLValue
		if err := json.Unmarshal([]byte(in), &got); err == nil {
			t.Errorf("%s decoded as %#v, want an error", in, got)
		}
	}
}

// Rows' values and the bodies that write them are the objects the maps were.
func TestRowWire(t *testing.T) {
	var row Row
	if err := json.Unmarshal([]byte(`{"id":"1","table_id":"t1","values":{"id":1,"label":"alpha","score":2.5,"note":null,"ok":true}}`), &row); err != nil {
		t.Fatal(err)
	}
	want := map[string]SQLValue{"id": SQLInt(1), "label": SQLText("alpha"), "score": SQLReal(2.5), "note": SQLNull(), "ok": SQLBool(true)}
	for k, v := range want {
		if row.Values[k] != v {
			t.Errorf("values[%s] = %#v, want %#v", k, row.Values[k], v)
		}
	}
	if label, ok := row.Values["label"].Text(); !ok || label != "alpha" {
		t.Errorf("label text = %q %v", label, ok)
	}
	if id, ok := row.Values["id"].Int64(); !ok || id != 1 {
		t.Errorf("id = %d %v", id, ok)
	}

	for _, c := range []struct {
		v    any
		want string
	}{
		{RowValues{Values: map[string]SQLValue{"qty": SQLInt(3), "label": SQLText("alpha"), "gone": SQLNull()}}, `{"values":{"gone":null,"label":"alpha","qty":3}}`},
		{databaseNameRequest{Name: "notes"}, `{"name":"notes"}`},
		{databaseMigrationRequest{Version: "001", Description: "init", UpSQL: "CREATE TABLE a (x)", DownSQL: "DROP TABLE a"}, `{"description":"init","down_sql":"DROP TABLE a","up_sql":"CREATE TABLE a (x)","version":"001"}`},
		{ShellExecuteRequest{Code: "print(x)", Variables: map[string]json.RawMessage{"x": json.RawMessage(`42`), "cfg": json.RawMessage(`{"a":[1,"b"]}`)}, Requirements: []string{"numpy"}}, `{"code":"print(x)","variables":{"cfg":{"a":[1,"b"]},"x":42},"requirements":["numpy"]}`},
		{ShellExecuteRequest{Code: "print(1)"}, `{"code":"print(1)"}`},
	} {
		got, err := json.Marshal(c.v)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != c.want {
			t.Errorf("on the wire:\n got %s\nwant %s", got, c.want)
		}
	}
}

// A SQLValue binds as a statement argument and scans back from SQLite as the
// value it was, a blob as the bytes it holds.
func TestSQLValue_BindsAndScans(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "v.sqlitedb"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE v (n, i INTEGER, r REAL, s TEXT, b BLOB, flag BOOLEAN)`); err != nil {
		t.Fatal(err)
	}
	in := []any{SQLNull(), SQLInt(9007199254740993), SQLReal(2.5), SQLText("hi"), SQLBlob([]byte{0, 1, 'a'}), SQLBool(true)}
	if _, err := db.Exec(`INSERT INTO v VALUES (?, ?, ?, ?, ?, ?)`, in...); err != nil {
		t.Fatal(err)
	}
	out := make([]SQLValue, len(in))
	ptrs := make([]any, len(out))
	for i := range out {
		ptrs[i] = &out[i]
	}
	if err := db.QueryRow(`SELECT n, i, r, s, b, flag FROM v`).Scan(ptrs...); err != nil {
		t.Fatal(err)
	}
	for i, v := range in {
		if out[i] != v {
			t.Errorf("column %d scanned as %#v, want %#v", i, out[i], v)
		}
	}
	if typ := sqliteTypeOf(t, db, "b"); typ != "blob" {
		t.Errorf("a blob bound as %s", typ)
	}
}

func sqliteTypeOf(t *testing.T, db *sql.DB, col string) string {
	t.Helper()
	var typ string
	if err := db.QueryRow(`SELECT typeof(` + col + `) FROM v`).Scan(&typ); err != nil {
		t.Fatal(err)
	}
	return typ
}
