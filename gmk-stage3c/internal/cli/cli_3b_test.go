package cli_test

// Stage 3b CLI integration tests: exercise gmk call / list / doc / schema
// through the cobra Root command (the way real users invoke them) and
// assert on stdout/stderr.
//
// These tests are higher-level than the per-package unit tests:
// they verify the wiring is correct end-to-end, including flag parsing,
// argument handling, and output formats. They complement the
// internal/integration tests, which run actual scripts via bash.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/cli"
)

func writeBuild(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gmk.yml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runRoot(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := cli.Root(cli.BuildInfo{Version: "test", Commit: "abc"})
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errb.String(), err
}

// ---- gmk list ----

func TestCLI_List_TextOutput(t *testing.T) {
	path := writeBuild(t, `
functions:
  greet:
    doc: "Say hello."
    params:
      - name: who
        type: string
    script: 'echo hi'
targets:
  build:
    doc: "Build it."
    run: 'echo built'
`)
	stdout, _, err := runRoot(t, "list", "-f", path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Functions:") {
		t.Errorf("missing Functions header: %q", stdout)
	}
	if !strings.Contains(stdout, "greet") {
		t.Errorf("missing greet: %q", stdout)
	}
	if !strings.Contains(stdout, "Say hello.") {
		t.Errorf("missing doc text: %q", stdout)
	}
	if !strings.Contains(stdout, "Targets:") {
		t.Errorf("missing Targets header: %q", stdout)
	}
	if !strings.Contains(stdout, "build") {
		t.Errorf("missing build: %q", stdout)
	}
}

func TestCLI_List_JSON(t *testing.T) {
	path := writeBuild(t, `
functions:
  greet:
    params: [{name: who, type: string}]
    script: 'echo'
`)
	stdout, _, err := runRoot(t, "list", "-f", path, "--all", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("list --json produced invalid JSON: %v\n%s", err, stdout)
	}
	if _, ok := doc["functions"]; !ok {
		t.Error("JSON output missing 'functions' key")
	}
	if _, ok := doc["targets"]; !ok {
		t.Error("JSON output missing 'targets' key")
	}
	if _, ok := doc["loggers"]; !ok {
		t.Error("JSON output missing 'loggers' key")
	}
}

func TestCLI_List_Verbose(t *testing.T) {
	path := writeBuild(t, `
functions:
  fn:
    params:
      - {name: x, type: int, doc: "the x"}
    prelude:
      doubled: "twice"
    script: 'true'
`)
	stdout, _, err := runRoot(t, "list", "-f", path, "-v")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "x: int") {
		t.Errorf("verbose should show param type: %q", stdout)
	}
	if !strings.Contains(stdout, "the x") {
		t.Errorf("verbose should show param doc: %q", stdout)
	}
	if !strings.Contains(stdout, "prelude") {
		t.Errorf("verbose should show prelude info: %q", stdout)
	}
}

// ---- gmk doc ----

func TestCLI_Doc_Function(t *testing.T) {
	path := writeBuild(t, `
functions:
  greet:
    doc: "Friendly hello function."
    params:
      - name: who
        type: string
        default: "world"
        doc: "Person to greet."
    result:
      type: string
      doc: "Greeting text."
    prelude:
      version: "1.0"
    script: |
      r_set "hello, $(a_get who)"
`)
	stdout, _, err := runRoot(t, "doc", "greet", "-f", path)
	if err != nil {
		t.Fatal(err)
	}
	wantContains := []string{
		"function greet",
		"Friendly hello function.",
		"Parameters:",
		"who: string (optional)",
		"Person to greet.",
		"Result:",
		"Greeting text.",
		"Prelude",
		"version",
		"Body:",
		"r_set",
	}
	for _, w := range wantContains {
		if !strings.Contains(stdout, w) {
			t.Errorf("doc output missing %q\nGot:\n%s", w, stdout)
		}
	}
}

func TestCLI_Doc_Target(t *testing.T) {
	path := writeBuild(t, `
targets:
  build:
    doc: "Build it."
    deps: ["clean"]
    phony: true
    env:
      ENV_VAR: "value"
    run: |
      echo "building"
  clean:
    run: 'rm -rf out'
`)
	stdout, _, err := runRoot(t, "doc", "build", "-f", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"target build",
		"(phony)",
		"Build it.",
		"Dependencies:",
		"clean",
		"Env:",
		"ENV_VAR",
		"Body:",
		"building",
	} {
		if !strings.Contains(stdout, w) {
			t.Errorf("missing %q in:\n%s", w, stdout)
		}
	}
}

