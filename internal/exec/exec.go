// Package exec runs materialized scripts via the appropriate interpreter
// for their language.
//
// Stage 1: dispatch by lang (defaulting to bash), inherit stdout/stderr,
// pass through env. Returns the process's exit code on failure.
//
// Stage 5 adds:
//   - V= verbosity passthrough (MYBUILD_V env var)
//   - Stderr/stdout capture for structured event emission
//   - Per-target working directory (cwd)
//   - Timeout enforcement
//
// Stage 8 adds:
//   - Capture of exit code + duration into deps.db
//   - Structured failure events with script path for the reproducer hint
//
// The Run function signature stays stable: callers always pass a script
// path, an env map, and get back an error.
package exec

import (
	"errors"
	"fmt"
	"os"
	osexec "os/exec"

	"github.com/suhrut/gmk/internal/cache"
)

// ErrEmptyPath signals a programming error: Run was called with no script.
var ErrEmptyPath = errors.New("script path is empty")

// ExitError wraps a non-zero exit. Callers can use errors.As to inspect
// the exit code. Stage 5 may add more fields (captured stderr, duration).
type ExitError struct {
	ScriptPath string
	ExitCode   int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("script %s exited with code %d", e.ScriptPath, e.ExitCode)
}

// Options controls how a script is executed.
//
// Stage 1 supports only Env. Future stages add Cwd, Verbosity, Timeout,
// Stdin, and capture controls. Defaulting unknown fields to their zero
// values keeps Run callable with Options{} for the common case.
type Options struct {
	// Env is additional environment to expose to the script, beyond the
	// current process's environment. Keys overwrite any existing var of
	// the same name in os.Environ().
	Env map[string]string

	// Lang names the script's interpreter. If empty, dispatches by file
	// extension via cache.InterpreterForLang's default ("bash").
	Lang string

	// Reserved for later stages:
	//   Cwd       string         // S2: working directory
	//   Verbosity int            // S5: MYBUILD_V level to export
	//   Timeout   time.Duration  // S5: kill timeout
	//   Stdout    io.Writer      // S8: capture target
	//   Stderr    io.Writer      // S8: capture target
}

// Run executes a script file via the interpreter for its language and
// inherits the caller's stdout/stderr.
//
// Stage 1 behavior:
//   - The interpreter is chosen from opts.Lang (or "bash" if empty).
//   - opts.Env entries are layered on top of os.Environ().
//   - On non-zero exit, returns *ExitError with the exit code.
//   - On interpreter-not-found or signal, returns the wrapped underlying error.
//
// Callers typically pass the path returned by materialize.WriteScript.
func Run(scriptPath string, opts Options) error {
	if scriptPath == "" {
		return ErrEmptyPath
	}

	interp := cache.InterpreterForLang(opts.Lang)
	cmd := osexec.Command(interp, scriptPath) //nolint:gosec // script path is computed, not user-provided

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = composeEnv(opts.Env)

	if err := cmd.Run(); err != nil {
		var exitErr *osexec.ExitError
		if errors.As(err, &exitErr) {
			return &ExitError{ScriptPath: scriptPath, ExitCode: exitErr.ExitCode()}
		}
		return fmt.Errorf("exec %s: %w", scriptPath, err)
	}
	return nil
}

// composeEnv layers extra entries on top of the inherited environment.
// Returns a slice in "KEY=VALUE" form suitable for exec.Cmd.Env.
//
// If extra is nil/empty, returns os.Environ() directly to avoid an
// unnecessary allocation.
func composeEnv(extra map[string]string) []string {
	if len(extra) == 0 {
		return os.Environ()
	}

	base := os.Environ()
	out := make([]string, 0, len(base)+len(extra))

	// Track which extras we've already seen so we can append the rest.
	seen := make(map[string]bool, len(extra))

	// Walk base; for each var in extra, override its value here so the
	// final slice doesn't have duplicates.
	for _, kv := range base {
		key := envKey(kv)
		if v, ok := extra[key]; ok {
			out = append(out, key+"="+v)
			seen[key] = true
			continue
		}
		out = append(out, kv)
	}

	// Append extras that weren't already in base.
	for k, v := range extra {
		if !seen[k] {
			out = append(out, k+"="+v)
		}
	}

	return out
}

// envKey returns the key portion of a "KEY=VALUE" entry. If no "=" is
// present, returns the whole string (unusual but possible).
func envKey(kv string) string {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i]
		}
	}
	return kv
}
