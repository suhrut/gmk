package load

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Stage 1 behaviour: must continue to work ---

func TestLoad_S1_MinimalStillWorks(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gmk.yml")
	content := `
vars:
  greeting: "hello"

targets:
  hello:
    run: "echo ${greeting}"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if p.Vars["greeting"].Value != "hello" {
		t.Errorf("S1-compat: Vars[greeting].Value = %q", p.Vars["greeting"].Value)
	}
	if p.Targets["hello"].Run != "echo ${greeting}" {
		t.Errorf("S1-compat: target body wrong")
	}
	if p.RootScope == nil {
		t.Fatal("RootScope must be populated even in S1-style files")
	}
	if p.RootScope.Vars["greeting"].Value != "hello" {
		t.Errorf("RootScope.Vars not populated correctly")
	}
}

// --- Multiple vars_N blocks ---

func TestLoad_MultipleVarsBlocks_MergeInOrder(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gmk.yml")
	content := `
vars:
  a: "first"
  b: "first_b"

vars_1:
  b: "overridden_in_1"
  c: "from_1"

vars_2:
  c: "overridden_in_2"
  d: "from_2"

targets:
  t:
    run: "echo"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// a defined only in base vars block.
	if p.Vars["a"].Value != "first" {
		t.Errorf("a = %q, want first", p.Vars["a"].Value)
	}
	// b overridden by vars_1.
	if p.Vars["b"].Value != "overridden_in_1" {
		t.Errorf("b = %q, want overridden_in_1", p.Vars["b"].Value)
	}
	// c overridden by vars_2.
	if p.Vars["c"].Value != "overridden_in_2" {
		t.Errorf("c = %q, want overridden_in_2", p.Vars["c"].Value)
	}
	// d only in vars_2.
	if p.Vars["d"].Value != "from_2" {
		t.Errorf("d = %q, want from_2", p.Vars["d"].Value)
	}
}

func TestLoad_VarsBlock_DotSuffixRejected(t *testing.T) {
	// "vars.1" with a dot is reserved for future folder-namespacing.
	// It must be rejected as an unknown top-level key today.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gmk.yml")
	content := `
vars.1:
  x: "y"
targets:
  t:
    run: "echo"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for vars.1 (dot-suffix reserved)")
	}
	if !strings.Contains(err.Error(), "unknown top-level key") {
		t.Errorf("err should mention unknown key, got %v", err)
	}
}

// --- Includes: local ---

func TestLoad_LocalInclude(t *testing.T) {
	tmp := t.TempDir()
	// Create included file
	incPath := filepath.Join(tmp, "common.yml")
	if err := os.WriteFile(incPath, []byte("vars:\n  from_inc: \"shared\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mainPath := filepath.Join(tmp, "gmk.yml")
	mainContent := `
includes:
  - "./common.yml"

targets:
  t:
    run: "echo ${from_inc}"
`
	if err := os.WriteFile(mainPath, []byte(mainContent), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := Load(mainPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(p.RootScope.Includes) != 1 {
		t.Fatalf("expected 1 include, got %d", len(p.RootScope.Includes))
	}
	inc := p.RootScope.Includes[0]
	if inc.Kind != 0 { // IncludeLocal == 0
		t.Errorf("Kind = %v, want IncludeLocal", inc.Kind)
	}
	if inc.Spec != "./common.yml" {
		t.Errorf("Spec = %q", inc.Spec)
	}
	if inc.Project == nil {
		t.Fatal("Include.Project should be populated")
	}
	if inc.Project.Vars["from_inc"].Value != "shared" {
		t.Errorf("included var not loaded: %v", inc.Project.Vars)
	}
}

func TestLoad_LocalInclude_NotFound(t *testing.T) {
	tmp := t.TempDir()
	mainPath := filepath.Join(tmp, "gmk.yml")
	mainContent := `
includes: ["./missing.yml"]
targets:
  t:
    run: "echo"
`
	if err := os.WriteFile(mainPath, []byte(mainContent), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(mainPath)
	if err == nil {
		t.Fatal("expected error for missing include")
	}
}

// --- Includes: library ---

func TestLoad_LibraryInclude(t *testing.T) {
	tmp := t.TempDir()
	// Set up a library: <projRoot>/.gmk/lib/system.yml
	libDir := filepath.Join(tmp, ".gmk", "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "system.yml"),
		[]byte("vars:\n  os: \"linux\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mainPath := filepath.Join(tmp, "gmk.yml")
	mainContent := `
includes:
  - "<system.yml>"
targets:
  t:
    run: "echo ${os}"
`
	if err := os.WriteFile(mainPath, []byte(mainContent), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GMK_PATH", "")

	p, err := Load(mainPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(p.RootScope.Includes) != 1 {
		t.Fatalf("expected 1 include, got %d", len(p.RootScope.Includes))
	}
	inc := p.RootScope.Includes[0]
	if inc.Kind != 1 { // IncludeLibrary
		t.Errorf("Kind = %v, want IncludeLibrary", inc.Kind)
	}
	if !strings.HasSuffix(inc.Resolved, "/.gmk/lib/system.yml") {
		t.Errorf("Resolved = %q (expected project-local lib)", inc.Resolved)
	}
}

func TestLoad_InvalidIncludeSpec(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gmk.yml")
	content := `
includes: ["bare_name.yml"]
targets:
  t:
    run: "echo"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for bare-name include")
	}
	if !errors.Is(err, ErrInvalidIncludeSpec) {
		t.Errorf("err should wrap ErrInvalidIncludeSpec, got %v", err)
	}
}

