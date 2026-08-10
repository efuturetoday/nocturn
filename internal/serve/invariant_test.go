package serve

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The invariant this package exists to hold: exactly ONE place turns a device class into authority.
//
// capabilitiesOf's own doc comment records why. Before it, two independent decisions each inlined
// their own "is it an app" comparison — which is how a third capability quietly becomes a third
// comparison, and how adding a class turns into a search of the tree. Adding ClassWeb was exactly
// that moment, so the rule is pinned here rather than left in a comment nobody re-reads.
//
// Half of it the compiler already holds: `capabilities` is unexported, so no other package can name
// the type at all. This test holds the other half — a class CONSTANT referenced somewhere new, which
// is what an inlined comparison looks like before it grows into a policy.
//
// The allowlist is by file, and each entry is a claim about what that file is allowed to do:
//
//   - internal/auth/auth.go declares the constants and stamps legacy records (a migration, reading
//     what the file format used to mean — not a decision about authority).
//   - internal/serve/capability.go is the one place a class becomes authority (capabilitiesOf) and
//     the one place a holder becomes a class (classFor).
//   - cmd/nocturn/enroll.go mints the CLI's own credential, so it necessarily names its own class.
//
// Deliberately NOT allowlisted: internal/auth/otp.go and internal/auth/join.go, which each held a
// class comparison until this change. Their absence is what proves the fix stuck.
func TestInvariant_ClassConstantsStayWhereTheyAreInterpreted(t *testing.T) {
	allowed := map[string]bool{
		"internal/auth/auth.go":        true,
		"internal/serve/capability.go": true,
		"cmd/nocturn/enroll.go":        true,
	}

	offenders := scanClassRefs(t, allowed)
	if len(offenders) > 0 {
		t.Errorf(`a device class is named outside the files that own class meaning:

  %s

What a class MAY DO belongs in serve.capabilitiesOf, and which class a holder GETS belongs in
serve.classFor — both in internal/serve/capability.go. Route the decision through one of them, or
through conn.can, which the daemon already computed once at accept time.

If a new file genuinely must name a class (minting a credential for itself, say), add it to the
allowlist in this test with a sentence saying why.`, strings.Join(offenders, "\n  "))
	}
}

// A guard that cannot fail is not a guard. With nothing allowlisted the scan must find the three
// files that legitimately name a class — if it finds none, the walk is skipping the tree, the
// matcher stopped matching, or the constants were renamed, and the test above would have gone on
// passing while enforcing nothing.
func TestInvariant_TheScannerActuallySeesClassReferences(t *testing.T) {
	found := scanClassRefs(t, nil)
	if len(found) == 0 {
		t.Fatal("scanning with an empty allowlist found nothing — the guard above is enforcing nothing")
	}
	for _, want := range []string{"internal/auth/auth.go", "internal/serve/capability.go", "cmd/nocturn/enroll.go"} {
		if !slices.ContainsFunc(found, func(ref string) bool { return strings.HasPrefix(ref, want+":") }) {
			t.Errorf("the scan did not reach %s, which is known to name a class:\n  %s",
				want, strings.Join(found, "\n  "))
		}
	}
}

// scanClassRefs reports every "auth.Class…" reference in the module's non-test Go sources, as
// "path:line:col auth.ClassX", skipping the files in allowed.
func scanClassRefs(t *testing.T, allowed map[string]bool) []string {
	t.Helper()
	root := repoRoot(t)
	fset := token.NewFileSet()
	var refs []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// agentkit is a separate module that must never learn nocturn's vocabulary; the rest hold
			// no Go of ours.
			switch d.Name() {
			case ".git", "node_modules", "dist", "mobile", "docs", "satellite", "sdk", "agentkit":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if allowed[rel] {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil // not this test's business to fail on a file that does not parse
		}
		// Inside package auth the constants are UNQUALIFIED, so a selector matcher would look straight
		// past the one package the rule is most about — "auth stores classes and never interprets
		// them" is exactly the claim that needs checking there. Match bare identifiers in its own
		// files and qualified ones everywhere else.
		own := file.Name.Name == "auth"
		record := func(pos token.Pos, name string) {
			p := fset.Position(pos)
			refs = append(refs, fmt.Sprintf("%s:%d:%d %s", rel, p.Line, p.Column, name))
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SelectorExpr:
				pkg, ok := node.X.(*ast.Ident)
				// Matched on the qualified name rather than by resolving types: an import could be
				// aliased, but aliasing to dodge this would be a deliberate act, and the point is to
				// catch the ordinary case loudly rather than to be undefeatable.
				if ok && pkg.Name == "auth" && isClassConstant(node.Sel.Name) {
					record(node.Pos(), "auth."+node.Sel.Name)
				}
				return false // the Sel would otherwise be revisited as a bare Ident below
			case *ast.Ident:
				if own && isClassConstant(node.Name) {
					record(node.Pos(), node.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return refs
}

// isClassConstant reports whether name is one of auth's Class constants.
func isClassConstant(name string) bool {
	switch name {
	case "ClassUnknown", "ClassApp", "ClassAppliance", "ClassTool", "ClassWeb":
		return true
	}
	return false
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}
