package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/suhrut/gmk/internal/dag"
	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/materialize"
	"github.com/suhrut/gmk/internal/render"
	"github.com/suhrut/gmk/internal/resolve"
	"github.com/suhrut/gmk/internal/runner"
	"github.com/suhrut/gmk/internal/store"
	"github.com/suhrut/gmk/internal/template"
)

// newRunCmd returns the `gmk run <target>` command.
//
// Stage 3b post-fix flow (was Stage 1 cache.WriteScript before):
//
//  1. Load project (with includes and scope tree)
//  2. Build a dep graph over all targets reachable from <target>
//  3. Topo-sort
//  4. Open SQLite store at .gmk-cache/gmk.db (once for the whole run)
//  5. For each target in order:
//     - Allocate a (day, seq) via store
//     - Materialize into runs/<day>/<seq>-<target>/
//     - Record start in runs table
//     - Run the body via runner.RunCallable
//     - Update runs row with status, duration, exit code
//  6. On failure: stop, propagate the error with the target name
//
// Stage 2 flags:
//
//	--file/-f    path to a gmk.yml file (default: discovered by walking
//	             up from $PWD; error if no gmk.yml is found anywhere
//	             from $PWD to /)
//	--dry-run    show what would run without executing (same as `gmk dryrun X`)
//
// Why migrate `run` to the same path as `call`: target invocations and
// function invocations are both "execute a callable, record what
// happened." Keeping two parallel paths means two places to fix bugs,
// two layouts under .gmk-cache, two race profiles. One path keeps the
// system consistent and gives users a single mental model.
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

	cmd.Flags().StringVarP(&file, "file", "f", "", "Path to a gmk.yml file (default: discovered by walking up from $PWD)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would run without executing (same as `gmk dryrun`)")

	return cmd
}

// executeRun is the shared core of `gmk run` and `gmk dryrun`. The dryRun
// flag toggles between actual execution and printed-only mode.
func executeRun(out io.Writer, file, targetName string, dryRun bool) error {
	project, err := loadProject(file)
	if err != nil {
		return err
	}

	if _, ok := project.Targets[targetName]; !ok {
		return fmt.Errorf("target %q not found in %s", targetName, project.SourcePath)
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

	// Open the store once per top-level `gmk run`. Every target in the
	// dep chain gets its own (day, seq) from this same handle, and
	// every runs-row update goes through it.
	st, err := store.Open(project.Root)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	ctx := context.Background()
	day := materialize.Today()

	fmt.Fprintf(out, "gmk: running %s\n", formatList(order))

	for _, name := range order {
		if err := runOneTarget(ctx, out, project, project.Targets[name], st, day); err != nil {
			return err
		}
	}
	return nil
}

// runOneTarget materializes and executes a single target through the
// Stage 3b store-backed path. Errors wrap the target name for clean
// diagnostic surfaces.
func runOneTarget(ctx context.Context, out io.Writer, project *ir.Project, t *ir.Target, st *store.Store, day string) error {
	fmt.Fprintf(out, "gmk: target %s\n", t.Name)

	// Resolve env and cwd against the project's root scope before
	// materializing — Stage 1 did this in run.go, Stage 3b's
	// MaterializeOpts takes the resolved values via c.Env / c.Cwd on
	// the Callable struct.
	resolvedEnv, err := resolveTargetEnv(project, t)
	if err != nil {
		return fmt.Errorf("target %q: env resolution: %w", t.Name, err)
	}
	resolvedCwd := t.Cwd
	if resolvedCwd != "" {
		resolvedCwd, err = resolve.ResolveStringInScope(t.Cwd, project.RootScope)
		if err != nil {
			return fmt.Errorf("target %q: cwd resolution: %w", t.Name, err)
		}
	}

	// Resolve ${...} substitutions in the target body so Stage 1
	// behavior is preserved — body text gets project vars expanded.
	// This is the one place where target run differs from function
	// call: targets historically had body interpolation, functions
	// don't (they use $GMK_ARGS instead).
	//
	// Stage 3c: we pass a render dispatcher so target bodies that use
	// ${render:name(args)} expand the rendered output into the script
	// before materialization. Without this the render: would fail at
	// resolution time with "no render resolver configured".
	renderDisp := render.New(project.Templates, template.Default)
	resolvedRun, err := resolve.ResolveStringInScopeWithRender(t.Run, project.RootScope, renderDisp)
	if err != nil {
		return fmt.Errorf("target %q: body resolution: %w", t.Name, err)
	}

	c := materialize.CallableFromTarget(t)
	c.Run = resolvedRun
	c.Env = resolvedEnv
	c.Cwd = resolvedCwd

	inv, err := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
		ProjectRoot: project.Root,
		Store:       st,
		Day:         day,
		Languages:   project.Languages,
		SourceFile:  t.Source.File,
		// Args and PreludeValues empty: target invocation has no
		// caller-supplied args, and prelude (if any) is evaluated
		// when targets gain it in Stage 4. For now any Target.Prelude
		// is honored by future work; today it's silently empty.
	})
	if err != nil {
		return fmt.Errorf("target %q: materialize: %w", t.Name, err)
	}

	if err := st.RecordStart(ctx, store.StartParams{
		Day:          day,
		Seq:          inv.Seq,
		CallableName: t.Name,
		CallableKind: "target",
		StartedAt:    time.Now().UTC(),
		SourceFile:   t.Source.File,
	}); err != nil {
		return fmt.Errorf("target %q: record start: %w", t.Name, err)
	}

	res, runErr := runner.RunCallable(inv, runner.RunCallableOpts{
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		InheritEnv: true,
	})

	// Update the runs row regardless of outcome.
	finishStatus := "ok"
	exitCode := 0
	if runErr != nil {
		finishStatus = "error"
	} else if res != nil && res.ExitCode != 0 {
		finishStatus = "fail"
		exitCode = res.ExitCode
	}
	if res != nil {
		exitCode = res.ExitCode
	}
	_ = st.FinishRun(ctx, store.FinishParams{
		Day:        day,
		Seq:        inv.Seq,
		FinishedAt: time.Now().UTC(),
		Status:     finishStatus,
		ExitCode:   exitCode,
	})

	if runErr != nil {
		return fmt.Errorf("target %q failed: %w", t.Name, runErr)
	}
	if res != nil && res.ExitCode != 0 {
		return fmt.Errorf("target %q failed (exit %d)", t.Name, res.ExitCode)
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
