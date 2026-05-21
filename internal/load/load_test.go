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
	path := filepath.Join(tmp, "build.yml")
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
	path := filepath.Join(tmp, "build.yml")
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
	path := filepath.Join(tmp, "build.yml")
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

	mainPath := filepath.Join(tmp, "build.yml")
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
	mainPath := filepath.Join(tmp, "build.yml")
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

	mainPath := filepath.Join(tmp, "build.yml")
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
	path := filepath.Join(tmp, "build.yml")
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
	path := filepath.Join(tmp, "build.yml")
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
	path := filepath.Join(tmp, "build.yml")
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
	path := filepath.Join(tmp, "build.yml")
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

func TestVarsBlockSuffix(t *testing.T) {
	cases := []struct {
		key    string
		wantN  int
		wantOK bool
	}{
		{"vars", 0, true},
		{"vars_1", 1, true},
		{"vars_99", 99, true},
		{"vars_", 0, false},
		{"vars.1", 0, false},
		{"vars_foo", 0, false},
		{"vars_-1", 0, false},
		{"varsx", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			n, ok := varsBlockSuffix(tc.key)
			if n != tc.wantN || ok != tc.wantOK {
				t.Errorf("varsBlockSuffix(%q) = (%d, %v), want (%d, %v)",
					tc.key, n, ok, tc.wantN, tc.wantOK)
			}
		})
	}
}

func TestCoerceToString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"plain", "plain"},
		{int(42), "42"},
		{int64(123), "123"},
		{float64(3.14), "3.14"},
		{true, "true"},
		{false, "false"},
		{nil, ""},
	}
	for _, tc := range cases {
		got, err := coerceToString(tc.in)
		if err != nil {
			t.Errorf("coerceToString(%v): unexpected error %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("coerceToString(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
