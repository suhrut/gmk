// Package integration contains end-to-end tests that exercise the loader,
// scope walker, and expression evaluator together against real gmk.yml
// files in the examples/ tree.
//
// The contract these tests enforce:
//
//  1. Every gmk.yml under examples/ must parse (load.Load returns nil err).
//  2. Every var declared anywhere in that project must resolve cleanly
//     (resolve.Resolve returns nil err) given a controlled environment.
//  3. Every target's `deps:` must reference a target that actually exists.
//  4. Every target's `env:` block values must resolve cleanly.
//  5. Every target's `cwd:` (if set) must resolve cleanly.
//
// As new stages land (3b tags, S4 conditions, S5 outputs/inputs, etc.) we
// extend gmk *without* breaking these examples. If a future change makes
// any of them fail to load, parse, or resolve, this test catches it.
//
// To add a new example: drop a new gmk.yml under
// examples/<category>/<feature>/gmk.yml. It will be picked up
// automatically. Set env vars in setExampleEnv() if your example needs
// specific values to resolve.
package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/dag"
	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/load"
	"github.com/suhrut/gmk/internal/materialize"
	"github.com/suhrut/gmk/internal/render"
	"github.com/suhrut/gmk/internal/resolve"
	"github.com/suhrut/gmk/internal/runner"
	"github.com/suhrut/gmk/internal/template"
)

// examplesRoot returns the absolute path to the examples/ directory.
// The test binary runs from internal/integration/, so examples/ is at ../../examples.
func examplesRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Join(wd, "..", "..", "examples")
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("examples dir not found at %s: %v", abs, err)
	}
	return abs
}

// setExampleEnv configures the environment so examples that reference
// ${env:NAME} (without a :- default) still resolve deterministically.
// Examples should preferentially use :- defaults so they work in any env;
// this is the backstop for the few that don't.
func setExampleEnv(t *testing.T) {
	t.Helper()
	// USER is referenced by several examples. Set a stable value.
	t.Setenv("USER", "gmk-test")
}

// discoverExamples walks examples/ and returns the absolute path to every
// gmk.yml found at any depth. The tree intentionally has multiple levels
// (examples/<category>/<feature>/gmk.yml) and may grow deeper as
// libraries-of-libraries patterns appear.
func discoverExamples(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Only entry-point files are named gmk.yml. Included library
		// files (e.g. vars/common.yml, lib/shared.yml) have other names
		// and are exercised transitively via their includer.
		if d.Name() == "gmk.yml" {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("no gmk.yml files found under examples/")
	}
	return found
}

// TestExamplesLoad asserts every gmk.yml in the tree parses without error.
func TestExamplesLoad(t *testing.T) {
	setExampleEnv(t)
	root := examplesRoot(t)
	files := discoverExamples(t, root)

	for _, f := range files {
		f := f
		name := relName(root, f)
		t.Run(name, func(t *testing.T) {
			if _, err := load.Load(f); err != nil {
				t.Fatalf("load %s: %v", f, err)
			}
		})
	}
}

// TestExamplesResolveAllVars asserts every var in every loaded project
// resolves to a string without error. This covers Stage 3a's whole
// pipeline: template parse, ref lookup, modifier application, function
// dispatch, pipeline evaluation.
func TestExamplesResolveAllVars(t *testing.T) {
	setExampleEnv(t)
	root := examplesRoot(t)
	files := discoverExamples(t, root)

	for _, f := range files {
		f := f
		name := relName(root, f)
		t.Run(name, func(t *testing.T) {
			p, err := load.Load(f)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			// Walk vars in declaration order and resolve each.
			for _, vname := range p.VarOrder {
				if _, err := resolve.Resolve(vname, p); err != nil {
					t.Errorf("resolve var %q: %v", vname, err)
				}
			}
		})
	}
}

