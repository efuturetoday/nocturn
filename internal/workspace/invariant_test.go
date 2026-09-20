package workspace_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bindingCallers are the files allowed to hand the injector its owned bindings.
//
// Most of this rule is now held by the API rather than by this list: the injector has ONE mutator for
// owned bindings (SetOwned, a whole-set swap), so the failures a per-owner add/remove pair invited —
// double injection on reload, a binding outliving its extension, a window with no credential — are
// not expressible any more. What is left to pin is that the set is assembled in one place, so
// "a credential goes away when the thing that declared it does" has one implementation to read.
var bindingCallers = map[string]bool{
	"internal/workspace/extensions.go": true,
}

// bindingCalls are the injector methods that mutate what rides on a request.
var bindingCalls = map[string]bool{"SetOwned": true}

func TestOnlyOnePlaceRegistersACredentialBinding(t *testing.T) {
	if found := filesCallingBindingAPIs(t, "../.."); len(found) > 0 {
		t.Errorf("credential bindings are registered outside the one place: %v\n"+
			"if that is deliberate, say so here and in the doc comment — the two must not disagree", found)
	}
}

// TestTheScannerCanSeeAViolation is the counter-check: a green invariant is only worth something if a
// violation would turn it red.
func TestTheScannerCanSeeAViolation(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "internal", "sneaky")
	if err := os.MkdirAll(pkg, 0o700); err != nil {
		t.Fatal(err)
	}
	src := "package sneaky\n\nfunc f(in interface{ SetOwned(map[string][]int) }) { in.SetOwned(nil) }\n"
	if err := os.WriteFile(filepath.Join(pkg, "s.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if found := filesCallingBindingAPIs(t, dir); len(found) == 0 {
		t.Fatal("the scanner did not see a call it was written to find")
	}
}

// filesCallingBindingAPIs walks root for Go files (tests excluded) calling an injector binding method
// from outside the allowlist, and returns their repo-relative paths.
func filesCallingBindingAPIs(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return nil
		case d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "mobile" || d.Name() == "docs"):
			return fs.SkipDir
		case d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		// internal/secret DEFINES these methods; internal/mcp is not allowed to call them any more.
		if bindingCallers[rel] || strings.HasPrefix(rel, "internal/secret/") {
			return nil
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && bindingCalls[sel.Sel.Name] {
				found = append(found, rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}
