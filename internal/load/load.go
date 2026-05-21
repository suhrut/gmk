// Package load reads gmk YAML files from disk and converts them into the
// internal IR. Stage 2 adds includes, multiple vars_N blocks, scope tree
// construction, and the new target fields (deps, env, cwd, phony).
//
// The package isolates the IR types from any YAML library dependency.
// The goccy/go-yaml structs used here are package-private so changes to
// the YAML library never propagate to the rest of the codebase.
package load

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"

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

// rawProject mirrors the on-disk YAML shape for Stage 2. Unknown top-level
// keys are kept here as a separate Extra map and validated at convert time
// (Stage 4 may relax this for plugin-defined keys).
type rawProject struct {
	Includes []string              `yaml:"includes,omitempty"`
	Vars     map[string]string     `yaml:"vars,omitempty"`
	Targets  map[string]*rawTarget `yaml:"targets,omitempty"`

	// Captured separately so we can route vars_N keys without making the
	// schema fully open. goccy/go-yaml's tag parser handles `,inline`
	// imperfectly for our needs, so we deserialize to map[string]any and
	// then sift in the second pass below.
}

type rawTarget struct {
	Run   string            `yaml:"run"`
	Lang  string            `yaml:"lang,omitempty"`
	Deps  []string          `yaml:"deps,omitempty"`
	Env   map[string]string `yaml:"env,omitempty"`
	Cwd   string            `yaml:"cwd,omitempty"`
	Phony bool              `yaml:"phony,omitempty"`
}

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

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", absPath, err)
	}

	// Two-pass parse: first decode as a generic map to discover all top-level
	// keys (so we can find vars_N blocks); then decode into the typed
	// rawProject for the keys we know how to handle.
	var top map[string]any
	if err := yaml.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parse %s: %w", absPath, err)
	}

	var raw rawProject
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", absPath, err)
	}

	// Discover vars_N keys (numeric suffix) in declaration-order-ish:
	// YAML decode into map[string]any doesn't preserve key order from goccy
	// by default, so we sort numerically by N. N=0 is the bare "vars" key.
	varsBlocks, err := collectVarsBlocks(top, absPath)
	if err != nil {
		return nil, err
	}

	// Validate the known keys; reject unrecognized top-level keys (closed
	// schema for Stage 2). Acceptable keys: includes, vars, vars_N, targets.
	if err := validateTopLevelKeys(top, absPath); err != nil {
		return nil, err
	}

	if err := validateTargets(&raw, absPath); err != nil {
		return nil, err
	}

	// Build the project shell first; includes get attached after recursive load.
	p := &ir.Project{
		SourcePath: absPath,
		Root:       filepath.Dir(absPath),
		Targets:    make(map[string]*ir.Target, len(raw.Targets)),
	}
	p.RootScope = &ir.Scope{
		Path:     "/",
		Vars:     make(map[string]*ir.Var),
		VarOrder: make([]string, 0),
	}

	// Merge all vars blocks into RootScope in order (vars, vars_1, vars_2, ...).
	for _, block := range varsBlocks {
		for _, name := range block.order {
			val := block.vars[name]
			// Later blocks override earlier ones — last write wins. That
			// matches reasonable user expectation: vars_2 overrides vars_1.
			p.RootScope.Vars[name] = &ir.Var{
				Name:   name,
				Value:  val,
				Source: ir.SourceLoc{File: absPath},
			}
			// Maintain VarOrder uniqueness.
			if !containsString(p.RootScope.VarOrder, name) {
				p.RootScope.VarOrder = append(p.RootScope.VarOrder, name)
			}
		}
	}

	// Alias top-level Vars/VarOrder to RootScope's for Stage 1 backwards-compat.
	p.Vars = p.RootScope.Vars
	p.VarOrder = p.RootScope.VarOrder

	// Populate Targets.
	for name, rt := range raw.Targets {
		lang := rt.Lang
		if lang == "" {
			lang = "bash"
		}
		p.Targets[name] = &ir.Target{
			Name:   name,
			Run:    rt.Run,
			Lang:   lang,
			Deps:   append([]string(nil), rt.Deps...), // defensive copy
			Env:    copyStringMap(rt.Env),
			Cwd:    rt.Cwd,
			Phony:  rt.Phony,
			Source: ir.SourceLoc{File: absPath},
		}
	}

	// Load includes, recursively.
	newChain := append(append([]string{}, chain...), absPath)
	for _, spec := range raw.Includes {
		inc, err := loadInclude(spec, p.Root, newChain, absPath)
		if err != nil {
			return nil, err
		}
		p.RootScope.Includes = append(p.RootScope.Includes, inc)
	}

	return p, nil
}