// TestExamplesTargetsConsistent asserts target-level invariants:
//   - every dep references a target that exists in the project
//   - every env: value resolves
//   - every cwd value resolves
//   - every Run script resolves as a template (including any ${render:...}
//     references — Stage 3c added this expansion path)
func TestExamplesTargetsConsistent(t *testing.T) {
	setExampleEnv(t)
	root := examplesRoot(t)
	files := discoverExamples(t, root)

	for _, f := range files {
		f := f
		name := relName(root, f)
		t.Run(name, func(t *testing.T) {
			p, err := load.Load(f)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			// Stage 3c: skip examples that need an engine this
			// build doesn't include — same logic as TestExamplesRun.
			if missing := exampleNeedsMissingEngine(p.Templates); missing != "" {
				t.Skipf("skipping: template engine %q not available in this build", missing)
			}
			// Stage 3c: examples may use ${render:tmpl(args)} in their
			// target bodies. Build a render dispatcher from the project's
			// templates so resolution doesn't fail with "no render
			// resolver configured" for legitimate template usage.
			renderDisp := render.New(p.Templates, template.Default)
			for tname, tgt := range p.Targets {
				// Deps must resolve to real targets.
				for _, dep := range tgt.Deps {
					if _, ok := p.Targets[dep]; !ok {
						t.Errorf("target %q: dep %q not found", tname, dep)
					}
				}
				// Env values must resolve as templates.
				for k, v := range tgt.Env {
					if _, err := resolve.ResolveStringInScopeWithRender(v, p.RootScope, renderDisp); err != nil {
						t.Errorf("target %q env[%s]: %v", tname, k, err)
					}
				}
				// Cwd must resolve.
				if tgt.Cwd != "" {
					if _, err := resolve.ResolveStringInScopeWithRender(tgt.Cwd, p.RootScope, renderDisp); err != nil {
						t.Errorf("target %q cwd: %v", tname, err)
					}
				}
				// Run script must resolve as a template, including any
				// ${render:...} expansions.
				if tgt.Run != "" {
					if _, err := resolve.ResolveStringInScopeWithRender(tgt.Run, p.RootScope, renderDisp); err != nil {
						t.Errorf("target %q run: %v", tname, err)
					}
				}
			}
		})
	}
}

// TestExamplesShape lightweight structural assertions that catch the most
// common authoring mistakes: empty project, no targets, etc.
func TestExamplesShape(t *testing.T) {
	setExampleEnv(t)
	root := examplesRoot(t)
	files := discoverExamples(t, root)

	for _, f := range files {
		f := f
		name := relName(root, f)
		t.Run(name, func(t *testing.T) {
			p, err := load.Load(f)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			// Stage 3b: an example may declare only functions (no
			// targets) — those exercise gmk-call, not gmk-run. Require
			// at least one runnable thing of either kind.
			if len(p.Targets) == 0 && len(p.Functions) == 0 {
				t.Errorf("%s has no targets and no functions — every example "+
					"should have at least one runnable callable", f)
			}
			// Sanity: VarOrder length matches Vars map.
			if len(p.VarOrder) != countTopLevelVars(p) {
				t.Errorf("VarOrder len=%d vs Vars count=%d in %s",
					len(p.VarOrder), countTopLevelVars(p), f)
			}
		})
	}
}

// relName returns a path relative to examplesRoot, with leading separators
// trimmed — for nice subtest names.
func relName(root, full string) string {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return full
	}
	return strings.TrimPrefix(rel, string(filepath.Separator))
}

// countTopLevelVars counts vars in the project's top-level scope only
// (not vars contributed via includes).
func countTopLevelVars(p *ir.Project) int {
	if p == nil || p.RootScope == nil {
		return 0
	}
	return len(p.RootScope.Vars)
}

