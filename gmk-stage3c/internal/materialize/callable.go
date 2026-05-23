// Stage 3b callable materialization. Coexists with the Stage 1 WriteScript
// path (still used for bare targets without prelude). The new entry point
// is MaterializeCallable, which:
//
//   1. Creates the per-run scratch dir at
//      <root>/.gmk-cache/runs/<run-id>/<callable-name>/
//   2. Writes args.json, prelude.json (each as JSON file) into that dir.
//   3. Renders the body to a script file in the same dir (so users can
//      cat/exec it standalone for debugging).
//   4. Returns a CallableInvocation describing the scratch dir, the
//      env vars to pass to the child process, and the interpreter argv.
//
// The runner package consumes CallableInvocation, launches the child,
// and reads result.json back to produce the Value the dispatcher
// returns. The split keeps materialize focused on "produce files" and
// runner focused on "execute things".

package materialize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/logger"
	"github.com/suhrut/gmk/internal/store"
)

// Callable is either a Target or a Function as far as the run-time
// machinery is concerned. We unify them at this layer so the dispatcher
// and runner don't have to switch on type.
type Callable struct {
	// Name is the callable's identifier ("release", "git-version", ...).
	Name string

	// Kind is either "target" or "function" — used for log lines and
	// for the future case where the two diverge (e.g. functions have
	// no Phony field).
	Kind string

	// Run is the script body as written in YAML.
	Run string

	// Lang names the interpreter to use. "" means bash.
	Lang string

	// Prelude is the gmk-time bindings, evaluated before the body runs.
	Prelude []ir.PreludeEntry

	// Env is per-callable env overrides on top of the inherited env.
	Env map[string]string

	// Cwd is the working directory for the body. Empty means project root.
	Cwd string

	// Source is the YAML location, for diagnostics.
	Source ir.SourceLoc
}

// CallableFromTarget converts an ir.Target into a Callable.
func CallableFromTarget(t *ir.Target) *Callable {
	return &Callable{
		Name:    t.Name,
		Kind:    "target",
		Run:     t.Run,
		Lang:    t.Lang,
		Prelude: t.Prelude,
		Env:     t.Env,
		Cwd:     t.Cwd,
		Source:  t.Source,
	}
}

// CallableFromFunction converts an ir.Function into a Callable.
func CallableFromFunction(fn *ir.Function) *Callable {
	return &Callable{
		Name:    fn.Name,
		Kind:    "function",
		Run:     fn.Run,
		Lang:    fn.Lang,
		Prelude: fn.Prelude,
		Env:     fn.Env,
		Cwd:     fn.Cwd,
		Source:  fn.Source,
	}
}

// CallableInvocation describes everything the runner needs to execute a
// materialized callable: the on-disk paths it should pass via env vars,
// the interpreter command line, and the working directory.
type CallableInvocation struct {
	// Name and Kind echo the source Callable for log lines.
	Name string
	Kind string

	// Day is "YYYYMMDD" for the run's bucket. Day + Seq together form
	// the primary key in the runs table.
	Day string

	// Seq is the per-day monotonic allocation. Zero in the legacy path
	// when no Store was supplied (tests only).
	Seq int64

	// ScratchDir is the per-callable scratch directory. JSON files
	// live here plus the log.jsonl.
	ScratchDir string

	// ScriptPath is the script file the interpreter should execute.
	ScriptPath string

	// ArgsPath, PreludePath, ResultPath are the absolute paths the
	// runner will export as GMK_ARGS, GMK_PRELUDE, GMK_RESULT. ArgsPath
	// and PreludePath are pre-populated; ResultPath is just the file
	// name — the body writes to it.
	ArgsPath, PreludePath, ResultPath string

	// LogPath is the JSONL log file for this callable's slice of the run.
	LogPath string

	// Argv is the argv to launch (interpreter + flags + script).
	Argv []string

	// Env are extra env vars beyond the inherited environment. The
	// runner is responsible for merging these with os.Environ() (or
	// not, depending on policy).
	Env map[string]string

	// Cwd is the working directory the runner should use. Always
	// absolute by the time materialize returns.
	Cwd string
}

