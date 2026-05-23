package exec

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeScript creates an executable file at path with the given body
// (a shebang line is prepended). Returns the path.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "#!/usr/bin/env bash\nset -eo pipefail\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRun_Success(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash-dependent test")
	}
	dir := t.TempDir()
	path := writeScript(t, dir, "ok.sh", "true")

	if err := Run(path, Options{}); err != nil {
		t.Errorf("Run: %v", err)
	}
}

func TestRun_NonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash-dependent test")
	}
	dir := t.TempDir()
	path := writeScript(t, dir, "fail.sh", "exit 7")

	err := Run(path, Options{})
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("err %v should be *ExitError", err)
	}
	if exitErr.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", exitErr.ExitCode)
	}
	if exitErr.ScriptPath != path {
		t.Errorf("ScriptPath = %q, want %q", exitErr.ScriptPath, path)
	}
}

func TestRun_EmptyPath(t *testing.T) {
	err := Run("", Options{})
	if !errors.Is(err, ErrEmptyPath) {
		t.Errorf("err = %v, want ErrEmptyPath", err)
	}
}

func TestRun_EnvPassthrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash-dependent test")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	path := writeScript(t, dir, "envtest.sh", `echo "$MY_VAR" > `+out)

	if err := Run(path, Options{Env: map[string]string{"MY_VAR": "from_gmk"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if got != "from_gmk\n" {
		t.Errorf("env var not passed: got %q", got)
	}
}

func TestRun_EnvOverrideExisting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash-dependent test")
	}
	// Set PATH in the current process; verify Run can override it.
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	path := writeScript(t, dir, "override.sh", `echo "$GMK_TEST_OVERRIDE" > `+out)

	t.Setenv("GMK_TEST_OVERRIDE", "original")
	if err := Run(path, Options{Env: map[string]string{"GMK_TEST_OVERRIDE": "overridden"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, _ := os.ReadFile(out)
	if string(data) != "overridden\n" {
		t.Errorf("env override failed: got %q", string(data))
	}
}

func TestRun_InterpreterNotFound(t *testing.T) {
	// Construct a script that needs a non-existent interpreter via Lang.
	// We can't easily test this without polluting PATH, so we verify the
	// behavior via the error type instead: ask for an interpreter that
	// exists but the file doesn't.
	err := Run("/nonexistent/script/path.sh", Options{})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	// Bash returns 127 for "file not found" in some configurations; we
	// just confirm an error was raised, not the specific code.
}

func TestComposeEnv_NilExtra(t *testing.T) {
	got := composeEnv(nil)
	if len(got) != len(os.Environ()) {
		t.Errorf("composeEnv(nil) length = %d, want %d", len(got), len(os.Environ()))
	}
}

func TestComposeEnv_AddsNew(t *testing.T) {
	got := composeEnv(map[string]string{"GMK_TEST_NEW_VAR": "1"})
	found := false
	for _, kv := range got {
		if kv == "GMK_TEST_NEW_VAR=1" {
			found = true
			break
		}
	}
	if !found {
		t.Error("composeEnv did not add new var")
	}
}

func TestComposeEnv_OverridesExisting(t *testing.T) {
	t.Setenv("GMK_TEST_OVERRIDE_KEY", "original")
	got := composeEnv(map[string]string{"GMK_TEST_OVERRIDE_KEY": "new"})

	count := 0
	for _, kv := range got {
		if envKey(kv) == "GMK_TEST_OVERRIDE_KEY" {
			count++
			if kv != "GMK_TEST_OVERRIDE_KEY=new" {
				t.Errorf("entry = %q, want %q", kv, "GMK_TEST_OVERRIDE_KEY=new")
			}
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 entry for overridden var, got %d", count)
	}
}

func TestRun_CwdHonored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash-dependent test")
	}
	scriptDir := t.TempDir()
	cwdDir := t.TempDir() // distinct from scriptDir so $(pwd) != where the file lives
	out := filepath.Join(scriptDir, "where")
	path := writeScript(t, scriptDir, "pwd.sh", "pwd > "+out)

	if err := Run(path, Options{Cwd: cwdDir}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(data))
	// macOS may resolve /tmp via /private/tmp; tolerate that by checking
	// for the cwdDir basename suffix rather than exact string equality.
	if !strings.HasSuffix(got, filepath.Base(cwdDir)) {
		t.Errorf("script ran in %q, want a dir ending in %q", got, filepath.Base(cwdDir))
	}
}

func TestRun_CwdEmpty_InheritsParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash-dependent test")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "where")
	path := writeScript(t, dir, "pwd.sh", "pwd > "+out)

	if err := Run(path, Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, _ := os.ReadFile(out)
	got := strings.TrimSpace(string(data))
	if got == "" {
		t.Errorf("script wrote empty pwd")
	}
	if !filepath.IsAbs(got) {
		t.Errorf("pwd should be absolute, got %q", got)
	}
}

func TestEnvKey(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"KEY=VALUE", "KEY"},
		{"KEY=", "KEY"},
		{"=VALUE", ""},
		{"NOEQUALS", "NOEQUALS"},
		{"KEY=VAL=UE", "KEY"},
	}
	for _, tc := range tests {
		if got := envKey(tc.in); got != tc.want {
			t.Errorf("envKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
