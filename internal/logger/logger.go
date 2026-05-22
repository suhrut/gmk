// Package logger provides hierarchical, policy-driven structured logging
// for gmk. It is a thin layer over Go's log/slog package adding:
//
//   - Hierarchical logger names (gmk.expr.parser, gmk.target.release, ...).
//   - Per-name level policies with prefix-wildcard matching.
//   - Context-bound logger names so atom-level operations (target/function/
//     plugin runs) log under their atom's name automatically.
//   - Per-run JSONL log files in the scratch directory (always trace level)
//     plus a terminal handler honouring the user's policy.
//
// Why hierarchical names: the 80/20 observation says most code is stable
// and only ~20% needs verbose investigation at any given time. Tools that
// flip a global -v drown the user. A name-tree with per-prefix policy
// lets the user say "give me trace for gmk.target.flaky-deploy and
// gmk.plugin.vault, warn for everything else" — and the design works
// because every log site has already declared which atom it belongs to.
//
// Naming convention (closed):
//
//	gmk.expr.parser, gmk.expr.eval
//	gmk.load.yaml, gmk.load.include
//	gmk.dag
//	gmk.runner.bash, gmk.runner.python, ...
//	gmk.cache
//	gmk.socket            (parent-child IPC)
//	gmk.target.<name>     (per-target atom)
//	gmk.fn.<name>         (per-function atom)
//	gmk.plugin.<name>     (per-plugin atom)
//	gmk.tmpl.<name>       (per-template atom, S3c)
//	gmk.run.<run-id>      (per-run scope)
//
// All names begin with "gmk." so user atom names cannot shadow internal
// loggers. Names are matched against the policy tree by prefix using "."
// as the separator; pattern "gmk.target.*" matches any single trailing
// segment, and an exact name takes precedence over a wildcard.
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
)

// Level mirrors the slog levels with explicit names for the six standard
// gmk log levels. The integer values are chosen to match slog's so a
// caller can pass a Level directly to slog.Logger.Log.
type Level slog.Level

const (
	LevelTrace Level = -8
	LevelDebug Level = Level(slog.LevelDebug)
	LevelInfo  Level = Level(slog.LevelInfo)
	LevelWarn  Level = Level(slog.LevelWarn)
	LevelError Level = Level(slog.LevelError)
	LevelFatal Level = 12
)

// String returns the human-readable level name.
func (l Level) String() string {
	switch {
	case l <= LevelTrace:
		return "trace"
	case l <= LevelDebug:
		return "debug"
	case l <= LevelInfo:
		return "info"
	case l <= LevelWarn:
		return "warn"
	case l <= LevelError:
		return "error"
	default:
		return "fatal"
	}
}

// ParseLevel returns the level for the given case-insensitive name,
// or an error if the name is not one of the six standard levels.
func ParseLevel(s string) (Level, error) {
	switch lower(s) {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return LevelDebug, nil
	case "info":
		return LevelInfo, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error", "err":
		return LevelError, nil
	case "fatal":
		return LevelFatal, nil
	default:
		return 0, &unknownLevelError{Name: s}
	}
}

type unknownLevelError struct{ Name string }

func (e *unknownLevelError) Error() string { return "unknown log level: " + e.Name }

// nameAttrKey is the slog attribute key that carries the hierarchical
// logger name. Handlers inspect this attribute when deciding whether
// to emit a record.
const nameAttrKey = "name"

// ctxKey is the type used for the context value that carries a logger
// name. It's unexported so callers cannot collide with our key.
type ctxKey struct{}

// WithName returns a derived context that carries the given logger name.
// Code that calls logger.From(ctx) gets a logger with this name attached.
//
// Use this when entering an atom-scoped operation:
//
//	ctx = logger.WithName(ctx, "gmk.target."+target.Name)
//	defer ...
//	logger.From(ctx).Info("starting target", "deps", target.Deps)
func WithName(ctx context.Context, name string) context.Context {
	if name == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, name)
}

// NameFromContext returns the logger name bound to ctx, or the empty
// string if none was set. Exposed for test introspection.
func NameFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if s, ok := ctx.Value(ctxKey{}).(string); ok {
		return s
	}
	return ""
}

// Logger is a thin wrapper around *slog.Logger that knows its name.
// All log methods preserve the name in emitted records so the handler
// can apply per-name policy.
type Logger struct {
	base *slog.Logger
	name string
}

// Get returns a Logger with the given hierarchical name. The Logger
// shares the global handler stack, so a single policy update reaches
// every Logger that was already obtained.
func Get(name string) *Logger {
	return &Logger{
		base: slog.Default(),
		name: name,
	}
}

// From returns a Logger for the name carried on ctx, or the default
// (unnamed) Logger if ctx carries no name.
func From(ctx context.Context) *Logger {
	if name := NameFromContext(ctx); name != "" {
		return Get(name)
	}
	return &Logger{base: slog.Default(), name: ""}
}

// Name returns the logger's hierarchical name.
func (l *Logger) Name() string { return l.name }