// MaterializeOpts describes how to materialize. The store handles
// per-invocation uniqueness; this struct carries the inputs we already
// have at the call site (project root, args, prelude values, language
// registry) and a Store pointer for the SQLite-backed allocation.
type MaterializeOpts struct {
	// ProjectRoot is the abs path to the project root (where .gmk-cache lives).
	ProjectRoot string

	// Store is the SQLite-backed run-id allocator. If nil, materialize
	// falls back to time-based RunIDs (legacy path, kept for tests that
	// don't want to set up a DB). New code should always pass a Store.
	Store *store.Store

	// Day is the YYYYMMDD bucket for the run. Empty => today's UTC date.
	Day string

	// Args are the caller-supplied arguments for this callable. Written
	// to args.json. Always a non-nil map; pass empty if no args.
	Args map[string]expr.Value

	// PreludeValues is the already-evaluated prelude bindings. This
	// layer does NOT evaluate the prelude — the dispatcher does, so
	// expression evaluation can recurse via ${call:...} without
	// creating a layering cycle here.
	PreludeValues map[string]expr.Value

	// Languages is the project-level user-defined language registry.
	// Built-ins are consulted first; this map overrides or adds.
	Languages map[string]*ir.Language

	// SourceFile is the YAML file the callable came from, recorded in
	// the runs table for provenance. Optional.
	SourceFile string
}

// MaterializeCallable prepares everything needed to run a callable.
// Returns a CallableInvocation that the runner can execute.
//
// Layout produced under .gmk-cache/:
//
//	bodies/<sha256>/body.<ext>          content-addressed; shared
//	lib.sh                              project-wide bash helper
//	runs/<YYYYMMDD>/<seq>-<callable>/
//	    args.json
//	    prelude.json
//	    result.json                     seeded with "null"
//	    log.jsonl
//
// Uniqueness of the runs/ subdir is guaranteed by store.AllocateRun,
// which serializes via SQLite's writer lock — kernel-grade atomicity
// without filesystem-level lock files.
func MaterializeCallable(c *Callable, opts MaterializeOpts) (*CallableInvocation, error) {
	if c == nil {
		return nil, fmt.Errorf("materialize: callable is nil")
	}
	if opts.ProjectRoot == "" {
		return nil, fmt.Errorf("materialize %s: empty ProjectRoot", c.Name)
	}

	day := opts.Day
	if day == "" {
		day = store.Today()
	}

	// Allocate the per-day monotonic seq via SQLite, or fall back to a
	// timestamp-based RunID when no store is provided (test helpers).
	var (
		seq    int64
		runDir string
	)
	if opts.Store != nil {
		s, err := opts.Store.AllocateRun(context.Background(), day)
		if err != nil {
			return nil, fmt.Errorf("materialize %s: allocate seq: %w", c.Name, err)
		}
		seq = s
		runDir = filepath.Join(opts.ProjectRoot, ".gmk-cache", "runs", day,
			fmt.Sprintf("%04d-%s", seq, c.Name))
	} else {
		// Legacy path: timestamp + nanosecond fallback. Kept so tests
		// that materialize without a store keep working. Code paths
		// that go through the CLI always pass a store.
		runDir = filepath.Join(opts.ProjectRoot, ".gmk-cache", "runs", day,
			fmt.Sprintf("%s-%s", NewRunID(), c.Name))
	}

	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, fmt.Errorf("materialize %s: mkdir %s: %w", c.Name, runDir, err)
	}

	lang := c.Lang
	if lang == "" {
		lang = "bash"
	}

	langDef, err := lookupLanguage(lang, opts.Languages)
	if err != nil {
		return nil, fmt.Errorf("materialize %s: %w", c.Name, err)
	}

	// Resolve the interpreter via $PATH if not absolute.
	interp := langDef.Interpreter
	if !filepath.IsAbs(interp) {
		resolved, lookErr := exec.LookPath(interp)
		if lookErr != nil {
			return nil, fmt.Errorf("materialize %s: cannot find interpreter %q on PATH: %w",
				c.Name, interp, lookErr)
		}
		interp = resolved
	}

	// Args / prelude / result live in the per-call run dir.
	argsPath := filepath.Join(runDir, "args.json")
	if err := writeJSON(argsPath, valueMapToJSON(opts.Args)); err != nil {
		return nil, fmt.Errorf("materialize %s: write args.json: %w", c.Name, err)
	}
	preludePath := filepath.Join(runDir, "prelude.json")
	if err := writeJSON(preludePath, valueMapToJSON(opts.PreludeValues)); err != nil {
		return nil, fmt.Errorf("materialize %s: write prelude.json: %w", c.Name, err)
	}
	resultPath := filepath.Join(runDir, "result.json")
	if err := writeJSONRaw(resultPath, []byte("null\n")); err != nil {
		return nil, fmt.Errorf("materialize %s: seed result.json: %w", c.Name, err)
	}

	// lib.sh: project-wide single copy at .gmk-cache/lib.sh. Two
	// concurrent calls writing it race benignly — bytes are identical
	// and atomicWrite uses temp+rename.
	libPath := ""
	if langDef.Name == "bash" || langDef.Name == "sh" {
		libPath = filepath.Join(opts.ProjectRoot, ".gmk-cache", "lib.sh")
		if _, err := os.Stat(libPath); os.IsNotExist(err) {
			if err := atomicWrite(libPath, []byte(libShContents), 0o644); err != nil {
				return nil, fmt.Errorf("materialize %s: write lib.sh: %w", c.Name, err)
			}
		}
	}

	// Body: content-addressed at .gmk-cache/bodies/<sha256>/body.<ext>.
	// Hash inputs: language name, file extension, libPath (so changing
	// lib.sh path invalidates), and the user's body text. Two callables
	// with byte-identical bodies under the same language share the
	// generated file.
	content := renderBody(langDef, c.Run, libPath)
	bodyHash := sha256OfStrings(langDef.Name, langDef.Ext, libPath, content)
	bodyDir := filepath.Join(opts.ProjectRoot, ".gmk-cache", "bodies", bodyHash)
	scriptPath := filepath.Join(bodyDir, "body"+langDef.Ext)
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		if err := os.MkdirAll(bodyDir, 0o755); err != nil {
			return nil, fmt.Errorf("materialize %s: mkdir bodies: %w", c.Name, err)
		}
		if err := atomicWrite(scriptPath, []byte(content), 0o755); err != nil {
			return nil, fmt.Errorf("materialize %s: write body: %w", c.Name, err)
		}
	}

	logPath := filepath.Join(runDir, "log.jsonl")

	// Resolve Cwd to abs.
	cwd := c.Cwd
	if cwd == "" {
		cwd = opts.ProjectRoot
	}
	if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(opts.ProjectRoot, cwd)
	}

	argv := append([]string{interp}, langDef.Args...)
	argv = append(argv, scriptPath)

	// GMK_RUN_ID is "<day>/<seq>" — a stable token any caller can use
	// as a SQL key into the runs table or as a path reference.
	runID := day
	if seq > 0 {
		runID = fmt.Sprintf("%s/%04d", day, seq)
	}

	env := map[string]string{
		"GMK_ARGS":    argsPath,
		"GMK_PRELUDE": preludePath,
		"GMK_RESULT":  resultPath,
		"GMK_NAME":    c.Name,
		"GMK_KIND":    c.Kind,
		"GMK_RUN_ID":  runID,
		"GMK_DAY":     day,
	}
	if seq > 0 {
		env["GMK_SEQ"] = fmt.Sprintf("%d", seq)
	}
	for k, v := range c.Env {
		env[k] = v
	}

	logger.Get("gmk.materialize").Debug("callable materialized",
		"callable", c.Name, "lang", lang,
		"day", day, "seq", seq, "scratch", runDir, "body_hash", bodyHash)

	return &CallableInvocation{
		Name:        c.Name,
		Kind:        c.Kind,
		Day:         day,
		Seq:         seq,
		ScratchDir:  runDir,
		ScriptPath:  scriptPath,
		ArgsPath:    argsPath,
		PreludePath: preludePath,
		ResultPath:  resultPath,
		LogPath:     logPath,
		Argv:        argv,
		Env:         env,
		Cwd:         cwd,
	}, nil
}

