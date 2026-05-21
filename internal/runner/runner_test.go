package runner

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	gexec "github.com/suhrut/gmk/internal/exec"
)

// writeScript creates an executable bash script at dir/name with the given
// body and returns its path.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "#!/usr/bin/env bash\nset -eo pipefail\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- Interface conformance ---

func TestScriptRunner_ImplementsRunner(t *testing.T) {
	var _ Runner = &ScriptRunner{}
}

// --- Basic success ---

func TestScriptRunner_Run_Success(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash-dependent test")
	}
	dir := t.TempDir()
	path := writeScript(t, dir, "ok.sh", "true")

	r := &ScriptRunner{ScriptPath: path, Lang: "bash"}
	out, err := r.Run(map[string]any{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if exitCode, _ := out["exit_code"].(int); exitCode != 0 {
		t.Errorf("exit_code = %v, want 0", out["exit_code"])
	}
	durNs, ok := out["duration_ns"].(int64)
	if !ok {
		t.Errorf("duration_ns missing or wrong type: %T %v", out["duration_ns"], out["duration_ns"])
	}
	if durNs < 0 {
		t.Errorf("duration_ns should be non-negative, got %d", durNs)
	}
}

// --- Non-zero exit surfaces in both error and output ---

func TestScriptRunner_Run_Failure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash-dependent test")
	}
	dir := t.TempDir()
	path := writeScript(t, dir, "fail.sh", "exit 7")

	r := &ScriptRunner{ScriptPath: path, Lang: "bash"}
	out, err := r.Run(map[string]any{})

	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	var exitErr *gexec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("err should wrap *exec.ExitError, got %T %v", err, err)
	}
	if got, _ := out["exit_code"].(int); got != 7 {
		t.Errorf("output exit_code = %v, want 7", out["exit_code"])
	}
}

// --- Env passthrough via input ---

func TestScriptRunner_Run_EnvPassthrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash-dependent test")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "result")
	path := writeScript(t, dir, "envtest.sh", `echo "$MY_RUNNER_VAR" > `+out)

	r := &ScriptRunner{ScriptPath: path, Lang: "bash"}
	_, err := r.Run(map[string]any{
		"env": map[string]string{"MY_RUNNER_VAR": "from_runner"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "from_runner" {
		t.Errorf("env not passed: got %q", got)
	}
}

// --- Input validation ---

func TestScriptRunner_Run_BadEnvType(t *testing.T) {
	r := &ScriptRunner{ScriptPath: "/dev/null", Lang: "bash"}
	_, err := r.Run(map[string]any{"env": "not_a_map"})
	if err == nil {
		t.Fatal("expected error for wrong-typed env input")
	}
	if !strings.Contains(err.Error(), "map[string]string") {
		t.Errorf("err should mention expected type, got %v", err)
	}
}

func TestScriptRunner_Run_BadCwdType(t *testing.T) {
	r := &ScriptRunner{ScriptPath: "/dev/null", Lang: "bash"}
	_, err := r.Run(map[string]any{"cwd": 123})
	if err == nil {
		t.Fatal("expected error for wrong-typed cwd input")
	}
}

// --- cwd input is honored ---

func TestScriptRunner_Run_CwdHonored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash-dependent test")
	}
	scriptDir := t.TempDir()
	cwdDir := t.TempDir()
	out := filepath.Join(scriptDir, "where")
	path := writeScript(t, scriptDir, "pwd.sh", "pwd > "+out)

	r := &ScriptRunner{ScriptPath: path, Lang: "bash"}
	_, err := r.Run(map[string]any{"cwd": cwdDir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(data))
	if !strings.HasSuffix(got, filepath.Base(cwdDir)) {
		t.Errorf("ran in %q, want dir ending in %q", got, filepath.Base(cwdDir))
	}
}

// --- Receiver guards ---

func TestScriptRunner_Run_NilReceiver(t *testing.T) {
	var r *ScriptRunner
	_, err := r.Run(map[string]any{})
	if err == nil {
		t.Fatal("expected error for nil receiver")
	}
}

func TestScriptRunner_Run_EmptyScriptPath(t *testing.T) {
	r := &ScriptRunner{}
	_, err := r.Run(map[string]any{})
	if err == nil {
		t.Fatal("expected error for empty ScriptPath")
	}
	if !strings.Contains(err.Error(), "ScriptPath") {
		t.Errorf("err should mention ScriptPath, got %v", err)
	}
}

// --- Output contract: required keys always present ---

func TestScriptRunner_Run_OutputContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash-dependent test")
	}
	dir := t.TempDir()
	path := writeScript(t, dir, "ok.sh", "true")

	r := &ScriptRunner{ScriptPath: path, Lang: "bash"}
	out, err := r.Run(nil) // nil input is also acceptable
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["exit_code"]; !ok {
		t.Error("output missing exit_code")
	}
	if _, ok := out["duration_ns"]; !ok {
		t.Error("output missing duration_ns")
	}
}
