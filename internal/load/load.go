// Package load reads gmk YAML files from disk and converts them into the
// internal IR. Stage 3a switches the underlying YAML parser from
// yaml.Unmarshal (struct-based, order-losing) to goccy/go-yaml's AST mode
// (via the yamlast.go wrapper), which gives us:
//
//  1. Declaration-order preservation across vars blocks
//  2. Source positions on every value
//  3. Visibility of YAML tags (!sh, !env, ...) for Stage 3b
//
// Var values that contain ${...} substitutions or expression operators are
// parsed by the expr package at load time. The resulting AST is stored in
// Var.Expr; plain literal values skip the parse and live in Var.Value only.
//
// The package isolates the IR types from any YAML library dependency: all
// goccy AST usage is in yamlast.go.
package load

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/ir"
)

// ErrIncludeCycle indicates that include resolution detected a cycle:
// file A includes B (directly or transitively) which includes A.
var ErrIncludeCycle = errors.New("include cycle")

// ErrIncludeNotFound indicates a "<lib>" include couldn't be found in
// the search path.
var ErrIncludeNotFound = errors.New("include not found")

// ErrInvalidIncludeSpec indicates an includes: entry didn't match either
// local-path syntax ("./..." / "/...") or library syntax ("<...>").
var ErrInvalidIncludeSpec = errors.New("invalid include spec")

// ErrTaggedValue is returned when Stage 3a encounters a YAML tag on a
// value (e.g. `!sh "git rev-parse HEAD"`). Tags are reserved for Stage 3b;
// this error makes it clear they're not silently dropped.
var ErrTaggedValue = errors.New("YAML tags not yet supported (Stage 3b)")

// Load reads a YAML file at the given path, resolves all transitive
// includes, and returns a fully populated Project.
//
// Includes are loaded eagerly: every "<lib>" or "./local.yml" entry is
// recursively Loaded into its own Project, attached as ir.Include with a
// Project pointer. Lookup uses scope.Lookup to walk through these.
//
// Include cycles are detected and return ErrIncludeCycle wrapped with
// the include chain.
func Load(path string) (*ir.Project, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("load %s: resolve absolute path: %w", path, err)
	}
	return loadWithChain(abs, nil)
}