// renderBody adds a small language-specific preamble before the user's
// body. Goal: every script gets strict-mode defaults so failures are
// caught early, plus a comment header for `cat`-debug ergonomics.
//
// For bash/sh, libPath is the on-disk lib.sh that the preamble sources
// (giving the body p_get/a_get/r_set/etc.). Empty for other languages.
//
// Comment syntax differs across languages: shell/Python/Ruby/Perl all
// use `#`; Node/JS uses `//`. We pick the right marker per language so
// the preamble doesn't blow up the interpreter.
func renderBody(lang *ir.Language, userBody, libPath string) string {
	var b strings.Builder
	// Shebang reflects the interpreter; bare interpreter form so it
	// works when PATH is set sensibly (matches Stage 1).
	b.WriteString("#!/usr/bin/env ")
	b.WriteString(filepath.Base(lang.Interpreter))
	b.WriteString("\n")

	// Comment marker for the rest of the preamble.
	cmt := "#"
	if lang.Name == "node" {
		cmt = "//"
	}

	b.WriteString(cmt)
	b.WriteString(" Generated by gmk. JSON inputs at $GMK_ARGS, $GMK_PRELUDE.\n")
	b.WriteString(cmt)
	b.WriteString(" Write result to $GMK_RESULT (JSON, defaults to null).\n")
	b.WriteString("\n")
	switch lang.Name {
	case "bash":
		b.WriteString("set -eo pipefail\n")
		if libPath != "" {
			b.WriteString(". \"")
			b.WriteString(libPath)
			b.WriteString("\"\n")
		}
		b.WriteString("\n")
	case "sh":
		b.WriteString("set -e\n")
		if libPath != "" {
			b.WriteString(". \"")
			b.WriteString(libPath)
			b.WriteString("\"\n")
		}
		b.WriteString("\n")
	case "python", "python3":
		b.WriteString("import json, os, sys\n\n")
	case "ruby":
		b.WriteString("require 'json'\n\n")
	case "node":
		b.WriteString("'use strict';\n\n")
	}
	b.WriteString(userBody)
	if len(userBody) == 0 || userBody[len(userBody)-1] != '\n' {
		b.WriteString("\n")
	}
	return b.String()
}

