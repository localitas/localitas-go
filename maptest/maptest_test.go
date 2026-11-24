package maptest

import (
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The count takes map[string]interface{} and map[string]any wherever they
// are written, and nothing else keyed or valued.
func TestCountUntyped_TheRule(t *testing.T) {
	src := `package p
type T struct {
	A map[string]interface{}
	B map[string]any
	C []map[string]interface{}
	D map[string]map[string]any
	E map[string]string
	F map[int]interface{}
	G map[string]interface{ Close() error }
	H map[string]json.RawMessage
}
func f(v any) {
	switch v.(type) {
	case map[string]interface{}:
	}
	_ = map[string]any{"k": 1}
}`
	f, err := parser.ParseFile(token.NewFileSet(), "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := countUntyped(f); got != 6 {
		t.Fatalf("counted %d, want 6 (A, B, C, D's inner map, the case, the literal)", got)
	}
}

// Count walks the tree by relative slash path and leaves out tests,
// testdata, vendor and hidden directories.
func TestCount_WalksTheSourceTree(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, src string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const two = "package p\nvar a map[string]any\nvar b map[string]interface{}\n"
	write("main.go", two)
	write("sub/one.go", "package sub\nvar a map[string]any\n")
	write("sub/none.go", "package sub\nvar a map[string]string\n")
	write("sub/one_test.go", two)
	write("testdata/x.go", two)
	write("vendor/x/x.go", two)
	write(".claude/x.go", two)

	got, err := Count(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"main.go": 2, "sub/one.go": 1}
	if !maps.Equal(got, want) {
		t.Fatalf("Count = %v, want %v", got, want)
	}
}

// A file over its allowance, under it, or allowed without a reason is a
// problem; a file at its allowance is not.
func TestProblems(t *testing.T) {
	found := map[string]int{"at.go": 1, "over.go": 2, "under.go": 1, "new.go": 1, "noreason.go": 1}
	allowed := map[string]Use{
		"at.go":       {1, "goja's Export returns one"},
		"over.go":     {1, "a library"},
		"under.go":    {2, "a library"},
		"gone.go":     {1, "a library"},
		"noreason.go": {1, ""},
	}
	want := []string{
		"gone.go: 0 untyped maps, 1 allowed: lower its allowance to 0",
		"new.go: 1 untyped maps, 0 allowed: use a struct (or json.RawMessage for data whose shape is open)",
		"noreason.go: an allowed untyped map needs the reason it stays",
		"over.go: 2 untyped maps, 1 allowed: use a struct (or json.RawMessage for data whose shape is open)",
		"under.go: 1 untyped maps, 2 allowed: lower its allowance to 1",
	}
	if got := problems(found, allowed); !slices.Equal(got, want) {
		t.Fatalf("problems =\n%q\nwant\n%q", got, want)
	}
}
