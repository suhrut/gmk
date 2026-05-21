package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/suhrut/gmk/internal/dag"
	gexec "github.com/suhrut/gmk/internal/exec"
	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/load"
	"github.com/suhrut/gmk/internal/materialize"
	"github.com/suhrut/gmk/internal/resolve"
	"github.com/suhrut/gmk/internal/runner"
)

// newRunCmd returns the `gmk run <target>` command.
//
// Stage 2 flow:
//  1. Load project (with includes and scope tree)
//  2. Build a dep graph over all targets reachable from <target>
//  3. Topo-sort; execute each target in order via runner.ScriptRunner
//  4. On failure: stop, propagate the error with the target name
//
// Stage 2 flags:
//
//	--file/-f    path to top-level YAML (default "build.yml")
//	--dry-run    show what would run without executing (same as `gmk dryrun X`)
//
// Future stages add: -v / --verbose, --jobs N, --keep-going, etc.
func newRunCmd() *cobra.Command {
	var (
		file   string
		dryRun bool
	)

	cmd := &cobra.Command{
		Use:   "run <target>",
		Short: "Run a target (and its dependencies) from a gmk YAML file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeRun(cmd.OutOrStdout(), file, args[0], dryRun)
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "build.yml", "Path to the gmk YAML file")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would run without executing (same as `gmk dryrun`)")

	return cmd
}

// executeRun is the shared core of `gmk run` and `gmk dryrun`. The dryRun
// flag toggles between actual execution and printed-only mode.
//
// It's exported via lowercase-but-package-visible naming so dryrun.go can
// call into it without duplicating the load+plan logic.
func executeRun(out io.Writer, file, targetName string, dryRun bool) error {
	project, err := load.Load(file)
	if err != nil {
		return err
	}

	if _, ok := project.Targets[targetName]; !ok {
		return fmt.Errorf("target %q not found in %s", targetName, file)
	}

	// Build name -> deps map for the DAG.
	nodes := make(map[string][]string, len(project.Targets))
	for name, t := range project.Targets {
		nodes[name] = t.Deps
	}

	order, err := dag.TopoSort(nodes, []string{targetName})
	if err != nil {
		return fmt.Errorf("plan %s: %w", targetName, err)
	}

	if dryRun {
		return printDryRunPlan(out, project, order)
	}

	fmt.Fprintf(out, "gmk: running %s\n", formatList(order))

	for _, name := range order {
		if err := runOneTarget(out, project, project.Targets[name]); err != nil {
			return err
		}
	}
	return nil
}

// runOneTarget materializes and executes a single target. Errors are
// wrapped with the target name for clean diagnostic surfaces.
func runOneTarget(out io.Writer, project *ir.Project, t *ir.Target) error {
	fmt.Fprintf(out, "gmk: target %s\n", t.Name)

	scriptPath, err := materialize.WriteScript(t, project)
	if err != nil {
		return err
	}

	// Resolve target's Env against the project's root scope (Stage 2;
	// Stage 4 will use a per-target scope here).
	resolvedEnv, err := resolveTargetEnv(project, t)
	if err != nil {
		return fmt.Errorf("target %q: env resolution: %w", t.Name, err)
	}

	r := &runner.ScriptRunner{ScriptPath: scriptPath, Lang: t.Lang}
	input := map[string]any{}
	if len(resolvedEnv) > 0 {
		input["env"] = resolvedEnv
	}
	if t.Cwd != "" {
		input["cwd"] = t.Cwd
	}

	_, err = r.Run(input)
	if err != nil {
		var exitErr *gexec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("target %q failed (exit %d): %w", t.Name, exitErr.ExitCode, err)
		}
		return fmt.Errorf("target %q failed: %w", t.Name, err)
	}
	return nil
}

// resolveTargetEnv computes the env map for a target by resolving any
// ${...} substitutions in declared Env values against the project's root
// scope.
//
// Stage 2 resolves at run time (just before exec), per the design
// decision: this means S5+ lazy vars can be referenced in env without
// changing the call shape.
func resolveTargetEnv(project *ir.Project, t *ir.Target) (map[string]string, error) {
	if len(t.Env) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(t.Env))
	for k, v := range t.Env {
		resolved, err := resolve.ResolveStringInScope(v, project.RootScope)
		if err != nil {
			return nil, fmt.Errorf("env[%q]: %w", k, err)
		}
		out[k] = resolved
	}
	return out, nil
}
