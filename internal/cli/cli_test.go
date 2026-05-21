package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Stage 1 / version (regression) ---

func TestVersion(t *testing.T) {
	r := Root(BuildInfo{Version: "v0.0.1-test", Commit: "abcdef"})
	r.SetArgs([]string{"version"})

	var out bytes.Buffer
	r.SetOut(&out)
	r.SetErr(&out)

	if err := r.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "v0.0.1-test") || !strings.Contains(got, "abcdef") {
		t.Errorf("output should contain version and commit, got %q", got)
	}
}

// --- Run: simple end-to-end ---

func TestRun_Simple(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build.yml")
	content := `
vars:
  greeting: "hello"
targets:
  hello:
    run: |
      echo "${greeting} stage 2"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "hello", "--file", path})
	if err := r.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	scriptPath := filepath.Join(dir, ".gmk-cache", "code", "local", "build.yml", "hello.sh")
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("script not materialized: %v", err)
	}
	if !strings.Contains(string(data), `echo "hello stage 2"`) {
		t.Errorf("script body unexpected:\n%s", data)
	}
}

// --- Run: multi-target deps execute in correct order ---

func TestRun_DepsExecuteInOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build.yml")
	out := filepath.Join(dir, "trace")
	content := `
targets:
  prep:
    run: |
      echo prep >> ` + out + `
  build:
    deps: [prep]
    run: |
      echo build >> ` + out + `
  ship:
    deps: [build]
    run: |
      echo ship >> ` + out + `
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "ship", "--file", path})
	if err := r.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := "prep\nbuild\nship\n"
	if string(data) != want {
		t.Errorf("trace = %q, want %q", string(data), want)
	}
}

// --- Run: dep cycle is caught at planning time, no exec attempted ---

func TestRun_CycleErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build.yml")
	content := `
targets:
  a:
    deps: [b]
    run: "echo a"
  b:
    deps: [a]
    run: "echo b"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "a", "--file", path})
	err := r.Execute()
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("err %q should mention cycle", err)
	}
}

// --- Run: missing dep is caught at planning time ---

func TestRun_MissingDepErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build.yml")
	content := `
targets:
  a:
    deps: [ghost]
    run: "echo a"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "a", "--file", path})
	err := r.Execute()
	if err == nil {
		t.Fatal("expected missing-dep error")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("err should mention missing dep, got %v", err)
	}
}

// --- Run: target's env is resolved against project scope ---

func TestRun_EnvResolved(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "envout")
	path := filepath.Join(dir, "build.yml")
	content := `
vars:
  who: "world"
targets:
  greet:
    env:
      GREETING: "hello ${who}"
    run: |
      echo "$GREETING" > ` + out + `
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "greet", "--file", path})
	if err := r.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "hello world" {
		t.Errorf("env not resolved: got %q", strings.TrimSpace(string(data)))
	}
}

// --- DryRun subcommand prints plan, executes nothing ---

func TestDryRun_PrintsPlanNoExecution(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "sentinel")
	path := filepath.Join(dir, "build.yml")
	content := `
targets:
  prep:
    run: |
      echo executed > ` + out + `
  build:
    deps: [prep]
    run: |
      echo also > ` + out + `
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"dryrun", "build", "--file", path})

	var stdout bytes.Buffer
	r.SetOut(&stdout)

	if err := r.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Sentinel file should NOT exist — dryrun must not execute scripts.
	if _, err := os.Stat(out); err == nil {
		t.Error("dryrun executed a script; sentinel file exists")
	}

	got := stdout.String()
	if !strings.Contains(got, "resolved dep order") {
		t.Errorf("output should mention dep order, got:\n%s", got)
	}
	if !strings.Contains(got, "prep") || !strings.Contains(got, "build") {
		t.Errorf("output should list both targets, got:\n%s", got)
	}
	if !strings.Contains(got, "would run:") {
		t.Errorf("output should show command preview, got:\n%s", got)
	}
	if !strings.Contains(got, "nothing executed") {
		t.Errorf("output should confirm no execution, got:\n%s", got)
	}
}

// --- --dry-run flag on `run` equivalent to `dryrun` subcommand ---

func TestRun_DryRunFlag(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "sentinel")
	path := filepath.Join(dir, "build.yml")
	content := `
targets:
  t:
    run: |
      echo executed > ` + out + `
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "t", "--file", path, "--dry-run"})

	var stdout bytes.Buffer
	r.SetOut(&stdout)

	if err := r.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if _, err := os.Stat(out); err == nil {
		t.Error("--dry-run executed a script")
	}
	if !strings.Contains(stdout.String(), "would run:") {
		t.Errorf("--dry-run should print plan, got:\n%s", stdout.String())
	}
}

// --- Includes flow end-to-end ---

func TestRun_WithLocalInclude(t *testing.T) {
	dir := t.TempDir()
	// Common include
	incDir := filepath.Join(dir, "vars")
	if err := os.MkdirAll(incDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incDir, "common.yml"),
		[]byte("vars:\n  shared: \"sharedval\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out")
	path := filepath.Join(dir, "build.yml")
	content := `
includes:
  - "./vars/common.yml"
targets:
  t:
    run: |
      echo "${shared}" > ` + out + `
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "t", "--file", path})
	if err := r.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	data, _ := os.ReadFile(out)
	if strings.TrimSpace(string(data)) != "sharedval" {
		t.Errorf("included var not resolved: got %q", strings.TrimSpace(string(data)))
	}
}

// --- Unknown target ---

func TestRun_UnknownTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build.yml")
	if err := os.WriteFile(path, []byte("targets:\n  exists: {run: \"true\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "missing", "--file", path})
	err := r.Execute()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("err should mention target name, got %v", err)
	}
}