// loadWithChain is the recursive entry point. The `chain` slice tracks
// absolute paths visited so far on the current include branch, used for
// cycle detection.
func loadWithChain(absPath string, chain []string) (*ir.Project, error) {
	// Cycle check.
	for _, prior := range chain {
		if prior == absPath {
			fullChain := append([]string{}, chain...)
			fullChain = append(fullChain, absPath)
			return nil, fmt.Errorf("%w: %s", ErrIncludeCycle, strings.Join(fullChain, " -> "))
		}
	}

	if _, err := os.Stat(absPath); err != nil {
		return nil, fmt.Errorf("load %s: %w", absPath, err)
	}

	top, err := parseYAMLFile(absPath)
	if err != nil {
		return nil, err
	}

	// Empty/null top-level file is treated as an empty project.
	if top.IsNull() {
		return emptyProject(absPath), nil
	}

	topMap, err := top.AsMapping()
	if err != nil {
		return nil, fmt.Errorf("load %s: top-level must be a mapping: %w", absPath, err)
	}

	// Pass 1: validate keys against the closed schema.
	if err := validateTopLevelKeys(topMap, absPath); err != nil {
		return nil, err
	}

	p := emptyProject(absPath)

	// Pass 2: walk top-level entries in declaration order, dispatching
	// by key name. Vars blocks accumulate in the order they appear in the
	// file (vars first, then vars_1, then vars_2, ...). Within each block,
	// var declaration order is preserved.
	var includeSpecs []includeSpec
	for _, entry := range topMap.Entries {
		switch {
		case entry.Key == "includes":
			seq, err := entry.Value.AsSequence()
			if err != nil {
				return nil, fmt.Errorf("load %s: includes: %w", absPath, err)
			}
			for _, item := range seq.Items {
				if item.Tag() != "" {
					return nil, fmt.Errorf("load %s:%d:%d: includes entries cannot be tagged: %w",
						absPath, item.Line, item.Col, ErrTaggedValue)
				}
				spec, err := item.AsString()
				if err != nil {
					return nil, fmt.Errorf("load %s: includes: %w", absPath, err)
				}
				includeSpecs = append(includeSpecs, includeSpec{spec: spec, line: item.Line, col: item.Col})
			}

		case entry.Key == "vars" || isVarsBlockKey(entry.Key):
			if err := loadVarsBlock(p, entry, absPath); err != nil {
				return nil, err
			}

		case entry.Key == "targets":
			if err := loadTargets(p, entry, absPath); err != nil {
				return nil, err
			}

		default:
			// validateTopLevelKeys should have caught this; defensive.
			return nil, fmt.Errorf("load %s:%d:%d: unknown top-level key %q",
				absPath, entry.KeyLine, entry.KeyCol, entry.Key)
		}
	}

	// Alias top-level Vars/VarOrder to RootScope's for Stage 1 backwards-compat.
	p.Vars = p.RootScope.Vars
	p.VarOrder = p.RootScope.VarOrder

	// Pass 3: load includes recursively. We do this after the local file is
	// fully parsed so an include error doesn't mask a local parse error.
	newChain := append(append([]string{}, chain...), absPath)
	for _, is := range includeSpecs {
		inc, err := loadInclude(is.spec, p.Root, newChain, absPath)
		if err != nil {
			return nil, err
		}
		p.RootScope.Includes = append(p.RootScope.Includes, inc)
	}

	return p, nil
}

// emptyProject returns a blank Project shell with an initialized RootScope.
func emptyProject(absPath string) *ir.Project {
	p := &ir.Project{
		SourcePath: absPath,
		Root:       filepath.Dir(absPath),
		Targets:    make(map[string]*ir.Target),
	}
	p.RootScope = &ir.Scope{
		Path:     "/",
		Vars:     make(map[string]*ir.Var),
		VarOrder: make([]string, 0),
	}
	p.Vars = p.RootScope.Vars
	p.VarOrder = p.RootScope.VarOrder
	return p
}

// includeSpec captures one includes: entry along with its YAML position
// for error reporting.
type includeSpec struct {
	spec string
	line int
	col  int
}

// loadVarsBlock processes one "vars" or "vars_N" entry. Adds vars to the
// project's RootScope in YAML declaration order. Later blocks override
// earlier ones (vars_2 wins over vars_1 wins over vars), matching the
// Stage 2 semantics — but within a single block, order is now true
// declaration order rather than alphabetical.
func loadVarsBlock(p *ir.Project, entry yamlMapEntry, file string) error {
	m, err := entry.Value.AsMapping()
	if err != nil {
		return fmt.Errorf("load %s: %s: must be a mapping: %w", file, entry.Key, err)
	}
	for _, kv := range m.Entries {
		// Reject tagged var values in 3a; Stage 3b will dispatch them.
		if tag := kv.Value.Tag(); tag != "" {
			return fmt.Errorf("load %s:%d:%d: %s.%s uses tag %s: %w",
				file, kv.Value.Line, kv.Value.Col, entry.Key, kv.Key, tag, ErrTaggedValue)
		}
		raw, err := kv.Value.AsString()
		if err != nil {
			return fmt.Errorf("load %s: %s.%s: %w", file, entry.Key, kv.Key, err)
		}
		v, err := buildVar(kv.Key, raw, ir.SourceLoc{
			File: file, Line: kv.Value.Line, Column: kv.Value.Col,
		})
		if err != nil {
			return fmt.Errorf("load %s:%d:%d: %s.%s: %w",
				file, kv.Value.Line, kv.Value.Col, entry.Key, kv.Key, err)
		}
		// Last-write-wins across blocks, but maintain declaration order.
		if _, existed := p.RootScope.Vars[kv.Key]; !existed {
			p.RootScope.VarOrder = append(p.RootScope.VarOrder, kv.Key)
		}
		p.RootScope.Vars[kv.Key] = v
	}
	return nil
}

