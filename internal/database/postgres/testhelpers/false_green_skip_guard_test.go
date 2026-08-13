package testhelpers

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// This guard closes the class of defect behind #1597 for tests not yet written,
// rather than only the call sites that exist today.
//
// t.Skip reports a test as neither passed nor failed, which is the right answer
// for "this environment cannot run the test at all" and the wrong answer for
// anything the test was written to detect. The DB-backed suites used it for
// both: a Postgres testcontainer that would not start AND a set of migrations
// that would not apply took the same t.Skipf branch. Break any SQL file under
// internal/database/postgres/migrations and the whole integration suite went
// green, with nothing distinguishing "no Docker on this runner" from "the schema
// is broken".
//
// The rule enforced here is not "no skips". It is that the skip predicate must
// test the specific precondition. Docker being absent is probed directly by
// RequirePostgresContainer, via testcontainers' own provider health check, and
// that is the only thing allowed to skip. Whether a container came up and
// whether migrations applied are results, not preconditions, so an error from
// either has to fail.
//
// The check is syntactic and parse-only, so it costs milliseconds and needs no
// build tags -- which matters, because every file it guards is behind
// //go:build integration and would otherwise be invisible to a default `go test`.

// guardedCalls are the calls whose error must never be answered with a skip,
// mapped to why. Both produce a result the suite exists to assert on.
var guardedCalls = map[string]string{
	"SetupPostgresContainer": "a container that will not start while Docker is healthy is a " +
		"real failure; use testhelpers.RequirePostgresContainer, which probes Docker itself " +
		"and skips only on that",
	"RunMigrations": "migrating is the thing under test; a migration that will not apply is " +
		"a broken schema and must fail",
	"MigrateToVersion": "migrating is the thing under test; a migration that will not apply " +
		"is a broken schema and must fail",
}

// skipFuncs are the *testing.T methods that end a test without a verdict.
var skipFuncs = map[string]bool{"Skip": true, "Skipf": true, "SkipNow": true}

type finding struct {
	pos       token.Position
	call      string
	skipFn    string
	rationale string
}

func (f finding) String() string {
	return fmt.Sprintf("%s: t.%s() answers an error from %s -- %s",
		f.pos, f.skipFn, f.call, f.rationale)
}

func TestNoSkipOnDatabaseSetupFailure(t *testing.T) {
	root := repoRootDir(t)

	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".terraform", "frontend":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	fset := token.NewFileSet()
	var findings []finding
	inspected := 0
	for _, path := range files {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			// A file that does not parse is the compiler's problem, not this
			// guard's: a package that does not compile has no passing tests to
			// falsely report green either.
			continue
		}
		n, fs := inspectFile(fset, f)
		inspected += n
		findings = append(findings, fs...)
	}

	// "Found nothing" and "looked at nothing" are the same colour on a terminal,
	// and telling them apart is precisely what this guard is about.
	if inspected == 0 {
		t.Fatal("guard inspected zero guarded call sites; its detection has broken and it " +
			"would report success on a repo that skips on every migration failure")
	}
	t.Logf("inspected %d guarded call site(s) across %d test file(s)", inspected, len(files))

	if len(findings) > 0 {
		msgs := make([]string, 0, len(findings))
		for _, f := range findings {
			msgs = append(msgs, f.String())
		}
		sort.Strings(msgs)
		t.Errorf("%d test setup site(s) turn a real failure into a skip:\n\n%s",
			len(findings), strings.Join(msgs, "\n\n"))
	}
}

// inspectFile reports how many guarded call sites it looked at and which of them
// are answered with a skip. It is the ONE dispatch: the repo-wide run and the
// self-test both go through it, so the self-test cannot pass against logic the
// real run does not have.
//
// A guarded call site is an if statement that mentions one of guardedCalls in
// its init or condition -- covering both `if err := f(); err != nil` and the
// `x, err := f()` / `if err != nil` pair, since for the latter the call is
// looked up through the preceding assignment.
func inspectFile(fset *token.FileSet, f *ast.File) (int, []finding) {
	inspected := 0
	var findings []finding

	// Map of variable name -> guarded call it was last assigned from, so the
	// two-statement shape resolves. Scoped per function body: reusing `err`
	// across tests in one file is the norm, and a file-wide map would attribute
	// one test's call to another's check.
	var walkBody func(body *ast.BlockStmt)
	walkBody = func(body *ast.BlockStmt) {
		assigned := map[string]string{}
		for _, stmt := range body.List {
			switch s := stmt.(type) {
			case *ast.AssignStmt:
				recordAssignment(s, assigned)
			case *ast.IfStmt:
				call := guardedCallIn(s.Init, s.Cond, assigned)
				if call != "" {
					inspected++
					if fn, pos, ok := skipCallIn(fset, s.Body); ok {
						findings = append(findings, finding{
							pos: pos, call: call, skipFn: fn, rationale: guardedCalls[call],
						})
					}
				}
			}
			// Nested bodies (subtests, closures, loops, the if bodies
			// themselves) get their own scope, walked independently.
			ast.Inspect(stmt, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.FuncLit:
					if v.Body != nil {
						walkBody(v.Body)
					}
					return false
				case *ast.BlockStmt:
					if v != body {
						walkBody(v)
						return false
					}
				}
				return true
			})
		}
	}

	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Body != nil {
			walkBody(fd.Body)
		}
	}
	return inspected, findings
}