// --- Include cycles ---

func TestLoad_IncludeCycle(t *testing.T) {
	tmp := t.TempDir()
	// a.yml includes b.yml; b.yml includes a.yml
	aPath := filepath.Join(tmp, "a.yml")
	bPath := filepath.Join(tmp, "b.yml")
	if err := os.WriteFile(aPath, []byte("includes: [\"./b.yml\"]\ntargets:\n  t: {run: \"echo\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte("includes: [\"./a.yml\"]\ntargets:\n  u: {run: \"echo\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(aPath)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !errors.Is(err, ErrIncludeCycle) {
		t.Errorf("err should wrap ErrIncludeCycle, got %v", err)
	}
}

// --- New target fields: deps, env, cwd, phony ---

func TestLoad_Target_NewFields(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gmk.yml")
	content := `
targets:
  prep:
    run: "echo prep"
  build:
    deps: [prep]
    env:
      NOTE: "from yaml"
      LEVEL: "info"
    cwd: "/tmp"
    phony: true
    run: "echo build"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	build := p.Targets["build"]
	if len(build.Deps) != 1 || build.Deps[0] != "prep" {
		t.Errorf("Deps = %v, want [prep]", build.Deps)
	}
	if build.Env["NOTE"] != "from yaml" {
		t.Errorf("Env[NOTE] = %q", build.Env["NOTE"])
	}
	if build.Env["LEVEL"] != "info" {
		t.Errorf("Env[LEVEL] = %q", build.Env["LEVEL"])
	}
	if build.Cwd != "/tmp" {
		t.Errorf("Cwd = %q", build.Cwd)
	}
	if !build.Phony {
		t.Error("Phony should be true")
	}
}

func TestLoad_Target_SelfDep_Rejected(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gmk.yml")
	content := `
targets:
  t:
    deps: [t]
    run: "echo"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected self-dep error")
	}
	if !strings.Contains(err.Error(), "itself") {
		t.Errorf("err should mention self-reference, got %v", err)
	}
}

// --- Closed schema: unknown top-level keys ---

func TestLoad_UnknownTopLevelKey_Rejected(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gmk.yml")
	content := `
mysteryKey: "value"
targets:
  t:
    run: "echo"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unknown top-level key")
	}
	if !strings.Contains(err.Error(), "unknown top-level key") {
		t.Errorf("err should mention unknown key, got %v", err)
	}
}

// --- Helpers ---

// Stage 3a renamed `varsBlockSuffix` (returning suffix int) to
// `isVarsBlockKey` (returning bool) because declaration order is now read
// from the YAML AST — there's no longer a numeric suffix to sort by.
// Block order in the file == iteration order.
func TestIsVarsBlockKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"vars_1", true},
		{"vars_99", true},
		{"vars", false},   // handled separately as the bare "vars" key
		{"vars_", false},  // empty suffix
		{"vars.1", false}, // wrong separator
		{"vars_foo", false},
		{"vars_-1", false},
		{"varsx", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := isVarsBlockKey(tc.key); got != tc.want {
				t.Errorf("isVarsBlockKey(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

// ============================================================================
// Stage 3a additions: declaration order, VarKind classification, tag rejection
// ============================================================================

func TestLoad_S3a_VarsDeclarationOrderPreserved(t *testing.T) {
	// In Stage 2, vars order was alphabetical. In Stage 3a it must reflect
	// the YAML source order. We use names that don't sort alphabetically
	// in the order we declare them, to make the difference observable.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gmk.yml")
	content := `
vars:
  zeta: "1"
  alpha: "2"
  middle: "3"
  beta: "4"

targets:
  noop:
    run: "true"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"zeta", "alpha", "middle", "beta"}
	if !equalStrings(p.RootScope.VarOrder, want) {
		t.Errorf("VarOrder = %v, want %v", p.RootScope.VarOrder, want)
	}
}

