// Policy and matcher logic for the hierarchical logger.
//
// The Policy maps logger names to levels. Lookup rules, in order:
//
//  1. Exact match wins.
//  2. Longest matching prefix-wildcard wins. A pattern ending in ".*"
//     matches any name with that prefix; e.g. "gmk.target.*" matches
//     "gmk.target.release" and "gmk.target.test-e2e".
//  3. The Default level applies if no pattern matches.
//
// Patterns may end in a single ".*" only. Mid-name wildcards
// ("gmk.*.parser") are intentionally NOT supported; they'd complicate
// matching without offering meaningful new expressivity since the user
// can always set the level on the affected leaf name directly.

package logger

import (
	"sort"
	"strings"
)

// Policy is the lookup table for level decisions, keyed by logger name.
type Policy struct {
	// Default is the level applied to names that don't match any rule.
	Default Level

	// exact holds rules for names without a trailing wildcard.
	exact map[string]Level

	// wildcards holds prefix-wildcard rules. Sorted by prefix length
	// descending so the first match found is the longest (most specific).
	wildcards []wildcardRule
}

type wildcardRule struct {
	prefix string // includes trailing "." but not the "*"
	level  Level
}

// NewPolicy returns a Policy with the given default level and no rules.
func NewPolicy(def Level) *Policy {
	return &Policy{
		Default: def,
		exact:   make(map[string]Level),
	}
}

// Set installs a rule. A pattern ending in ".*" is a prefix wildcard;
// otherwise it's an exact-name rule. Setting the same pattern twice
// overwrites.
func (p *Policy) Set(pattern string, level Level) {
	if p == nil {
		return
	}
	if strings.HasSuffix(pattern, ".*") {
		prefix := pattern[:len(pattern)-1] // keep the trailing "."
		// Drop any existing entry for this prefix, then re-insert sorted.
		for i, r := range p.wildcards {
			if r.prefix == prefix {
				p.wildcards = append(p.wildcards[:i], p.wildcards[i+1:]...)
				break
			}
		}
		p.wildcards = append(p.wildcards, wildcardRule{prefix: prefix, level: level})
		sort.SliceStable(p.wildcards, func(i, j int) bool {
			return len(p.wildcards[i].prefix) > len(p.wildcards[j].prefix)
		})
		return
	}
	if p.exact == nil {
		p.exact = make(map[string]Level)
	}
	p.exact[pattern] = level
}

// LevelFor returns the effective level for the given logger name.
func (p *Policy) LevelFor(name string) Level {
	if p == nil {
		return LevelInfo
	}
	if name != "" {
		if lev, ok := p.exact[name]; ok {
			return lev
		}
		for _, r := range p.wildcards {
			if strings.HasPrefix(name, r.prefix) {
				return r.level
			}
		}
	}
	return p.Default
}

// Enabled reports whether a record at the given level should be emitted
// for the given logger name. It's the policy lookup's primary use site.
func (p *Policy) Enabled(name string, level Level) bool {
	return level >= p.LevelFor(name)
}

// Clone returns a deep copy. Used by Configure to snapshot the policy
// before installing so subsequent mutations don't affect the live
// handler stack.
func (p *Policy) Clone() *Policy {
	if p == nil {
		return nil
	}
	out := &Policy{
		Default: p.Default,
		exact:   make(map[string]Level, len(p.exact)),
	}
	for k, v := range p.exact {
		out.exact[k] = v
	}
	out.wildcards = append(out.wildcards, p.wildcards...)
	return out
}

// Rules returns the rules as a flat name-to-level map, with wildcards
// rendered back to their "<prefix>.*" form. Sorted by name. For
// diagnostic output and JSON marshalling.
func (p *Policy) Rules() map[string]Level {
	if p == nil {
		return nil
	}
	out := make(map[string]Level, len(p.exact)+len(p.wildcards))
	for k, v := range p.exact {
		out[k] = v
	}
	for _, w := range p.wildcards {
		out[w.prefix+"*"] = w.level
	}
	return out
}

// NameMatcher decides whether a given logger name matches a set of
// patterns. Used by file routes so a file sink can capture e.g. only
// records under "gmk.target.*".
type NameMatcher struct {
	exact     map[string]struct{}
	wildcards []string // prefix forms with trailing "."
	matchAll  bool
}

// NewNameMatcher returns a matcher accepting the given patterns. A
// single pattern of "*" matches everything; otherwise patterns follow
// the same exact-vs-wildcard rules as Policy.
func NewNameMatcher(patterns ...string) *NameMatcher {
	m := &NameMatcher{exact: make(map[string]struct{})}
	for _, p := range patterns {
		if p == "*" {
			m.matchAll = true
			continue
		}
		if strings.HasSuffix(p, ".*") {
			m.wildcards = append(m.wildcards, p[:len(p)-1])
			continue
		}
		m.exact[p] = struct{}{}
	}
	return m
}

// Match reports whether name is accepted by the matcher.
func (m *NameMatcher) Match(name string) bool {
	if m == nil {
		return false
	}
	if m.matchAll {
		return true
	}
	if _, ok := m.exact[name]; ok {
		return true
	}
	for _, p := range m.wildcards {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