// lookupLanguage finds the named language. User-defined entries in
// userLangs override built-ins.
func lookupLanguage(name string, userLangs map[string]*ir.Language) (*ir.Language, error) {
	if userLangs != nil {
		if l, ok := userLangs[name]; ok {
			return l, nil
		}
	}
	if l, ok := builtinLanguages[name]; ok {
		return l, nil
	}
	return nil, fmt.Errorf("unknown language %q (built-ins: bash, sh, python, ruby, node, perl; "+
		"add custom via top-level languages: block)", name)
}

// builtinLanguages is the registry of out-of-the-box interpreters. Each
// entry's Interpreter is resolved via exec.LookPath at materialize time.
// User-defined entries in a project's languages: block override these.
var builtinLanguages = map[string]*ir.Language{
	"bash":    {Name: "bash", Interpreter: "bash", Ext: ".sh"},
	"sh":      {Name: "sh", Interpreter: "sh", Ext: ".sh"},
	"python":  {Name: "python", Interpreter: "python3", Ext: ".py"},
	"python3": {Name: "python3", Interpreter: "python3", Ext: ".py"},
	"ruby":    {Name: "ruby", Interpreter: "ruby", Ext: ".rb"},
	"node":    {Name: "node", Interpreter: "node", Ext: ".js"},
	"perl":    {Name: "perl", Interpreter: "perl", Ext: ".pl"},
}

// valueMapToJSON converts a Value map into a JSON-marshallable map.
// Treats a nil input as an empty map (so downstream JSON files always
// look like {}, never null).
func valueMapToJSON(m map[string]expr.Value) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v.ToJSON()
	}
	return out
}

// writeJSON marshals v and atomically writes it to path.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeJSONRaw(path, data)
}

// writeJSONRaw atomically writes raw bytes to path.
func writeJSONRaw(path string, data []byte) error {
	return atomicWrite(path, data, 0o644)
}

// sha256OfStrings produces a hex sha256 of the concatenation of inputs,
// each separated by a NUL byte so e.g. ("a","bc") and ("ab","c") hash
// differently. Used to content-address generated body files.
func sha256OfStrings(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// NewRunID is the legacy time-based identifier used as a fallback when
// MaterializeCallable is called without a Store. Stage 3a/3b code went
// through this path; new code goes through store.AllocateRun and ignores
// this function. Kept exported so existing tests that pre-date the
// store integration still compile.
func NewRunID() string {
	now := time.Now().UTC()
	return fmt.Sprintf("%04d%02d%02d-%02d%02d%02d-%06x",
		now.Year(), now.Month(), now.Day(),
		now.Hour(), now.Minute(), now.Second(),
		now.UnixNano()&0xffffff)
}

// ReadResultFile parses result.json and returns the resulting Value.
// Called by the runner after the body completes to extract the
// function's return value.
//
// Empty/missing files are treated as null (NoneKind), not as errors.
func ReadResultFile(path string) (expr.Value, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return expr.NewNone(), nil
		}
		return expr.NewNone(), fmt.Errorf("read result: %w", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return expr.NewNone(), nil
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return expr.NewNone(), fmt.Errorf("parse result: %w", err)
	}
	return expr.FromJSON(raw)
}

// Today returns today's date in "YYYYMMDD" UTC format. Convenience shim
// around store.Today() so callers don't need to import the store
// package just to construct a MaterializeOpts.Day.
func Today() string { return store.Today() }
