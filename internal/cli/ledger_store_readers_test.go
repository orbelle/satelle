package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// site is where a selector or call appears: the file's base name and the
// enclosing function (a func literal inside a command constructor counts as
// that constructor).
type site struct{ file, fn string }

// TestOnlyReplicationVerbsReadTheStore proves who reads [ledger] store: the
// selector Ledger.Store appears only in resolveStore and store-test's
// command; resolveStore is called only by openStore, openStore only by the
// push, pull and status commands' path (withStore); storeCommand only by
// openStore and store-test; ledgerstore.Resolve only by resolveStore. So no
// hook, gate, read or remote path reads the setting.
func TestOnlyReplicationVerbsReadTheStore(t *testing.T) {
	root := filepath.Join(pkgDir, "..", "..")
	selectors := map[string][]site{}
	calls := map[string][]site{}
	fset := token.NewFileSet()
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			for _, decl := range f.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				here := site{filepath.Base(p), fd.Name.Name}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.SelectorExpr:
						if x.Sel.Name == "Store" {
							if inner, ok := x.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "Ledger" {
								selectors["Ledger.Store"] = append(selectors["Ledger.Store"], here)
							}
						}
					case *ast.CallExpr:
						switch fn := x.Fun.(type) {
						case *ast.Ident:
							calls[fn.Name] = append(calls[fn.Name], here)
						case *ast.SelectorExpr:
							if id, ok := fn.X.(*ast.Ident); ok && id.Name == "ledgerstore" {
								calls["ledgerstore."+fn.Sel.Name] = append(calls["ledgerstore."+fn.Sel.Name], here)
							}
						}
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check := func(what string, got []site, allowed ...site) {
		t.Helper()
		if len(got) == 0 {
			t.Errorf("%s: no site found; the scan is broken", what)
		}
		for _, s := range got {
			if !slices.Contains(allowed, s) {
				t.Errorf("%s appears in %s %s", what, s.file, s.fn)
			}
		}
	}
	check("Ledger.Store", selectors["Ledger.Store"], site{"ledger_store.go", "resolveStore"}, site{"cmd_ledger.go", "ledgerStoreTestCmd"})
	check("resolveStore()", calls["resolveStore"], site{"ledger_store.go", "openStore"})
	check("openStore()", calls["openStore"], site{"ledger_store.go", "withStore"})
	check("withStore()", calls["withStore"], site{"cmd_ledger.go", "ledgerPushCmd"}, site{"cmd_ledger.go", "ledgerPullCmd"}, site{"cmd_ledger.go", "ledgerStatusCmd"})
	check("storeCommand()", calls["storeCommand"], site{"ledger_store.go", "openStore"}, site{"cmd_ledger.go", "ledgerStoreTestCmd"})
	check("ledgerstore.Resolve()", calls["ledgerstore.Resolve"], site{"ledger_store.go", "resolveStore"})
}
