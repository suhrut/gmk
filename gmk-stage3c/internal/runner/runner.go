// Package runner defines the uniform map-in/map-out producer interface
// used across gmk's stages.
//
// All producers — script targets (Stage 2), lazy vars (Stage 5), probes
// (Stage 7), plugins and daemons (Stage 12) — implement Runner.Run. The
// shape stays constant; later stages add Runner implementations as
// drop-in additions.
//
// The map-in/map-out contract is the long-term composition primitive.
// Stage 12's plugin protocol is map-in/map-out over JSON-RPC; Stage 2's
// ScriptRunner does the same Go-internal call shape so plugins are a
// transport change, not an interface change.
//
// Below ScriptRunner, the typed internal/exec.Run primitive remains as
// the lower-level syscall wrapper for type-safety where it matters.
package runner

import (
	"fmt"
	"time"

	"github.com/suhrut/gmk/internal/exec"
)

// Runner is the uniform interface for anything that "produces a result"
// from a map of inputs. Implementations:
//
//	ScriptRunner  (S2) — runs a materialized script via exec.Run
//	LazyVarRunner (S5) — runs a !sh body and captures stdout as a value
//	ProbeRunner   (S7) — runs a probe and returns {found, version, ...}
//	PluginRunner  (S12) — invokes a plugin via JSON-RPC over stdio or socket
//
// Input keys are runner-specific but include common conventions:
//
//	"env":  map[string]string
//	"cwd":  string
//
// Output keys are also runner-specific but always include:
//
//	"exit_code":   int     (0 on success; non-zero on script-or-plugin failure)
//	"duration_ns": int64   (wall time of the operation)
//
// Implementations are responsible for documenting any additional keys.
// Unknown input keys are ignored; unknown output keys are passed through
// to callers (who may capture them as vars in S5+).
type Runner interface {
	Run(input map[string]any) (output map[string]any, err error)
}

// ScriptRunner runs a previously-materialized script via the appropriate
// interpreter.
//
// Stage 2 inputs:
//
//	"env":  map[string]string — extra env vars layered on top of process env
//	"cwd":  string             — working directory (defaults to caller's cwd)
//
// Stage 2 outputs (always present):
//
//	"exit_code":   int    — 0 on success
//	"duration_ns": int64  — measured wall time
//
// Stage 5 will add: "stdout", "stderr" when capture is requested via input.
// Stage 8 will add: "deps_added" with header-dep edges to register.
type ScriptRunner struct {
	// ScriptPath is the absolute path to the materialized script file.
	// Typically the result of materialize.WriteScript.
	ScriptPath string

	// Lang names the interpreter (bash, sh, python, ...). Empty defaults
	// to bash via cache.InterpreterForLang.
	Lang string
}

// Run implements Runner. Returns ErrInvalidInput if input keys have wrong
// types; underlying script failures surface via output["exit_code"] != 0
// and a non-nil error wrapping exec.ExitError.
func (s *ScriptRunner) Run(input map[string]any) (map[string]any, error) {
	if s == nil {
		return nil, fmt.Errorf("ScriptRunner.Run: nil receiver")
	}
	if s.ScriptPath == "" {
		return nil, fmt.Errorf("ScriptRunner.Run: ScriptPath is empty")
	}

	opts := exec.Options{Lang: s.Lang}

	if envAny, ok := input["env"]; ok {
		env, ok := envAny.(map[string]string)
		if !ok {
			return nil, fmt.Errorf("ScriptRunner.Run: input[\"env\"] must be map[string]string, got %T", envAny)
		}
		opts.Env = env
	}

	// "cwd" handling: the caller may pass a working directory for the
	// script. We forward it via exec.Options.Cwd, which sets the child
	// process's cmd.Dir (thread-safe; never touches the parent's pwd).
	if cwdAny, ok := input["cwd"]; ok {
		cwdStr, ok := cwdAny.(string)
		if !ok {
			return nil, fmt.Errorf("ScriptRunner.Run: input[\"cwd\"] must be string, got %T", cwdAny)
		}
		opts.Cwd = cwdStr
	}

	start := time.Now()
	execErr := exec.Run(s.ScriptPath, opts)
	duration := time.Since(start)

	out := map[string]any{
		"duration_ns": duration.Nanoseconds(),
		"exit_code":   0,
	}

	if execErr != nil {
		// Try to extract the exit code from *exec.ExitError; otherwise
		// expose -1 as a sentinel "failed before reaching the process".
		if exitErr, ok := execErr.(*exec.ExitError); ok {
			out["exit_code"] = exitErr.ExitCode
		} else {
			out["exit_code"] = -1
		}
		return out, execErr
	}

	return out, nil
}