// recordAssignment notes `x, err := SetupPostgresContainer(...)` so a following
// `if err != nil` is recognised as guarding that call.
func recordAssignment(s *ast.AssignStmt, assigned map[string]string) {
	if len(s.Rhs) != 1 {
		return
	}
	call := guardedCallName(s.Rhs[0])
	for _, lhs := range s.Lhs {
		id, ok := lhs.(*ast.Ident)
		if !ok || id.Name == "_" {
			continue
		}
		if call == "" {
			// Rebinding to something else clears the association, so a later
			// `if err != nil` is not attributed to a call it does not check.
			delete(assigned, id.Name)
			continue
		}
		assigned[id.Name] = call
	}
}

// guardedCallIn returns the guarded call an if statement checks the error of,
// either inline in its init/condition or through a preceding assignment.
func guardedCallIn(init ast.Stmt, cond ast.Expr, assigned map[string]string) string {
	var found string
	inspect := func(n ast.Node) {
		if n == nil {
			return
		}
		ast.Inspect(n, func(x ast.Node) bool {
			if name := guardedCallName(x); name != "" {
				found = name
				return false
			}
			return true
		})
	}
	inspect(init)
	if found != "" {
		return found
	}
	inspect(cond)
	if found != "" {
		return found
	}
	// `if err != nil` on its own: resolve err through the preceding assignment.
	ast.Inspect(cond, func(x ast.Node) bool {
		id, ok := x.(*ast.Ident)
		if !ok {
			return true
		}
		if call, ok := assigned[id.Name]; ok {
			found = call
			return false
		}
		return true
	})
	return found
}

// guardedCallName returns the guarded function name a node calls, if any. It
// matches on the function name alone: qualified (migrations.RunMigrations,
// testhelpers.SetupPostgresContainer) and unqualified in-package calls are the
// same defect, and these names are distinctive enough that matching the selector
// is not worth the false negatives from an unexpected package alias.
func guardedCallName(n ast.Node) string {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return ""
	}
	var name string
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		name = fun.Sel.Name
	case *ast.Ident:
		name = fun.Name
	}
	if _, guarded := guardedCalls[name]; guarded {
		return name
	}
	return ""
}

// skipCallIn reports the first t.Skip / t.Skipf / t.SkipNow inside a block,
// including nested blocks, since a skip behind another condition still ends the
// test without a verdict.
func skipCallIn(fset *token.FileSet, body *ast.BlockStmt) (string, token.Position, bool) {
	var name string
	var pos token.Position
	ast.Inspect(body, func(n ast.Node) bool {
		if name != "" {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !skipFuncs[sel.Sel.Name] {
			return true
		}
		name = sel.Sel.Name
		pos = fset.Position(call.Pos())
		return false
	})
	return name, pos, name != ""
}

func repoRootDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found above the working directory")
		}
		dir = parent
	}
}

// TestFalseGreenSkipGuardDetects exercises the detector on synthetic source
// covering each shape it must separate. Without it the guard could break into
// reporting nothing and the repo-wide run above would still look green -- the
// exact failure mode #1597 is about, one level up.
func TestFalseGreenSkipGuardDetects(t *testing.T) {
	const src = `package p

func TestInlineSkip(t *testing.T) {
	if err := migrations.RunMigrations(ctx, pool, path, "", ""); err != nil {
		t.Skipf("migrations failed: %v", err) // want: finding
	}
}

func TestTwoStatementSkip(t *testing.T) {
	container, err := testhelpers.SetupPostgresContainer(ctx, t)
	if err != nil {
		t.Skipf("no container: %v", err) // want: finding
	}
	_ = container
}

func TestNestedSkip(t *testing.T) {
	err := migrations.MigrateToVersion(ctx, pool, path, 42)
	if err != nil {
		if strings.Contains(err.Error(), "dirty") {
			t.Skip("dirty") // want: finding, a nested skip still ends without a verdict
		}
	}
}

func TestFails(t *testing.T) {
	if err := migrations.RunMigrations(ctx, pool, path, "", ""); err != nil {
		t.Fatalf("migrations failed: %v", err) // want: ok
	}
}

func TestRebindClearsAssociation(t *testing.T) {
	_, err := testhelpers.SetupPostgresContainer(ctx, t)
	require.NoError(t, err)
	err = os.Setenv("X", "1")
	if err != nil {
		t.Skip("unrelated") // want: ok, err no longer holds the guarded call
	}
}

func TestUnrelatedSkip(t *testing.T) {
	if os.Getenv("SKIP_DB_TESTS") != "" {
		t.Skip("opt-out") // want: ok, an explicit environment opt-out
	}
}

func TestSubtestSkip(t *testing.T) {
	t.Run("sub", func(t *testing.T) {
		err := migrations.RunMigrations(ctx, pool, path, "", "")
		if err != nil {
			t.Skipf("migrations failed: %v", err) // want: finding, closures are walked
		}
	})
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	inspected, findings := inspectFile(fset, f)

	// Six guarded if statements: three that skip inline or nested, the Fatalf
	// one, the subtest one, and the rebind case whose association is cleared is
	// NOT counted (its `if err != nil` no longer resolves to a guarded call).
	if inspected != 5 {
		t.Errorf("inspected = %d guarded site(s), want 5", inspected)
	}

	wantLines := []int{5, 12, 21, 51}
	if len(findings) != len(wantLines) {
		msgs := make([]string, 0, len(findings))
		for _, f := range findings {
			msgs = append(msgs, f.String())
		}
		t.Fatalf("findings = %d, want %d:\n%s", len(findings), len(wantLines), strings.Join(msgs, "\n"))
	}
	got := map[int]bool{}
	for _, f := range findings {
		got[f.pos.Line] = true
	}
	for _, line := range wantLines {
		if !got[line] {
			t.Errorf("no finding on line %d", line)
		}
	}
}
