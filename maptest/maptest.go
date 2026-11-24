// Package maptest keeps an app's Go typed: no map[string]interface{} or
// map[string]any outside the few a library's own signature makes it write.
// An app's tests call NoUntypedMaps on its source tree.
package maptest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Use is a file's untyped maps that stay, and why: a library's signature
// that takes or returns one.
type Use struct {
	Count int
	Why   string
}

// NoUntypedMaps fails t when a non-test Go file under dir holds a
// map[string]interface{} or map[string]any beyond allowed's count for it
// (keyed by slash path relative to dir), or fewer than allowed says: the
// allowance follows every map converted. Directories named testdata or
// vendor, and hidden ones, are not scanned.
func NoUntypedMaps(t testing.TB, dir string, allowed map[string]Use) {
	t.Helper()
	found, err := Count(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems(found, allowed) {
		t.Error(p)
	}
}

// problems is each way found strays from allowed, by file name.
func problems(found map[string]int, allowed map[string]Use) []string {
	files := map[string]bool{}
	for f := range found {
		files[f] = true
	}
	for f := range allowed {
		files[f] = true
	}
	var names []string
	for f := range files {
		names = append(names, f)
	}
	sort.Strings(names)
	var out []string
	for _, f := range names {
		n, use := found[f], allowed[f]
		switch {
		case use.Count > 0 && use.Why == "":
			out = append(out, fmt.Sprintf("%s: an allowed untyped map needs the reason it stays", f))
		case n > use.Count:
			out = append(out, fmt.Sprintf("%s: %d untyped maps, %d allowed: use a struct (or json.RawMessage for data whose shape is open)", f, n, use.Count))
		case n < use.Count:
			out = append(out, fmt.Sprintf("%s: %d untyped maps, %d allowed: lower its allowance to %d", f, n, use.Count, n))
		}
	}
	return out
}

// Count is each non-test Go file under dir's untyped maps, keyed by slash
// path relative to dir; a file with none is absent.
func Count(dir string) (map[string]int, error) {
	found := map[string]int{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != dir && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		if n := countUntyped(f); n > 0 {
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			found[filepath.ToSlash(rel)] = n
		}
		return nil
	})
	return found, err
}

// countUntyped is f's map types keyed by string whose value is interface{}
// or any, nested ones each counted.
func countUntyped(f *ast.File) int {
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		if m, ok := node.(*ast.MapType); ok && isString(m.Key) && isEmptyInterface(m.Value) {
			n++
		}
		return true
	})
	return n
}

func isString(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "string"
}

func isEmptyInterface(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name == "any"
	case *ast.InterfaceType:
		return v.Methods == nil || len(v.Methods.List) == 0
	}
	return false
}
