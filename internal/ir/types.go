// Package ir defines the intermediate representation that all gmk packages
// operate on after a YAML file is loaded.
//
// Design discipline: fields are added across stages but never renamed.
// Reserved-for-future-stage fields are documented in comments rather than
// stubbed out, so the growth path is visible to anyone reading this file.
//
// Dependencies: ir imports expr (Stage 3a) for the Var.Expr field. The expr
// package itself has no dependency on ir — it operates over interfaces, so
// the dependency direction stays unidirectional.
package ir

import "github.com/suhrut/gmk/internal/expr"

// Project is the root of a parsed gmk file. It contains all vars, targets,
// and includes visible at the top level of the source YAML, plus a
// RootScope that represents the project's top-level lexical region.
//
// Stage 2 additions:
//   - RootScope (the project's top-level scope)
//   - Multiple vars_N blocks merge into RootScope.Vars in declaration order
//   - Includes are loaded as sub-Projects and attached via RootScope.Includes
//
// For backwards compatibility, the top-level Vars and VarOrder fields
// alias RootScope.Vars and RootScope.VarOrder after Load completes — Stage 1
// callers that read p.Vars directly continue to work unchanged.
type Project struct {
	// SourcePath is the absolute path to the YAML file this Project was
	// loaded from.
	SourcePath string

	// Root is the absolute directory containing SourcePath, used as the
	// project root for cache location and relative-include resolution.
	Root string

	// Vars and VarOrder mirror RootScope.Vars / RootScope.VarOrder for
	// backwards compatibility with Stage 1 callers. New code (Stage 2+)
	// should use RootScope directly to participate in scope walking.
	Vars     map[string]*Var
	VarOrder []string

	// Targets maps a target name to its definition. Stage 2 keeps targets
	// flat at the project level (no nested scopes inside targets yet);
	// dep references resolve across the include closure via name lookup.
	Targets map[string]*Target

	// RootScope is the project's top-level lexical scope. All vars
	// declared at the file's top level live here, along with the project's
	// Includes. Target-level scopes (S4) will be children of this.
	RootScope *Scope

	// Functions maps a function name to its definition. Stage 3b: functions
	// are first-class callables alongside targets — they share the
	// prelude/body shape but are invoked via ${call:name(...)} or
	// `gmk call name`. Targets in this stage compile down to functions
	// with a default invocation path; functions are a strict superset.
	//
	// The same name space rules apply: function names must be unique across
	// the project (no shadowing across include files in this stage).
	Functions map[string]*Function

	// FunctionOrder preserves declaration order from YAML, for deterministic
	// listing in `gmk list` and `gmk doc` output.
	FunctionOrder []string

	// Languages maps a user-defined language name to its interpreter
	// configuration. The six built-in languages (bash, sh, python, ruby,
	// node, perl) are registered at runtime in materialize; the YAML
	// `languages:` block lets a project add custom entries (e.g. zsh, lua,
	// or a wrapper script).
	//
	// Stage 3b: each language is just {interpreter: string, args: []string}.
	// Stage 3c may extend with per-language strict-mode preludes and other
	// options.
	Languages map[string]*Language

	// Templates maps a template name to its definition. Stage 3c: templates
	// are reusable text-rendering patterns invoked from expressions via
	// ${render:name(args)}. They sit alongside functions and targets but
	// carry no executable code of their own — the underlying engine
	// (jinja by default, go for stdlib text/template) handles rendering.
	// Template lookup is project-wide and follows the same uniqueness
	// rules as functions/targets.
	Templates map[string]*Template

	// TemplateOrder preserves declaration order from YAML, for deterministic
	// listing in `gmk list --templates` and `gmk doc` output.
	TemplateOrder []string

	// Reserved for later stages:
	//
	//   Hash string  // S6: sha256 of normalized IR, for cache key
}