// buildVar parses a var's raw string value as an expression template and
// constructs an ir.Var. Pure-literal values (no ${...}) are stored as
// VarLiteral with Expr=nil for a zero-evaluation fast path in resolve.
func buildVar(name, raw string, src ir.SourceLoc) (*ir.Var, error) {
	exprPos := expr.Position{File: src.File, Line: src.Line, Col: src.Column}
	node, err := expr.ParseTemplate(raw, exprPos)
	if err != nil {
		return nil, err
	}
	v := &ir.Var{
		Name:   name,
		Value:  raw,
		Source: src,
	}
	if expr.IsLiteral(node) {
		v.Kind = ir.VarLiteral
		v.Expr = nil
	} else {
		v.Kind = ir.VarExpression
		v.Expr = node
	}
	return v, nil
}

// loadTargets processes the "targets" mapping into ir.Target entries.
func loadTargets(p *ir.Project, entry yamlMapEntry, file string) error {
	m, err := entry.Value.AsMapping()
	if err != nil {
		return fmt.Errorf("load %s: targets: %w", file, err)
	}
	for _, kv := range m.Entries {
		t, err := loadTarget(kv, file)
		if err != nil {
			return err
		}
		p.Targets[kv.Key] = t
	}
	return nil
}

// loadTarget converts one target mapping entry into an ir.Target.
func loadTarget(kv yamlMapEntry, file string) (*ir.Target, error) {
	name := kv.Key
	body, err := kv.Value.AsMapping()
	if err != nil {
		return nil, fmt.Errorf("load %s: target %q: %w", file, name, err)
	}
	t := &ir.Target{
		Name:   name,
		Lang:   "bash",
		Source: ir.SourceLoc{File: file, Line: kv.Value.Line, Column: kv.Value.Col},
	}

	// Closed schema for target body: only known keys allowed.
	allowed := map[string]bool{
		"run": true, "lang": true, "deps": true,
		"env": true, "cwd": true, "phony": true,
	}
	for _, te := range body.Entries {
		if !allowed[te.Key] {
			return nil, fmt.Errorf("load %s:%d:%d: target %q has unknown field %q",
				file, te.KeyLine, te.KeyCol, name, te.Key)
		}
		switch te.Key {
		case "run":
			s, err := te.Value.AsString()
			if err != nil {
				return nil, fmt.Errorf("load %s: target %q.run: %w", file, name, err)
			}
			t.Run = s
		case "lang":
			s, err := te.Value.AsString()
			if err != nil {
				return nil, fmt.Errorf("load %s: target %q.lang: %w", file, name, err)
			}
			if s != "" {
				t.Lang = s
			}
		case "deps":
			seq, err := te.Value.AsSequence()
			if err != nil {
				return nil, fmt.Errorf("load %s: target %q.deps: %w", file, name, err)
			}
			for _, item := range seq.Items {
				s, err := item.AsString()
				if err != nil {
					return nil, fmt.Errorf("load %s: target %q.deps: %w", file, name, err)
				}
				t.Deps = append(t.Deps, s)
			}
		case "env":
			em, err := te.Value.AsMapping()
			if err != nil {
				return nil, fmt.Errorf("load %s: target %q.env: %w", file, name, err)
			}
			if t.Env == nil {
				t.Env = make(map[string]string, len(em.Entries))
			}
			for _, envKV := range em.Entries {
				s, err := envKV.Value.AsString()
				if err != nil {
					return nil, fmt.Errorf("load %s: target %q.env.%s: %w", file, name, envKV.Key, err)
				}
				t.Env[envKV.Key] = s
			}
		case "cwd":
			s, err := te.Value.AsString()
			if err != nil {
				return nil, fmt.Errorf("load %s: target %q.cwd: %w", file, name, err)
			}
			t.Cwd = s
		case "phony":
			b, err := te.Value.AsBool()
			if err != nil {
				return nil, fmt.Errorf("load %s: target %q.phony: %w", file, name, err)
			}
			t.Phony = b
		}
	}

	// Stage 2 validation, retained.
	if t.Run == "" {
		return nil, fmt.Errorf("validate %s: target %q is missing required field 'run'", file, name)
	}
	for _, d := range t.Deps {
		if d == name {
			return nil, fmt.Errorf("validate %s: target %q has itself as a dependency", file, name)
		}
	}
	return t, nil
}