// loadInclude resolves a single includes: entry and loads it as a sub-project.
//
// spec is the raw includes: string. projRoot is the absolute directory of
// the including file (used to resolve local-path includes relative to it).
// chain is the include chain so far for cycle detection.
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

// varsBlock holds one vars_N block with its declaration-order keys.
type varsBlock struct {
	suffix int // 0 for bare "vars", 1+ for "vars_1", "vars_2", ...
	vars   map[string]string
	order  []string
}

// collectVarsBlocks finds all top-level keys matching "vars" or "vars_N"
// (where N is a non-negative integer) and returns them sorted by suffix.
// Inside each block, key order is alphabetical for determinism (Stage 3's
// AST-based parser will preserve declaration order).
func collectVarsBlocks(top map[string]any, source string) ([]varsBlock, error) {
	var blocks []varsBlock
	for key, val := range top {
		suffix, ok := varsBlockSuffix(key)
		if !ok {
			continue
		}
		raw, ok := val.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("load %s: %q must be a mapping of var-name to string value", source, key)
		}
		strs := make(map[string]string, len(raw))
		order := make([]string, 0, len(raw))
		for k, v := range raw {
			s, err := coerceToString(v)
			if err != nil {
				return nil, fmt.Errorf("load %s: %s.%s: %w", source, key, k, err)
			}
			strs[k] = s
			order = append(order, k)
		}
		sort.Strings(order)
		blocks = append(blocks, varsBlock{suffix: suffix, vars: strs, order: order})
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].suffix < blocks[j].suffix })
	return blocks, nil
}

// varsBlockSuffix recognises "vars" (suffix 0) and "vars_N" for non-negative
// integer N (suffix N). Returns false for anything else, including "vars.1"
// which is reserved for future folder-namespacing of vars.
func varsBlockSuffix(key string) (int, bool) {
	if key == "vars" {
		return 0, true
	}
	if !strings.HasPrefix(key, "vars_") {
		return 0, false
	}
	suffix := strings.TrimPrefix(key, "vars_")
	if suffix == "" {
		return 0, false
	}
	n, err := strconv.Atoi(suffix)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// coerceToString accepts the few scalar types YAML may decode a var value
// into (string, int, float, bool) and renders them as strings. Stage 3's
// typed expression system will replace this with proper type handling.
func coerceToString(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case int, int64, uint64:
		return fmt.Sprintf("%d", x), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(x), nil
	default:
		return "", fmt.Errorf("unsupported scalar type %T (Stage 2 supports string, int, float, bool)", v)
	}
}

// validateTopLevelKeys enforces the Stage 2 closed schema: only known
// top-level keys are allowed. Unknown keys are errors with a helpful
// message rather than silently ignored.
func validateTopLevelKeys(top map[string]any, source string) error {
	for key := range top {
		switch {
		case key == "includes", key == "targets", key == "vars":
			continue
		}
		if _, ok := varsBlockSuffix(key); ok {
			continue
		}
		return fmt.Errorf("load %s: unknown top-level key %q (Stage 2 accepts: includes, vars, vars_N, targets)", source, key)
	}
	return nil
}

// validateTargets enforces target-level schema rules for Stage 2.
func validateTargets(raw *rawProject, source string) error {
	for name, t := range raw.Targets {
		if t == nil {
			return fmt.Errorf("validate %s: target %q has no body", source, name)
		}
		if t.Run == "" {
			return fmt.Errorf("validate %s: target %q is missing required field 'run'", source, name)
		}
		// Reject obvious self-deps; the dag package will catch cycles too,
		// but a per-file message here gives a better diagnostic surface.
		for _, d := range t.Deps {
			if d == name {
				return fmt.Errorf("validate %s: target %q has itself as a dependency", source, name)
			}
		}
	}
	return nil
}

func containsString(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