// Function is a callable defined in YAML. Functions share the prelude/body
// shape with Targets and dispatch through the same runner. The distinction:
//
//   - A Target is invoked by `gmk run <name>` (or as a dep of another
//     target). It produces side effects; its result value (if any) is
//     usually informational.
//   - A Function is invoked by `gmk call <name>` (or from inside another
//     callable via ${call:name(...)}). It's expected to produce a result
//     Value that callers can consume.
//
// Internally both compile down to the same Callable representation in
// materialize; this split exists at the IR level only to preserve
// declaration intent.
//
// Stage 3b: functions support typed parameters with defaults, a prelude
// (declarative, gmk-time expressions), and a body (run-time script in
// any supported language). The result is whatever the body writes to
// $GMK_RESULT as JSON.
type Function struct {
	// Name is the function's identifier (used by gmk call and ${call:}).
	Name string

	// Source is the file:line:column of the function's declaration.
	Source SourceLoc

	// Params declares the function's parameters in order. Empty for
	// zero-arg functions.
	Params []FunctionParam

	// Result documents what the function returns. Stage 3b: type and
	// description only; no enforcement. Stage 4 may add validation.
	Result *FunctionResult

	// Prelude is an ordered map of name->expression evaluated at gmk-time
	// (before the body runs), populating the prelude.json file the body
	// reads. Names appear in declaration order; later names may reference
	// earlier ones.
	Prelude []PreludeEntry

	// Run is the body script as written in YAML. May be empty for a
	// function that returns purely from its prelude (a pure-data function).
	Run string

	// Lang names the interpreter for Run. Defaults to "bash" when Run
	// is non-empty.
	Lang string

	// Env is extra environment variables to expose to the body, in
	// addition to the standard GMK_ARGS, GMK_PRELUDE, GMK_RESULT.
	Env map[string]string

	// Cwd is the working directory for the body.
	Cwd string

	// Doc is the user-supplied doc string from the YAML, surfaced by
	// `gmk doc <name>` and `gmk list --verbose`. Empty if not provided.
	Doc string
}

// FunctionParam declares one parameter of a Function. Stage 3b supports
// six types: string, int, float, bool, list, map — mirroring the Value
// lattice. Default is the value used when the caller omits the arg;
// nil means the param is required.
type FunctionParam struct {
	Name    string
	Type    string // string|int|float|bool|list|map
	Default *expr.Node
	Doc     string
}

// FunctionResult documents what a function returns. No enforcement in
// Stage 3b; surfaced by `gmk doc`.
type FunctionResult struct {
	Type string // string|int|float|bool|list|map|none
	Doc  string
}

// PreludeEntry is one (name, expression) pair from a function's prelude.
// Order is preserved so later entries can reference earlier ones.
//
// Stage 3c.2: a prelude entry's value may be a structured Map or List
// rather than a scalar expression. When Static is non-zero (not NoneKind),
// it's used directly as the binding's value and Expr is nil. Scalar
// expressions continue to populate Expr; Static stays at NoneKind.
type PreludeEntry struct {
	Name   string
	Expr   expr.Node
	Static expr.Value
	Source SourceLoc
}

// Language describes an interpreter for the body of a callable.
// The six built-ins (bash, sh, python, ruby, node, perl) are baked into
// materialize; user-defined languages come through Project.Languages.
type Language struct {
	// Name is the language identifier used in `lang:` fields.
	Name string

	// Interpreter is the binary name or absolute path. Resolved via
	// $PATH at materialize time if not absolute.
	Interpreter string

	// Args are prepended to the interpreter's argv before the script path.
	// For example, "-eu -o pipefail" for bash strict mode.
	Args []string

	// Ext is the file extension to use when writing the script to disk.
	// Defaults to ".sh"; some interpreters care (.py, .rb, .js).
	Ext string

	// Source is the YAML location, for diagnostics.
	Source SourceLoc
}

// Scope is a lexical region in which vars are declared and resolved.
// Scopes form a tree rooted at the project; child scopes inherit from
// their parent and may override individual vars.
//
// Lookup order when resolving a var name from a scope:
//  1. This scope's own Vars (direct declaration)
//  2. This scope's Includes (recursively), in declaration order
//  3. Parent scope (recurse to step 1)
//
// The "self -> includes -> parent" order means an included file's vars are
// visible from the including scope, but the including file's own vars
// take precedence. Parent scopes (e.g. enclosing target-scopes in S4)
// are checked last.
//
// Stage 2 only creates: one root scope per loaded Project (including
// each included file's project). Nested target-level scopes land in S4
// when per-target var blocks become useful.
type Scope struct {
	// Path identifies this scope for diagnostics. Examples:
	//   "/"                                     project root
	//   "/include[./vars/common.yml]"           local include's root
	//   "/include[<probes/system.yml>]"         library include's root
	Path string

	// Parent is the enclosing scope, or nil for a project root.
	// Lookup walks Parent recursively until found or nil reached.
	Parent *Scope

	// Vars declared directly in this scope. Iteration uses VarOrder for
	// determinism; map access is fine for direct lookups.
	Vars     map[string]*Var
	VarOrder []string

	// Includes contributed by this scope. Each Include carries a loaded
	// sub-Project; scope.Lookup walks include[0].RootScope, include[1].RootScope,
	// ... after checking this scope's own Vars.
	Includes []*Include
}

// IncludeKind classifies an include directive's resolution strategy.
type IncludeKind int

const (
	// IncludeLocal is a path-based include: "./shared.yml", "/abs/path.yml",
	// or "../sibling.yml". Resolved relative to the including file's directory.
	IncludeLocal IncludeKind = iota

	// IncludeLibrary is a path-searched include: "<probes/system.yml>".
	// Resolved against the GMK_PATH search list with defaults.
	IncludeLibrary
)