func TestLoad_S3a_MultipleVarsBlocksOrderByDeclaration(t *testing.T) {
	// Test that with vars + vars_1 + vars_2, names appear in the order they
	// were declared across blocks (not sorted alphabetically within a block).
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gmk.yml")
	content := `
vars:
  z: "first"
  a: "second"

vars_1:
  m: "third"

vars_2:
  b: "fourth"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"z", "a", "m", "b"}
	if !equalStrings(p.RootScope.VarOrder, want) {
		t.Errorf("VarOrder = %v, want %v", p.RootScope.VarOrder, want)
	}
}

func TestLoad_S3a_VarKindClassification(t *testing.T) {
	// Pure literal vars get VarLiteral + nil Expr (the fast path).
	// Vars with ${} get VarExpression + non-nil Expr.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gmk.yml")
	content := `
vars:
  plain: "just a string"
  with_ref: "hello ${plain}"
  with_modifier: "${env:HOME:-/tmp}"
  with_pipeline: "${plain | upper}"

targets:
  noop:
    run: "true"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	plain := p.RootScope.Vars["plain"]
	if plain == nil {
		t.Fatal("var 'plain' missing")
	}
	// We import ir to use ir.VarLiteral; resolve that via package's ir.
	if plain.Expr != nil {
		t.Errorf("'plain' should have nil Expr (literal), got %T", plain.Expr)
	}

	withRef := p.RootScope.Vars["with_ref"]
	if withRef == nil {
		t.Fatal("var 'with_ref' missing")
	}
	if withRef.Expr == nil {
		t.Errorf("'with_ref' should have non-nil Expr (expression)")
	}

	withMod := p.RootScope.Vars["with_modifier"]
	if withMod == nil || withMod.Expr == nil {
		t.Errorf("'with_modifier' should have non-nil Expr")
	}

	withPipe := p.RootScope.Vars["with_pipeline"]
	if withPipe == nil || withPipe.Expr == nil {
		t.Errorf("'with_pipeline' should have non-nil Expr")
	}
}

func TestLoad_S3a_VarSourcePositionsRecorded(t *testing.T) {
	// AST mode gives us source positions on every value. Verify Var.Source
	// has line numbers (not just file).
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gmk.yml")
	content := `vars:
  first: "v1"
  second: "v2"
targets:
  t:
    run: "true"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if first := p.RootScope.Vars["first"]; first.Source.Line == 0 {
		t.Errorf("expected non-zero source line for 'first', got %+v", first.Source)
	}
	if second := p.RootScope.Vars["second"]; second.Source.Line == 0 {
		t.Errorf("expected non-zero source line for 'second', got %+v", second.Source)
	}
	first := p.RootScope.Vars["first"]
	second := p.RootScope.Vars["second"]
	if first.Source.Line >= second.Source.Line {
		t.Errorf("expected first.Line < second.Line, got %d vs %d", first.Source.Line, second.Source.Line)
	}
}

func TestLoad_S3a_TaggedValueRejected(t *testing.T) {
	// Stage 3b will accept !sh, !env, etc. For Stage 3a we reject with
	// a clear error so users don't get confusing silent behaviour.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gmk.yml")
	content := `
vars:
  sh_var: !sh "echo hi"

targets:
  noop:
    run: "true"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for tagged value")
	}
	if !errors.Is(err, ErrTaggedValue) {
		t.Errorf("err = %v, want wrapping ErrTaggedValue", err)
	}
}

func TestLoad_S3a_BadExpressionRejectedAtLoad(t *testing.T) {
	// Syntax errors in expressions should surface at Load(), not at
	// Resolve(). This matches the Stage 1 promise: parse errors fail fast.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gmk.yml")
	content := `
vars:
  broken: "${unterminated"

targets:
  noop:
    run: "true"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected parse error for unterminated ${")
	}
}

func TestLoad_S3a_DuplicateTopLevelKeyRejected(t *testing.T) {
	// Duplicate keys at top level should be a hard error (Stage 2 silently
	// took the last). YAML libraries may or may not surface this; our
	// validate pass catches it explicitly.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "gmk.yml")
	content := `
vars:
  a: "1"
vars:
  b: "2"
targets:
  t:
    run: "true"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate top-level key")
	}
}

// equalStrings compares two string slices for equality.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