// log emits a record at the given level if and only if the global
// policy permits it for this logger's name. The check happens in the
// handler chain (the policyHandler at the top of the stack), so this
// method always goes through slog and lets slog short-circuit on
// disabled levels for free.
//
// The hierarchical name is attached as an attribute on the RECORD
// (not via slog.Logger.With, which would bury it inside the handler
// chain). This way handlers can read it via record.Attrs and the
// policyHandler can decide whether to forward.
func (l *Logger) log(level Level, msg string, args ...any) {
	if l == nil || l.base == nil {
		return
	}
	if l.name != "" {
		// Prepend so the name is the first attr on the record; helps
		// human-format output align consistently.
		args = append([]any{nameAttrKey, l.name}, args...)
	}
	l.base.Log(context.Background(), slog.Level(level), msg, args...)
}

// Trace logs at trace level — most verbose, internal state.
func (l *Logger) Trace(msg string, args ...any) { l.log(LevelTrace, msg, args...) }

// Debug logs at debug level — significant decisions, dispatch points.
func (l *Logger) Debug(msg string, args ...any) { l.log(LevelDebug, msg, args...) }

// Info logs at info level — high-level progress, atom start/end.
func (l *Logger) Info(msg string, args ...any) { l.log(LevelInfo, msg, args...) }

// Warn logs at warn level — potential issues, deprecations.
func (l *Logger) Warn(msg string, args ...any) { l.log(LevelWarn, msg, args...) }

// Error logs at error level — failures recovered from or surfaced.
func (l *Logger) Error(msg string, args ...any) { l.log(LevelError, msg, args...) }

// Fatal logs at fatal level. Does NOT call os.Exit — callers are
// responsible for terminating; this is the level reserved for the
// final pre-exit log line so the message can be tagged correctly.
func (l *Logger) Fatal(msg string, args ...any) { l.log(LevelFatal, msg, args...) }

// With returns a new Logger that attaches the given key/value pairs
// to every record. Useful for run-scoped attributes (run-id, etc.)
// that should appear on every log line from a given context.
func (l *Logger) With(args ...any) *Logger {
	if l == nil || l.base == nil {
		return l
	}
	return &Logger{base: l.base.With(args...), name: l.name}
}

// Configure installs a new global handler stack from the given options.
// Subsequent calls to Get and From use the new stack. Existing Logger
// values continue to work because they hold a *slog.Logger that shares
// the stack via slog.Default(); when the default changes, they pick it
// up on next use.
//
// Calling Configure with the zero Options resets to the default (which
// is a discarding handler used in tests if Init wasn't called).
//
// The handler shape installed:
//
//	multiHandler
//	  ├─ policyHandler -> terminalHandler   (terminal honours policy)
//	  └─ fileHandler*                       (files capture everything matching)
//
// Files always capture matching records at trace level — they're the
// archive for retrospective debugging. The terminal is the live view
// the user is reading; that's where the policy applies.
func Configure(opts Options) {
	mu.Lock()
	defer mu.Unlock()

	current = opts.snapshot()

	var branches []slog.Handler

	if opts.TerminalWriter != nil {
		term := newTerminalHandler(opts.TerminalWriter, opts.TerminalFormat)
		// Policy gates only the terminal branch.
		branches = append(branches, newPolicyHandler(current.Policy, term))
	}

	for _, fr := range opts.FileRoutes {
		f, err := os.OpenFile(fr.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			os.Stderr.WriteString("logger: cannot open " + fr.Path + ": " + err.Error() + "\n")
			continue
		}
		// File branches bypass policy: always trace.
		branches = append(branches, newFileHandler(f, fr.Matcher))
	}

	if len(branches) == 0 {
		branches = append(branches, newDiscardHandler())
	}

	slog.SetDefault(slog.New(multiHandler(branches)))
}

// Options configures the global logger stack. Construct from CLI flags,
// env vars, and/or config files via the Config helpers, then call
// Configure.
type Options struct {
	// Policy is the level policy. nil policy = use only DefaultLevel.
	Policy *Policy

	// TerminalWriter is where human-readable logs go. Typically os.Stderr.
	// nil means no terminal output.
	TerminalWriter io.Writer

	// TerminalFormat selects the on-screen format. Currently:
	//   "human"  — colour-tinted single line, default
	//   "json"   — compact JSON, one record per line
	TerminalFormat string

	// FileRoutes additional file sinks. Each route writes records whose
	// name matches the route's Matcher. Always at trace level (file
	// sinks never apply the terminal policy — files are for completeness).
	FileRoutes []FileRoute
}

// snapshot copies Options to avoid the caller mutating it after install.
func (o Options) snapshot() Options {
	out := o
	if o.Policy != nil {
		out.Policy = o.Policy.Clone()
	}
	out.FileRoutes = append([]FileRoute(nil), o.FileRoutes...)
	return out
}

// FileRoute is a file sink with a name matcher.
type FileRoute struct {
	Path    string
	Matcher *NameMatcher
}

var (
	mu      sync.Mutex
	current Options
)

// Reset is for tests: discards any installed handler stack and reverts
// to the no-op default.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	current = Options{}
	slog.SetDefault(slog.New(newDiscardHandler()))
}

// lower lowercases an ASCII string without pulling in the strings package.
func lower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}
