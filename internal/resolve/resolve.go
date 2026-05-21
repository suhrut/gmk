// Package resolve provides variable lookup and ${...} substitution for
// gmk var references inside strings.
//
// Stage 1 supports only bare ${NAME} references against the project's
// Vars map. Stage 3 will:
//   - replace this with a full expression evaluator
//   - support typed refs (${env:HOME}, ${ctx:JWT}, etc.)
//   - support modifiers (${var:-default}, ${var:?error})
//   - support function calls and pipelines
//
// The exported API surface (Resolve, ResolveString) stays the same across
// stages. Callers in materialize/ and cli/ remain unchanged when the
// evaluator is upgraded.
package resolve

import (
	"errors"
	"fmt"
	"strings"

	"github.com/suhrut/gmk/internal/ir"
)

// ErrUndefined indicates a referenced var was not found in scope.
//
// Stage 4+ may differentiate "undefined" from "deferred" (lazy refs that
// have not yet been evaluated). Callers should use errors.Is for forward
// compatibility.
var ErrUndefined = errors.New("undefined var reference")

// Resolve returns the resolved string value of a named var in the project.
//
// Stage 1: looks up p.Vars[name].Value and substitutes any nested ${...}
// references it contains, recursively.
// Stage 2: walks the scope chain (parent scopes if not found locally).
// Stage 3: evaluates the var's expression tree.
//
// Returns ErrUndefined wrapped with the var name if not found.
func Resolve(name string, p *ir.Project) (string, error) {
	v, ok := p.Vars[name]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUndefined, name)
	}
	return ResolveString(v.Value, p)
}

// ResolveString substitutes all ${NAME} references in s with their resolved
// values from p.
//
// Stage 1 grammar (intentionally minimal):
//
//	${NAME}       - bare reference to a var named NAME
//	$$            - literal dollar sign (escape)
//
// Anything else triggers a parse error. Stage 3 will dramatically extend
// the grammar; calls to ResolveString do not change.
//
// Resolution recurses: if a var's value contains ${other}, that ref is
// resolved too. Cycle detection guards against infinite loops.
func ResolveString(s string, p *ir.Project) (string, error) {
	return resolveStringWithStack(s, p, nil)
}

// resolveStringWithStack does the recursive work, threading a visited set
// to detect cycles. The set is a slice (not a map) because cycle depths
// are tiny in practice and slices give better error messages by preserving
// the offending chain.
func resolveStringWithStack(s string, p *ir.Project, stack []string) (string, error) {
	var b strings.Builder
	b.Grow(len(s))

	i := 0
	for i < len(s) {
		c := s[i]

		// Handle $$ escape: emit a single literal $
		if c == '$' && i+1 < len(s) && s[i+1] == '$' {
			b.WriteByte('$')
			i += 2
			continue
		}

		// Handle ${...} reference
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
				// Stage 3 will accept more shapes here (kinds, functions,
				// modifiers). For Stage 1 we're strict to surface ambiguity
				// early rather than silently masking it.
				return "", fmt.Errorf("invalid var reference ${%s} at position %d (Stage 1 supports only bare ${NAME})", name, i)
			}

			// Cycle check.
			for _, seen := range stack {
				if seen == name {
					return "", fmt.Errorf("cyclic var reference: %s -> %s", strings.Join(stack, " -> "), name)
				}
			}

			v, ok := p.Vars[name]
			if !ok {
				return "", fmt.Errorf("%w: %s", ErrUndefined, name)
			}

			// Recurse to resolve nested refs in the var's value.
			expanded, err := resolveStringWithStack(v.Value, p, append(stack, name))
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

// validVarName reports whether s is a syntactically valid bare var name
// for Stage 1: a letter or underscore followed by letters, digits, or
// underscores.
//
// Stage 3 broadens this when typed refs and function calls enter the
// grammar; the more permissive parser at that point replaces this check
// rather than extending it.
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
