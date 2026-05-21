// Package ir defines the intermediate representation that all gmk packages
// operate on after a YAML file is loaded.
//
// Design discipline: fields are added across stages but never renamed.
// Reserved-for-future-stage fields are documented in comments rather than
// stubbed out, so the growth path is visible to anyone reading this file.
package ir

// Project is the root of a parsed gmk file. It contains all vars and
// targets visible at the top level of the source YAML.
//
// Stage 1: only top-level vars (literal strings) and targets are populated.
// Stage 2 will add: Includes, a Scope tree for nested vars: blocks.
// Stage 6 will add: Hash (content-hash of normalized IR for cache validation).
type Project struct {
	// SourcePath is the absolute path to the YAML file this Project was
	// loaded from. Used for resolving relative paths and for diagnostics.
	SourcePath string

	// Root is the absolute directory containing SourcePath, used as the
	// project root for cache location.
	Root string

	// Vars maps a var name to its declaration. Iteration order is not
	// guaranteed; for ordered iteration use VarOrder.
	Vars map[string]*Var

	// VarOrder preserves the declaration order of vars from the YAML file.
	// Stage 3 will use this for ordered eager evaluation (later vars may
	// reference earlier ones).
	VarOrder []string

	// Targets maps a target name to its definition.
	Targets map[string]*Target

	// Reserved for later stages:
	//
	//   Includes []*Include  // S2: loaded library files, in declaration order
	//   Scope    *Scope      // S2: lexical scope tree for nested vars blocks
	//   Hash     string      // S6: sha256 of normalized IR, for cache key
}

// Var is a named value resolved within a scope.
//
// Stage 1: Value is always a literal string (post-substitution at load time
// is deferred to the resolve package).
// Stage 3 will convert Value into a parsed expression tree and add Kind/Tag
// fields. The String accessor below is the stable interface.
type Var struct {
	// Name is the var's identifier (e.g., "qgw_dir").
	Name string

	// Value is the literal string content from YAML, as-written, including
	// any unresolved ${...} substitution markers. The resolve package is
	// responsible for substitution; this field stores the raw template.
	Value string

	// Source is the file:line:column of this var's declaration in the source
	// YAML, used for diagnostic messages.
	Source SourceLoc

	// Reserved for later stages:
	//
	//   Kind  VarKind   // S3: Literal | Env | Ctx | Lazy | Computed
	//   Tag   string    // S3: which !tag produced this (empty for plain strings)
	//   Cache CacheMode // S6: parse | configure | ttl:* | per_run | per_read
	//   Class string    // S9: secret | pii | pci | phi (empty = none)
	//   Expr  *Expr     // S3: parsed expression AST when Value contains tags/refs
}

// Target is a runnable unit declared in YAML.
//
// Stage 1 supports only the bare essentials: a name, a Run body, and a Lang.
// Each later stage adds fields here without renaming existing ones.
type Target struct {
	// Name is the target's identifier (e.g., "build_qgw").
	Name string

	// Run is the script body as written in YAML, before any var substitution.
	// The materialize package handles substitution when generating the script
	// file.
	Run string

	// Lang names the interpreter to use. Defaults to "bash" if empty.
	// Stage 10 adds: sh, python, perl, ruby, node, pwsh, cmd.
	Lang string

	// Source is the file:line:column of this target's declaration.
	Source SourceLoc

	// Reserved for later stages:
	//
	//   Deps    []string         // S2: producers (targets or value-producing vars)
	//   Env     map[string]string// S2: extra env exposed to the script
	//   Cwd     string           // S2: working directory; defaults to project root
	//   Phony   bool             // S2: never skipped due to up-to-date checks
	//   When    *Expr            // S4: single condition expression
	//   All     []*Expr          // S4: list of AND-combined expressions
	//   Any     []*Expr          // S4: list of OR-combined expressions
	//   None    []*Expr          // S4: exclusion list (NOT-OR semantics)
	//   Cache   CacheMode        // S6: target-level cache mode
	//   Brief   string           // S5: short status line shown at V=0
	//   Output  []string         // S8: declared output paths for dep tracking
}

// SourceLoc identifies a position in a YAML file. Used in all diagnostic
// output ("config error at build.yml:42:5") and in the manifest for
// provenance ("var X was bound at lib/common.yml:8").
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
