package runner_test

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/materialize"
	"github.com/suhrut/gmk/internal/runner"
)

// hasBash reports whether bash is in PATH; tests skip if not.
func hasBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not in PATH")
	}
}

// hasJq reports whether jq is in PATH. Most JSON-aware tests prefer jq,
// but we fall back to bash-only when jq isn't available.
func hasJq() bool {
	_, err := exec.LookPath("jq")
	return err == nil
}

func TestRunCallable_ReadsArgsWritesResult(t *testing.T) {
	hasBash(t)

	body := `
# Body reads its name arg out of GMK_ARGS and writes a greeting to GMK_RESULT.
if command -v jq >/dev/null 2>&1; then
  name=$(jq -r .name "$GMK_ARGS")
else
  # Fallback parser: trivial — find "name": "VALUE".
  name=$(grep -o '"name":[ ]*"[^"]*"' "$GMK_ARGS" | sed 's/.*"\([^"]*\)"$/\1/')
fi
printf '%s' '"hello, '"$name"'"' > "$GMK_RESULT"
`
	root := t.TempDir()
	c := &materialize.Callable{Name: "greet", Kind: "function", Run: body, Lang: "bash"}
	inv, err := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
		ProjectRoot: root,
		RunID:       "r1",
		Args:        map[string]expr.Value{"name": expr.NewString("Sam")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	res, err := runner.RunCallable(inv, runner.RunCallableOpts{
		Stdout:     &bytes.Buffer{},
		Stderr:     &stderr,
		InheritEnv: true,
	})
	if err != nil {
		t.Fatalf("err: %v (stderr: %q)", err, stderr.String())
	}
	if res.ExitCode != 0 {
		t.Errorf("exit=%d, stderr=%q", res.ExitCode, stderr.String())
	}
	if res.Value.Kind != expr.StringKind {
		t.Errorf("kind=%v", res.Value.Kind)
	}
	if res.Value.Str != "hello, Sam" {
		t.Errorf("got %q, want %q", res.Value.Str, "hello, Sam")
	}
}

func TestRunCallable_ReadsPrelude(t *testing.T) {
	hasBash(t)
	if !hasJq() {
		t.Skip("jq not in PATH")
	}
	body := `
host=$(jq -r .host "$GMK_PRELUDE")
port=$(jq -r .port "$GMK_PRELUDE")
jq -n --arg h "$host" --argjson p "$port" '{host: $h, port: $p, ready: true}' > "$GMK_RESULT"
`
	root := t.TempDir()
	c := &materialize.Callable{Name: "show-config", Kind: "function", Run: body, Lang: "bash"}
	inv, err := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
		ProjectRoot: root,
		RunID:       "r1",
		PreludeValues: map[string]expr.Value{
			"host": expr.NewString("db.local"),
			"port": expr.NewInt(5432),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := runner.RunCallable(inv, runner.RunCallableOpts{
		Stdout:     &bytes.Buffer{},
		Stderr:     &bytes.Buffer{},
		InheritEnv: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d", res.ExitCode)
	}
	if res.Value.Kind != expr.MapKind {
		t.Fatalf("kind=%v", res.Value.Kind)
	}
	if res.Value.Map["host"].Str != "db.local" {
		t.Errorf("host: %+v", res.Value.Map["host"])
	}
	if res.Value.Map["port"].Int != 5432 {
		t.Errorf("port: %+v", res.Value.Map["port"])
	}
	if !res.Value.Map["ready"].Bool {
		t.Errorf("ready: %+v", res.Value.Map["ready"])
	}
}

func TestRunCallable_NonZeroExit(t *testing.T) {
	hasBash(t)
	body := `echo "failing"; exit 7`
	root := t.TempDir()
	c := &materialize.Callable{Name: "fail", Kind: "function", Run: body, Lang: "bash"}
	inv, err := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
		ProjectRoot: root,
		RunID:       "r1",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := runner.RunCallable(inv, runner.RunCallableOpts{
		Stdout:     &bytes.Buffer{},
		Stderr:     &bytes.Buffer{},
		InheritEnv: true,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.ExitCode != 7 {
		t.Errorf("exit=%d, want 7", res.ExitCode)
	}
	// Result not written → value is None.
	if res.Value.Kind != expr.NoneKind {
		t.Errorf("kind=%v, want none", res.Value.Kind)
	}
}

func TestRunCallable_StdoutCaptured(t *testing.T) {
	hasBash(t)
	body := `echo "to stdout"; echo "to stderr" >&2`
	root := t.TempDir()
	c := &materialize.Callable{Name: "out", Kind: "function", Run: body, Lang: "bash"}
	inv, _ := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
		ProjectRoot: root, RunID: "r",
	})

	var stdout, stderr bytes.Buffer
	_, err := runner.RunCallable(inv, runner.RunCallableOpts{
		Stdout: &stdout, Stderr: &stderr, InheritEnv: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "to stdout") {
		t.Errorf("stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "to stderr") {
		t.Errorf("stderr: %q", stderr.String())
	}
}

func TestRunCallable_EnvVarsExposed(t *testing.T) {
	hasBash(t)
	body := `
echo "name=$GMK_NAME"
echo "kind=$GMK_KIND"
echo "run=$GMK_RUN_ID"
test -f "$GMK_ARGS" || { echo "no args file"; exit 1; }
test -f "$GMK_PRELUDE" || { echo "no prelude file"; exit 1; }
`
	root := t.TempDir()
	c := &materialize.Callable{Name: "envcheck", Kind: "function", Run: body, Lang: "bash"}
	inv, _ := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
		ProjectRoot: root, RunID: "RID-123",
	})

	var stdout bytes.Buffer
	res, err := runner.RunCallable(inv, runner.RunCallableOpts{
		Stdout: &stdout, Stderr: &bytes.Buffer{}, InheritEnv: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d, stdout=%q", res.ExitCode, stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "name=envcheck") {
		t.Errorf("GMK_NAME missing: %q", out)
	}
	if !strings.Contains(out, "kind=function") {
		t.Errorf("GMK_KIND missing: %q", out)
	}
	if !strings.Contains(out, "run=RID-123") {
		t.Errorf("GMK_RUN_ID missing: %q", out)
	}
}