// loadInclude resolves a single includes: entry and loads it as a sub-project.
func loadInclude(spec, projRoot string, chain []string, including string) (*ir.Include, error) {
	if libSpec, isLib := IsLibInclude(spec); isLib {
		resolved, err := ResolveLibInclude(libSpec, projRoot)
		if err != nil {
			return nil, fmt.Errorf("load %s: include %q: %w", including, spec, err)
		}
		subProj, err := loadWithChain(resolved, chain)
		if err != nil {
			return nil, err
		}
		subProj.RootScope.Path = fmt.Sprintf("/include[%s]", spec)
		return &ir.Include{
			Spec:     spec,
			Resolved: resolved,
			Kind:     ir.IncludeLibrary,
			Project:  subProj,
			Source:   ir.SourceLoc{File: including},
		}, nil
	}

	if IsLocalInclude(spec) {
		var resolved string
		if filepath.IsAbs(spec) {
			resolved = spec
		} else {
			resolved = filepath.Join(projRoot, spec)
		}
		resolved = filepath.Clean(resolved)
		if _, err := os.Stat(resolved); err != nil {
			return nil, fmt.Errorf("load %s: include %q: %w", including, spec, err)
		}
		subProj, err := loadWithChain(resolved, chain)
		if err != nil {
			return nil, err
		}
		subProj.RootScope.Path = fmt.Sprintf("/include[%s]", spec)
		return &ir.Include{
			Spec:     spec,
			Resolved: resolved,
			Kind:     ir.IncludeLocal,
			Project:  subProj,
			Source:   ir.SourceLoc{File: including},
		}, nil
	}

	return nil, fmt.Errorf("load %s: include %q: %w (must be \"./...\", \"/...\", \"../...\", or \"<lib>\")",
		including, spec, ErrInvalidIncludeSpec)
}

// isVarsBlockKey recognises "vars_N" for non-negative integer N. The bare
// "vars" key is handled separately (no suffix).
func isVarsBlockKey(key string) bool {
	if !strings.HasPrefix(key, "vars_") {
		return false
	}
	suffix := strings.TrimPrefix(key, "vars_")
	if suffix == "" {
		return false
	}
	n, err := strconv.Atoi(suffix)
	if err != nil || n < 0 {
		return false
	}
	return true
}

// validateTopLevelKeys enforces the Stage 3a closed schema: only known
// top-level keys are allowed.
func validateTopLevelKeys(topMap *yamlMap, source string) error {
	seen := make(map[string]bool, len(topMap.Entries))
	for _, e := range topMap.Entries {
		if seen[e.Key] {
			return fmt.Errorf("load %s:%d:%d: duplicate top-level key %q",
				source, e.KeyLine, e.KeyCol, e.Key)
		}
		seen[e.Key] = true

		switch e.Key {
		case "includes", "targets", "vars":
			continue
		}
		if isVarsBlockKey(e.Key) {
			continue
		}
		return fmt.Errorf("load %s:%d:%d: unknown top-level key %q "+
			"(Stage 3a accepts: includes, vars, vars_N, targets)",
			source, e.KeyLine, e.KeyCol, e.Key)
	}
	return nil
}