// String returns the human-readable name of the IncludeKind, used in
// diagnostic output and scope paths.
func (k IncludeKind) String() string {
	switch k {
	case IncludeLocal:
		return "local"
	case IncludeLibrary:
		return "library"
	default:
		return "unknown"
	}
}

// Include records a YAML file pulled in via an includes: directive.
//
// Stage 2: every include is loaded eagerly into its own Project, with
// its own root scope. The including project's Scope.Includes list points
// at these sub-Projects in declaration order.
//
// Stage 4 may add: when: conditions on include entries (skip include if
// condition is false). The field for that lands then as Include.When.
type Include struct {
	// Spec is the raw includes: entry as written in YAML, e.g.
	// "./shared.yml" or "<probes/system.yml>". Preserved verbatim for
	// diagnostic output.
	Spec string

	// Resolved is the absolute path the Spec resolved to.
	Resolved string

	// Kind is Local or Library, mirroring the source syntax.
	Kind IncludeKind

	// Project is the loaded sub-project. Its RootScope is what scope.Lookup
	// walks into when checking includes.
	Project *Project

	// Source is the file:line of this includes: entry in the parent YAML.
	Source SourceLoc

	// Reserved:
	//   When *Expr  // S4: skip this include if condition is false
}

// VarKind classifies how a Var's value is produced. Stage 3a uses Literal
// and Expression. Stage 3b adds Tagged (for !sh, !env, !files, !join).
// Stage 5 may add Lazy (deferred evaluation marker).
type VarKind int

const (
	// VarLiteral indicates a plain string value with no substitutions or
	// expression syntax. The Value field holds the entire result; Expr is nil.
	// resolve.Resolve treats these as a zero-evaluation fast path.
	VarLiteral VarKind = iota

	// VarExpression indicates a value that contains ${...} substitutions or
	// expression operators. Expr holds the parsed AST; Value retains the raw
	// YAML text for diagnostic display.
	VarExpression

	// VarTagged indicates a value produced by a YAML tag (!sh, !env, etc.).
	// Reserved for Stage 3b; Stage 3a never produces this kind.
	VarTagged

	// VarStructured indicates a value that's a nested map or list rather
	// than a scalar. Stage 3c.2: vars and prelude entries can carry
	// Map/List/etc. values directly; the resolved value comes from
	// Var.Structured rather than from Value or Expr.
	VarStructured
)

// String returns the human-readable name of the kind, used in diagnostic
// output and `gmk explain` (Stage 8).
func (k VarKind) String() string {
	switch k {
	case VarLiteral:
		return "literal"
	case VarExpression:
		return "expression"
	case VarTagged:
		return "tagged"
	case VarStructured:
		return "structured"
	default:
		return "unknown"
	}
}

// Var is a named value resolved within a scope.
//
// Stage 1: Value is a literal string with optional ${NAME} substitutions.
// Stage 2: same; the scope into which the var is declared determines
// visibility. The Var type itself is unchanged from Stage 1.
// Stage 3a adds Kind and Expr fields:
//   - Kind classifies the value's production strategy (Literal vs Expression).
//   - Expr is the parsed AST for VarExpression vars; nil for VarLiteral.
//
// The Value field is always populated (raw YAML text retained for
// diagnostic display and Stage 6's IR cache key). For VarLiteral, Value is
// also the resolved value; for VarExpression, resolve evaluates Expr and
// the resulting string is independent of Value.
type Var struct {
	// Name is the var's identifier (e.g., "qgw_dir").
	Name string

	// Value is the literal string content from YAML, as-written, including
	// any unresolved ${...} substitution markers. The resolve package
	// produces the final value by evaluating Expr (if non-nil); for
	// VarLiteral vars, Value is used directly.
	Value string

	// Source is the file:line:column of this var's declaration in the
	// source YAML, used for diagnostic messages.
	Source SourceLoc

	// Kind classifies the var's production strategy. Stage 3a sets this at
	// load time. Stage 3b extends with VarTagged.
	Kind VarKind

	// Expr is the parsed expression AST when Kind == VarExpression (or, in
	// Stage 3b, when Kind == VarTagged and the tag produces an expression).
	// Nil for VarLiteral.
	Expr expr.Node

	// Structured carries the resolved Value directly when Kind ==
	// VarStructured. Stage 3c.2: a var declared as a YAML mapping or
	// sequence is converted at load time to an expr.Value (Map or List,
	// recursively); the scope resolver returns this verbatim instead of
	// evaluating Value or Expr. Zero (NoneKind) for non-structured vars.
	Structured expr.Value

	// Reserved for later stages:
	//
	//   Tag   string    // S3b: which !tag produced this (when Kind == VarTagged)
	//   Cache CacheMode // S6: parse | configure | ttl:* | per_run | per_read
	//   Class string    // S9: secret | pii | pci | phi
}