// TestExamplesRun is the strongest regression assertion: it actually
// executes every example's targets, end-to-end. The pipeline mirrors
// what `gmk run` does internally:
//
//  1. Discover leaf targets (those nothing else depends on) — these are
//     the natural roots and pull all transitive deps with them.
//  2. Topo-sort to get execution order including deps.
//  3. For each target in order:
//     a. Materialize the resolved run script to disk.
//     b. Resolve env: and cwd: against the project's scope.
//     c. Hand it to runner.ScriptRunner, assert exit 0.
//
// Side effect: each example dir gets a .gmk-cache/code/local/... tree
// of materialized scripts. .gitignore already excludes .gmk-cache; the
// test cleans them up afterwards so the working tree stays tidy.
//
// Tests do NOT t.Parallel because the per-example temp paths are stable
// and we want deterministic ordering for debugging.
func TestExamplesRun(t *testing.T) {
	setExampleEnv(t)
	root := examplesRoot(t)
	files := discoverExamples(t, root)

	for _, f := range files {
		f := f
		name := relName(root, f)
		t.Run(name, func(t *testing.T) {
			project, err := load.Load(f)
			if err != nil {
				t.Fatalf("load: %v", err)
			}

			// Stage 3c: skip examples that need a template engine
			// this build doesn't include. The most common case is
			// -tags nogonja excluding jinja: the binary still works
			// for `engine: go` projects, but a project using the
			// default engine (jinja) can't be rendered. We skip
			// rather than fail so test runs are useful in both
			// build configurations.
			if missing := exampleNeedsMissingEngine(project.Templates); missing != "" {
				t.Skipf("skipping: template engine %q not available in this build", missing)
			}

			// Cleanup any .gmk-cache the test creates so reruns are clean.
			t.Cleanup(func() {
				cacheDir := filepath.Join(project.Root, ".gmk-cache")
				_ = os.RemoveAll(cacheDir)
			})

			leaves := findLeafTargets(project)
			if len(leaves) == 0 {
				// Stage 3b: examples may be function-only (no targets).
				// Skip the run-target test for those — they're exercised
				// by TestExamplesCall instead. We DO require either
				// targets OR functions; otherwise the example is empty.
				if len(project.Functions) == 0 {
					t.Fatalf("no leaf targets and no functions found in %s (empty?)", f)
				}
				t.Skipf("no targets to run (function-only example)")
			}

			// Build the dep graph for topo sort.
			graph := make(map[string][]string, len(project.Targets))
			for tn, tgt := range project.Targets {
				graph[tn] = tgt.Deps
			}

			order, err := dag.TopoSort(graph, leaves)
			if err != nil {
				t.Fatalf("topo sort: %v", err)
			}

			for _, tname := range order {
				tgt := project.Targets[tname]
				if err := runOneExampleTarget(project, tgt); err != nil {
					t.Errorf("target %q: %v", tname, err)
					return
				}
			}
		})
	}
}

// findLeafTargets returns the names of targets that no other target
// depends on. These are the natural entry points (like a `make all`
// without an explicit target). Sorted for deterministic ordering.
func findLeafTargets(p *ir.Project) []string {
	if p == nil {
		return nil
	}
	dependedOn := make(map[string]bool, len(p.Targets))
	for _, tgt := range p.Targets {
		for _, dep := range tgt.Deps {
			dependedOn[dep] = true
		}
	}
	var leaves []string
	for tname := range p.Targets {
		if !dependedOn[tname] {
			leaves = append(leaves, tname)
		}
	}
	sort.Strings(leaves)
	return leaves
}

// runOneExampleTarget mirrors cli/run.go's runOneTarget closely but is
// kept here to keep the integration test free of cobra and to make the
// regression assertion explicit: this is the contract the cli must
// honour, and the cli is just one of several future drivers (Stage 12's
// daemon being another).
func runOneExampleTarget(project *ir.Project, t *ir.Target) error {
	scriptPath, err := materialize.WriteScript(t, project)
	if err != nil {
		return fmt.Errorf("materialize: %w", err)
	}

	env, err := resolveExampleTargetEnv(project, t)
	if err != nil {
		return fmt.Errorf("env: %w", err)
	}

	var cwd string
	if t.Cwd != "" {
		cwd, err = resolve.ResolveStringInScope(t.Cwd, project.RootScope)
		if err != nil {
			return fmt.Errorf("cwd: %w", err)
		}
	}

	r := &runner.ScriptRunner{ScriptPath: scriptPath, Lang: t.Lang}
	input := map[string]any{}
	if len(env) > 0 {
		input["env"] = env
	}
	if cwd != "" {
		input["cwd"] = cwd
	}

	out, err := r.Run(input)
	if err != nil {
		return fmt.Errorf("exec (exit_code=%v): %w", out["exit_code"], err)
	}
	if code, ok := out["exit_code"].(int); ok && code != 0 {
		return fmt.Errorf("non-zero exit code: %d", code)
	}
	return nil
}

// resolveExampleTargetEnv resolves a target's env block. Same shape as
// the cli's resolveTargetEnv — duplicated here to keep the integration
// test free of cli/cobra imports.
func resolveExampleTargetEnv(p *ir.Project, t *ir.Target) (map[string]string, error) {
	if len(t.Env) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(t.Env))
	for k, v := range t.Env {
		rv, err := resolve.ResolveStringInScope(v, p.RootScope)
		if err != nil {
			return nil, fmt.Errorf("env[%s]: %w", k, err)
		}
		out[k] = rv
	}
	return out, nil
}
