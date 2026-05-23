// Stage 3b callable runner: executes a materialized CallableInvocation
// as a child process, streams stdout/stderr, and reads back the
// result.json file to obtain the callable's return value.
//
// The Stage 1 ScriptRunner (in runner.go) is preserved for the existing
// target-invocation path; this file adds RunCallable, the entry point
// used by `gmk call` and by the dispatcher when a function is invoked
// from within an expression.

package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/logger"
	"github.com/suhrut/gmk/internal/materialize"
)

// CallableResult captures everything the caller needs to know about a
// completed callable invocation.
type CallableResult struct {
	// Value is the parsed result.json (NoneKind if the body wrote nothing
	// or wrote "null").
	Value expr.Value

	// ExitCode is the OS exit code from the body process. 0 on success.
	ExitCode int

	// Duration is wall-clock time spent in the body.
	Duration time.Duration
}

// RunCallableOpts tunes how the callable's child process is launched.
type RunCallableOpts struct {
	// Stdout and Stderr are where the body's output is forwarded. If
	// nil, defaults to os.Stdout/os.Stderr. The runner does NOT capture
	// the body's stdout into the result — result comes from result.json
	// only, by design (mixing modes would create ambiguity).
	Stdout io.Writer
	Stderr io.Writer

	// Stdin is the standard input for the body. Most callables don't
	// need stdin; pass nil and a closed reader is supplied.
	Stdin io.Reader

	// InheritEnv controls whether the child inherits the parent's
	// environment. True by default: most callables expect $PATH and
	// friends to be present. The callable's Env map is layered on top.
	InheritEnv bool

	// Timeout is an optional wall-clock cap. Zero means no timeout.
	// Implemented in a later stage; currently advisory.
	Timeout time.Duration
}

// RunCallable executes a materialized callable invocation. It is the
// closure-level building block; the Stage 3b CLI `gmk call` and the
// dispatcher's Runner field both call this.
//
// The returned CallableResult is non-nil on every non-fatal outcome —
// even when the body exits non-zero — so callers can inspect Value
// and ExitCode together. An err is returned for genuine I/O / launch
// failures (interpreter missing, process couldn't fork, etc.).
func RunCallable(inv *materialize.CallableInvocation, opts RunCallableOpts) (*CallableResult, error) {
	if inv == nil {
		return nil, errors.New("RunCallable: invocation is nil")
	}
	log := logger.Get("gmk.runner.callable").With(
		"callable", inv.Name, "kind", inv.Kind, "lang", langFromArgv(inv.Argv))

	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	cmd := exec.Command(inv.Argv[0], inv.Argv[1:]...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = opts.Stdin
	cmd.Dir = inv.Cwd

	if opts.InheritEnv {
		cmd.Env = os.Environ()
	}
	for k, v := range inv.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	log.Info("starting", "argv", inv.Argv, "cwd", inv.Cwd)
	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
			err = nil // body exited non-zero; not a launch failure
		} else {
			log.Error("launch failed", "err", err.Error(), "elapsed", elapsed.String())
			return nil, fmt.Errorf("run %s: %w", inv.Name, err)
		}
	}

	// Even on non-zero exit we still try to read result.json — the body
	// may have written a partial/error result before failing.
	value, readErr := materialize.ReadResultFile(inv.ResultPath)
	if readErr != nil {
		log.Warn("could not parse result", "err", readErr.Error())
		// We don't fail the call on a parse error; we surface the
		// error in the CallableResult.Value as None and let callers
		// decide whether to treat exit-zero-with-bad-json as success
		// or failure. For now the ExitCode is the source of truth.
		value = expr.NewNone()
	}

	res := &CallableResult{
		Value:    value,
		ExitCode: exitCode,
		Duration: elapsed,
	}
	if exitCode == 0 {
		log.Info("completed", "elapsed", elapsed.String(),
			"result_kind", value.Kind.String())
	} else {
		log.Warn("non-zero exit", "code", exitCode, "elapsed", elapsed.String())
	}
	return res, nil
}

// langFromArgv extracts the interpreter base name for log lines.
// Treats argv[0] as the path to the interpreter binary.
func langFromArgv(argv []string) string {
	if len(argv) == 0 {
		return "?"
	}
	// Take just the basename without the path prefix.
	s := argv[0]
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' || s[i] == '\\' {
			return s[i+1:]
		}
	}
	return s
}
