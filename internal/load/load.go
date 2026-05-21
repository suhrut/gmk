// Package load reads gmk YAML files from disk and converts them into the
// internal IR.
//
// The package isolates the IR types from any YAML library dependency. The
// goccy/go-yaml structs used here are package-private so changes to the YAML
// library never propagate to the rest of the codebase. Stage 6 will add IR
// serialization to MessagePack; that change happens entirely in the ir
// package and a sibling serdes package, with no touch to load.
package load

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"

	"github.com/suhrut/gmk/internal/ir"
)

// rawProject mirrors the on-disk YAML shape. Stage 1 supports the
// minimum: a vars map of strings, a targets map of objects. Adding
// includes, conditional sections, and tag handling will extend this
// struct in later stages.
type rawProject struct {
	Vars    map[string]string     `yaml:"vars,omitempty"`
	Targets map[string]*rawTarget `yaml:"targets,omitempty"`
}

type rawTarget struct {
	Run  string `yaml:"run"`
	Lang string `yaml:"lang,omitempty"`
}

// Load reads a YAML file at the given path and returns a parsed Project.
//
// Errors are wrapped with the source path so they're useful in cobra's
// default error display.
func Load(path string) (*ir.Project, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("load %s: resolve absolute path: %w", path, err)
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", abs, err)
	}

	return parseBytes(abs, data)
}

// parseBytes is the byte-level entry point, exported only via Load.
// Separating it allows tests to construct in-memory YAML without touching
// the filesystem.
func parseBytes(sourcePath string, data []byte) (*ir.Project, error) {
	var raw rawProject
	if err := yaml.Unmarshal(data, &raw); err != nil {
		// goccy/go-yaml errors include file position when used with
		// yaml.UseLineNumber, but at this top level we just wrap.
		return nil, fmt.Errorf("parse %s: %w", sourcePath, err)
	}

	if err := validate(&raw, sourcePath); err != nil {
		return nil, err
	}

	return toIR(&raw, sourcePath), nil
}

// validate enforces Stage 1 schema rules and returns an error citing the
// source file when violated.
//
// Stage 1 rules:
//   - every target must have a non-empty `run` field
//
// Future stages will add more rules here as the schema grows. The function
// signature stays stable.
func validate(raw *rawProject, source string) error {
	for name, t := range raw.Targets {
		if t == nil {
			return fmt.Errorf("validate %s: target %q has no body", source, name)
		}
		if t.Run == "" {
			return fmt.Errorf("validate %s: target %q is missing required field 'run'", source, name)
		}
	}
	return nil
}

// toIR converts the raw on-disk representation into the IR types used by
// the rest of the codebase.
//
// VarOrder cannot be reliably preserved from Go's map iteration, but Stage 3
// will switch to a parsing path that records declaration order. For Stage 1,
// we sort alphabetically for determinism.
func toIR(raw *rawProject, sourcePath string) *ir.Project {
	p := &ir.Project{
		SourcePath: sourcePath,
		Root:       filepath.Dir(sourcePath),
		Vars:       make(map[string]*ir.Var, len(raw.Vars)),
		VarOrder:   make([]string, 0, len(raw.Vars)),
		Targets:    make(map[string]*ir.Target, len(raw.Targets)),
	}

	// Stage 1: alphabetical order is deterministic; Stage 3 replaces with
	// declaration order. Either way, downstream code uses VarOrder, never
	// map iteration directly.
	names := sortedKeys(raw.Vars)
	for _, name := range names {
		p.Vars[name] = &ir.Var{
			Name:  name,
			Value: raw.Vars[name],
			Source: ir.SourceLoc{
				File: sourcePath,
				// Line/column unavailable from the simple unmarshal path;
				// Stage 3's AST-based parser will populate these.
			},
		}
		p.VarOrder = append(p.VarOrder, name)
	}

	for name, rt := range raw.Targets {
		lang := rt.Lang
		if lang == "" {
			lang = "bash"
		}
		p.Targets[name] = &ir.Target{
			Name: name,
			Run:  rt.Run,
			Lang: lang,
			Source: ir.SourceLoc{
				File: sourcePath,
			},
		}
	}

	return p
}

// sortedKeys returns map keys in lexicographic order for deterministic
// processing. Stage 3 replaces this with AST-walk-based ordering.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Simple insertion sort; tiny n, no need for sort.Strings dependency
	// at this level. Replaced in S3 with proper ordering.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
