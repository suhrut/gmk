package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if !strings.Contains(got, "v0.0.1-test") {
		t.Errorf("output %q should contain version", got)
	}
	if !strings.Contains(got, "abcdef") {
		t.Errorf("output %q should contain commit", got)
	}
}

func TestRun_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "build.yml")
	yamlContent := `
vars:
  greeting: "hello"

targets:
  hello:
    run: |
      echo "${greeting} from gmk"
`
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "hello", "--file", yamlPath})

	// Don't redirect stdout/stderr — the exec'd bash script writes there
	// via os.Stdout in the exec package. cobra's Execute() return value
	// tells us whether the run succeeded.
	if err := r.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Verify the materialized script exists where expected.
	scriptPath := filepath.Join(dir, ".gmk-cache", "code", "local", "build.yml", "hello.sh")
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("materialized script not found at %s: %v", scriptPath, err)
	}
	if !strings.Contains(string(data), "echo \"hello from gmk\"") {
		t.Errorf("materialized script content unexpected:\n%s", data)
	}
}

func TestRun_UnknownTarget(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "build.yml")
	yamlContent := `
targets:
  exists:
    run: "true"
`
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "missing", "--file", yamlPath})

	var out bytes.Buffer
	r.SetErr(&out)

	err := r.Execute()
	if err == nil {
		t.Fatal("expected error for unknown target")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error %q should mention missing target", err)
	}
}

func TestRun_MissingFile(t *testing.T) {
	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "anything", "--file", "/nonexistent/build.yml"})

	if err := r.Execute(); err == nil {
		t.Fatal("expected error for missing file")
	}
}