// Target is a runnable unit declared in YAML.
//
// Stage 2 additions: Deps, Env, Cwd, Phony. These extend Stage 1's
// Name/Run/Lang/Source without renaming or restructuring anything.
type Target struct {
	// Name is the target's identifier.
	Name string

	// Run is the script body as written in YAML, before substitution.
	Run string

	// Lang names the interpreter to use. Defaults to "bash" if empty.
	Lang string

	// Source is the file:line:column of this target's declaration.
	Source SourceLoc

	// Deps lists the names of producers (other targets - in S2; targets
	// or value-producing vars - from S5) that must complete successfully
	// before this target runs. Resolved against the project's target table.
	// Cycles are parse-time errors. Order is preserved from YAML for
	// reproducibility, but actual execution order is topological.
	Deps []string

	// Env is extra environment to expose to this target's script,
	// resolved against the target's scope at run time. Layered on top
	// of the process's inherited environment.
	Env map[string]string

	// Cwd is the working directory for the script, resolved against the
	// project root. Empty means use the project root.
	Cwd string

	// Phony marks this target as having no on-disk output. When the
	// up-to-date system lands in S8, phony targets are never skipped.
	// Stage 2 always runs every requested target; Phony is recorded but
	// not yet enforced.
	Phony bool

	// Prelude is the target's gmk-time-evaluated declarative bindings,
	// added in Stage 3b. Each entry is evaluated before the body runs
	// and the merged result is written to $GMK_PRELUDE as JSON. Order
	// is preserved so later entries can reference earlier ones via
	// ${name} substitution.
	//
	// Targets that don't declare a prelude have len(Prelude) == 0; the
	// runner writes an empty JSON object to $GMK_PRELUDE in that case.
	Prelude []PreludeEntry

	// Doc is the user-supplied doc string surfaced by `gmk doc <name>`
	// and `gmk list --verbose`. Empty when not provided.
	Doc string

	// Reserved for later stages:
	//
	//   When, All, Any, None []*Expr  // S4: conditional execution
	//   Cache CacheMode               // S6: per-target cache mode
	//   Brief string                  // S5: short status line shown at V=0
	//   Output []string               // S8: declared output paths for dep tracking
}

// SourceLoc identifies a position in a YAML file. Used in all diagnostic
// output and in the manifest for provenance.
type SourceLoc struct {
	File   string
	Line   int
	Column int
}

// String returns a "file:line:col" form suitable for log lines.
func (s SourceLoc) String() string {
	if s.File == "" {
		return "<unknown>"
	}
	if s.Line == 0 {
		return s.File
	}
	if s.Column == 0 {
		return s.File + ":" + itoa(s.Line)
	}
	return s.File + ":" + itoa(s.Line) + ":" + itoa(s.Column)
}

// itoa converts a non-negative int to string without pulling in strconv,
// keeping the ir package dependency-free.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// Template is a reusable text-rendering pattern (Stage 3c). One
// template plus one or more invocations replaces the "five almost-
// identical Dockerfiles" problem with one source of truth.
//
// Exactly one of Body or File is non-empty (load enforces). Inline
// Body is convenient for short snippets (license headers, one-line
// configs); File is the right choice for real templates that benefit
// from syntax highlighting in an editor and can grow without
// stretching the YAML.
//
// Engine names the registered template engine handling render
// ("jinja" for gonja-v2-backed Jinja2, "go" for stdlib text/template,
// or any engine added via Stage 3f plugins). Empty means "use the
// registry's default" — set at startup in cmd/gmk/main.go, currently
// "jinja".
//
// Params is intentionally omitted in Stage 3c: templates take whatever
// args ${render:name(args)} passes them and the engine handles missing-
// key behavior. Adding declared params (with types and defaults, like
// functions) would buy validation but doubles the surface area and the
// engines themselves already produce clear "undefined variable" errors.
// Revisit if real-world use shows users want load-time validation.
type Template struct {
	// Name is the template's identifier (used in ${render:name(...)}).
	Name string

	// Source is the file:line:column of the declaration, for error
	// messages and `gmk doc` output.
	Source SourceLoc

	// Body is the inline template source as written in YAML. Empty
	// when File is set.
	Body string

	// File is the path to the template source file, relative to the
	// YAML file that declared this template. Empty when Body is set.
	// Resolved to an absolute path at load time and stored in
	// ResolvedFile; File preserves the original spelling for doc/error
	// output.
	File string

	// ResolvedFile is the absolute path to the template file after
	// the relative-to-declaring-YAML resolution. Empty for inline
	// templates. The engine receives the file contents, not the path.
	ResolvedFile string

	// Engine names the template engine to use. Empty means use the
	// registry's default at render time.
	Engine string

	// Doc is the user-supplied doc string, surfaced by `gmk doc`.
	Doc string
}