func TestCLI_Doc_JSON(t *testing.T) {
	path := writeBuild(t, `
functions:
  f:
    doc: "A function."
    params: [{name: x, type: int}]
    result: {type: int}
    script: 'true'
`)
	stdout, _, err := runRoot(t, "doc", "f", "-f", path, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout)
	}
	if doc["kind"] != "function" {
		t.Errorf("kind=%v", doc["kind"])
	}
	if doc["name"] != "f" {
		t.Errorf("name=%v", doc["name"])
	}
}

func TestCLI_Doc_UnknownName(t *testing.T) {
	path := writeBuild(t, `
functions:
  exists:
    script: 'echo'
`)
	_, _, err := runRoot(t, "doc", "missing", "-f", path)
	if err == nil {
		t.Fatal("expected error for unknown name")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should name the missing item: %v", err)
	}
}

func TestCLI_Doc_ShellHelpers(t *testing.T) {
	stdout, _, err := runRoot(t, "doc", "--shell-helpers")
	if err != nil {
		t.Fatal(err)
	}
	// lib.sh contents include these helpers.
	for _, w := range []string{"p_get", "a_get", "r_set", "_gmk_have_jq"} {
		if !strings.Contains(stdout, w) {
			t.Errorf("shell-helpers output missing %q", w)
		}
	}
}

// ---- gmk schema ----

func TestCLI_Schema_ValidJSON(t *testing.T) {
	stdout, _, err := runRoot(t, "schema")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(stdout), &schema); err != nil {
		t.Fatalf("schema output is not valid JSON: %v\n%s", err, stdout)
	}
	if _, ok := schema["$schema"].(string); !ok {
		t.Error("schema lacks $schema")
	}
	if _, ok := schema["$id"].(string); !ok {
		t.Error("schema lacks $id")
	}
	if _, ok := schema["$defs"].(map[string]any); !ok {
		t.Error("schema lacks $defs")
	}
}

func TestCLI_Schema_TopLevelKeys(t *testing.T) {
	stdout, _, err := runRoot(t, "schema")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(stdout), &schema); err != nil {
		t.Fatal(err)
	}
	props := schema["properties"].(map[string]any)
	for _, k := range []string{"includes", "vars", "targets", "functions", "languages"} {
		if _, ok := props[k]; !ok {
			t.Errorf("schema.properties missing %q", k)
		}
	}
	patterns := schema["patternProperties"].(map[string]any)
	if _, ok := patterns["^vars_[0-9]+$"]; !ok {
		t.Error("schema should accept vars_N pattern keys")
	}
}

func TestCLI_Schema_DefsCrossRef(t *testing.T) {
	stdout, _, err := runRoot(t, "schema")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(stdout), &schema); err != nil {
		t.Fatal(err)
	}
	defs := schema["$defs"].(map[string]any)
	// Every $ref in the schema should point at an existing $def.
	walk(schema, func(v map[string]any) {
		if ref, ok := v["$ref"].(string); ok {
			if !strings.HasPrefix(ref, "#/$defs/") {
				t.Errorf("unexpected $ref form: %q", ref)
				return
			}
			name := strings.TrimPrefix(ref, "#/$defs/")
			if _, exists := defs[name]; !exists {
				t.Errorf("$ref points to missing def: %q", ref)
			}
		}
	})
}

// walk recursively visits every map node in a JSON-decoded document,
// calling fn on each. Used by TestCLI_Schema_DefsCrossRef to find $refs
// without writing a deep-walk by hand inline.
func walk(node any, fn func(map[string]any)) {
	switch n := node.(type) {
	case map[string]any:
		fn(n)
		for _, v := range n {
			walk(v, fn)
		}
	case []any:
		for _, v := range n {
			walk(v, fn)
		}
	}
}

// ---- gmk call (CLI flag handling — doesn't actually exec scripts) ----

func TestCLI_Call_ArgModesMutuallyExclusive(t *testing.T) {
	path := writeBuild(t, `
functions:
  f:
    params: [{name: x, type: string}]
    script: 'echo'
`)
	// Providing both --json and KEY=VAL pairs should error.
	_, _, err := runRoot(t, "call", "f", "x=1", "--json", `{"x":"2"}`, "-f", path)
	if err == nil {
		t.Fatal("expected error for conflicting arg modes")
	}
	if !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("error should explain mutex: %v", err)
	}
}

func TestCLI_Call_UnknownFunction(t *testing.T) {
	path := writeBuild(t, `
functions:
  exists:
    script: 'echo'
`)
	_, _, err := runRoot(t, "call", "missing", "-f", path)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no function") {
		t.Errorf("error msg: %v", err)
	}
}
