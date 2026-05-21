// Package ir defines the intermediate representation that all gmk packages
// operate on after a YAML file is loaded.
//
// Design discipline: fields are added across stages but never renamed.
// Reserved-for-future-stage fields are documented in comments rather than
// stubbed out, so the growth path is visible to anyone reading this file.
package ir

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

	// Reserved for later stages:
	//
	//   Hash string  // S6: sha256 of normalized IR, for cache key
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

// Var is a named value resolved within a scope.
//
// Stage 1: Value is a literal string with optional ${NAME} substitutions.
// Stage 2: same; the scope into which the var is declared determines
// visibility. The Var type itself is unchanged from Stage 1.
// Stage 3 adds Kind/Tag/Expr fields when the expression grammar lands.
type Var struct {
	// Name is the var's identifier (e.g., "qgw_dir").
	Name string

	// Value is the literal string content from YAML, as-written, including
	// any unresolved ${...} substitution markers. The resolve package is
	// responsible for substitution; this field stores the raw template.
	Value string

	// Source is the file:line:column of this var's declaration in the
	// source YAML, used for diagnostic messages.
	Source SourceLoc

	// Reserved for later stages:
	//
	//   Kind  VarKind   // S3: Literal | Env | Ctx | Lazy | Computed
	//   Tag   string    // S3: which !tag produced this
	//   Cache CacheMode // S6: parse | configure | ttl:* | per_run | per_read
	//   Class string    // S9: secret | pii | pci | phi
	//   Expr  *Expr     // S3: parsed expression AST
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
