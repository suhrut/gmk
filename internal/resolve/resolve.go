// Package resolve provides variable lookup and ${...} substitution for
// gmk var references inside strings.
//
// Stage 2 extends Stage 1 with scope-aware variants:
//
//   - Resolve / ResolveString:               operate against a Project's
//     root scope (unchanged Stage 1 signature; now delegates to scope walks)
//   - ResolveInScope / ResolveStringInScope: operate against an arbitrary
//     scope, enabling target-level env resolution and (in S4+) per-target
//     scopes with overrides.
//
// The exported API across both pairs stays stable across stages. Stage 3
// will replace the substitution grammar with a full expression parser
// supporting typed refs (${env:HOME}, ${ctx:JWT}) and modifiers, but the
// function signatures here are the long-term contract.
package resolve

import (
	"errors"
	"fmt"
	"strings"

	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/scope"
)

// ErrUndefined indicates a referenced var was not found in scope.
var ErrUndefined = errors.New("undefined var reference")

// Resolve returns the resolved string value of a named var in the project's
// root scope. Stage 1 backwards-compat wrapper around ResolveInScope.
func Resolve(name string, p *ir.Project) (string, error) {
	if p == nil || p.RootScope == nil {
		return "", fmt.Errorf("%w: %s (project has no root scope)", ErrUndefined, name)
	}
	return ResolveInScope(name, p.RootScope)
}

// ResolveString substitutes all ${NAME} references in s against the project's
// root scope. Stage 1 backwards-compat wrapper around ResolveStringInScope.
func ResolveString(s string, p *ir.Project) (string, error) {
	if p == nil || p.RootScope == nil {
		return "", fmt.Errorf("ResolveString: project has no root scope")
	}
	return ResolveStringInScope(s, p.RootScope)
}

// ResolveInScope returns the resolved string value of a named var found by
// walking from the given scope outward (see scope.Lookup for the walk order).
//
// Returns ErrUndefined wrapped with the var name if not found anywhere in
// the scope chain or its includes.
func ResolveInScope(name string, sc *ir.Scope) (string, error) {
	if sc == nil {
		return "", fmt.Errorf("%w: %s (nil scope)", ErrUndefined, name)
	}
	v, _ := scope.Lookup(sc, name)
	if v == nil {
		return "", fmt.Errorf("%w: %s", ErrUndefined, name)
	}
	return ResolveStringInScope(v.Value, sc)
}

// ResolveStringInScope substitutes all ${NAME} references in s with their
// resolved values from the given scope.
//
// Stage 2 grammar (unchanged from Stage 1, just now walks scopes):
//
//	${NAME}       - bare reference to a var named NAME
//	$$            - literal dollar sign (escape)
//
// Stage 3 will dramatically extend the grammar. The function signature
// stays the same across stages.
//
// Resolution recurses: if a var's value contains ${other}, that ref is
// resolved too. Cycle detection guards against infinite loops.
func ResolveStringInScope(s string, sc *ir.Scope) (string, error) {
	return resolveStringWithStack(s, sc, nil)
}

func resolveStringWithStack(s string, sc *ir.Scope, stack []string) (string, error) {
	var b strings.Builder
	b.Grow(len(s))

	i := 0
	for i < len(s) {
		c := s[i]

		if c == '$' && i+1 < len(s) && s[i+1] == '$' {
			b.WriteByte('$')
			i += 2
			continue
		}

		if c == '$' && i+1 < len(s) && s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				return "", fmt.Errorf("unterminated ${ at position %d in %q", i, s)
			}
			name := s[i+2 : i+2+end]
			if name == "" {
				return "", fmt.Errorf("empty ${} at position %d in %q", i, s)
			}
			if !validVarName(name) {
				return "", fmt.Errorf("invalid var reference ${%s} at position %d (Stage 2 supports only bare ${NAME})", name, i)
			}

			for _, seen := range stack {
				if seen == name {
					return "", fmt.Errorf("cyclic var reference: %s -> %s", strings.Join(stack, " -> "), name)
				}
			}

			v, _ := scope.Lookup(sc, name)
			if v == nil {
				return "", fmt.Errorf("%w: %s", ErrUndefined, name)
			}

			expanded, err := resolveStringWithStack(v.Value, sc, append(stack, name))
			if err != nil {
				return "", err
			}
			b.WriteString(expanded)

			i += 2 + end + 1
			continue
		}

		b.WriteByte(c)
		i++
	}

	return b.String(), nil
}

func validVarName(s string) bool {
	if s == "" {
		return false
	}
	if !isNameStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isNameCont(s[i]) {
			return false
		}
	}
	return true
}

func isNameStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isNameCont(c byte) bool {
	return isNameStart(c) || (c >= '0' && c <= '9')
}
