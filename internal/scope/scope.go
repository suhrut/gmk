// Package scope implements lexical variable lookup over the ir.Scope tree.
//
// A scope is a region in which vars are declared and inherited. Lookup
// walks: this scope's own vars, then includes (recursively), then parent.
// The walk produces both the resolved Var and the scope where it was found,
// which gmk's diagnostic surfaces (and S8's `gmk explain`) use to show
// users *why* a name resolved to a particular value.
//
// Stage 2: scopes form a flat tree — one root per loaded YAML file, with
// includes as siblings under the including file's root. Stage 4 will add
// nested per-target scopes; the Lookup algorithm extends naturally because
// it already walks Parent.
//
// The package has no external dependencies beyond the ir types, keeping
// the resolution semantics testable without YAML parsing or process exec.
package scope

import (
	"strings"

	"github.com/suhrut/gmk/internal/ir"
)

// Lookup finds a var by name reachable from the given scope.
//
// Walk order:
//  1. The scope's own Vars (direct declaration in this YAML region)
//  2. The scope's Includes (recursively, in declaration order)
//  3. The Parent scope (recurse to step 1)
//
// Returns the Var and the scope it was found in. Returns nil, nil if not
// found anywhere in the chain. The returned scope is useful for diagnostic
// output ("var X was found in scope /include[lib.yml]").
//
// Stage 2 detects no cycles in includes because load.Load rejects them
// at parse time. If a malformed scope tree is constructed manually (test
// scenarios) with a cyclic Parent or Includes link, Lookup is bounded by
// a depth limit to avoid infinite recursion.
func Lookup(s *ir.Scope, name string) (*ir.Var, *ir.Scope) {
	if s == nil || name == "" {
		return nil, nil
	}
	return lookupBounded(s, name, maxScopeDepth, make(map[*ir.Scope]bool))
}

// maxScopeDepth limits scope-walk recursion as a defensive bound. Real
// projects rarely exceed depth 10 (root + 1-2 levels of includes + maybe
// target scopes in S4). A bound of 256 is far above any legitimate case
// and catches programmer error in test-constructed cyclic scopes.
const maxScopeDepth = 256

func lookupBounded(s *ir.Scope, name string, depth int, visited map[*ir.Scope]bool) (*ir.Var, *ir.Scope) {
	if s == nil || depth <= 0 || visited[s] {
		return nil, nil
	}
	visited[s] = true

	// 1. Own vars first — local declarations always shadow inherited ones.
	if v, ok := s.Vars[name]; ok {
		return v, s
	}

	// 2. Includes (recursive). Walking the include's root scope lets us see
	//    transitively-included vars too, since each included project is
	//    itself a Project with its own RootScope.
	for _, inc := range s.Includes {
		if inc == nil || inc.Project == nil || inc.Project.RootScope == nil {
			continue
		}
		if v, foundIn := lookupBounded(inc.Project.RootScope, name, depth-1, visited); v != nil {
			return v, foundIn
		}
	}

	// 3. Parent (recurse). Resets the visited set's contribution from this
	//    scope so siblings under the same parent can each contribute.
	if s.Parent != nil {
		return lookupBounded(s.Parent, name, depth-1, visited)
	}

	return nil, nil
}

// LookupSource returns a short human-readable description of where a var
// was found, suitable for diagnostic output. Empty string if not found.
//
// Examples:
//
//	"declared at /home/u/proj/build.yml:12"          (direct declaration)
//	"inherited from include <probes/system.yml>"     (via include)
//	"inherited from parent scope /"                  (from enclosing scope)
//
// Stage 8's `gmk explain` will format this into multi-line provenance
// blocks. Stage 2 emits the one-line form for error messages.
func LookupSource(s *ir.Scope, name string) string {
	v, found := Lookup(s, name)
	if v == nil {
		return ""
	}

	var b strings.Builder
	if found == s {
		b.WriteString("declared at ")
		b.WriteString(v.Source.String())
		return b.String()
	}

	// Found in some other scope — could be an include or a parent.
	// We disambiguate based on whether `found` is reachable from s via
	// Parent only (then it's a parent) or via Includes (then it's an include).
	if isParentReachable(s, found) {
		b.WriteString("inherited from parent scope ")
		b.WriteString(found.Path)
		return b.String()
	}

	b.WriteString("inherited from include ")
	b.WriteString(found.Path)
	return b.String()
}

// isParentReachable reports whether 'target' is reachable from 'from' by
// walking Parent links only (no include traversal).
func isParentReachable(from, target *ir.Scope) bool {
	for s := from; s != nil; s = s.Parent {
		if s == target {
			return true
		}
	}
	return false
}
