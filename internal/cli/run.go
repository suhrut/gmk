package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/suhrut/gmk/internal/dag"
	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/funcs"
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

// RunOneTarget materializes and executes a single target through the
// Stage 3b store-backed path. Errors wrap the target name for clean
// diagnostic surfaces.
//
// Exported in Stage 3c.1.2 so the integration test driver
// (internal/integration) can exercise the actual production target-
// run code path including prelude evaluation. Before the export, the
// integration test maintained a parallel "this is the contract"
// reimplementation that drifted out of sync with the real cli
// (notably: it didn't evaluate target preludes, so prelude-using
// examples failed in test even though they worked at the command
// line). One canonical implementation, tested everywhere.
func RunOneTarget(ctx context.Context, out io.Writer, project *ir.Project, t *ir.Target, st *store.Store, day string) error {
	return runOneTarget(ctx, out, project, t, st, day)
}

// runOneTarget is the package-private impl. RunOneTarget is the
// exported wrapper kept thin so the cli package can still call the
// short name internally.
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

	// Stage 3c: evaluate the target's prelude (if any). Target preludes
	// were previously deferred to "Stage 4"; this turn wires them in
	// because the templates examples (which use ${call:fn()} in target
	// preludes to assemble Map data for ${render:tmpl(...mapvar)})
	// have no other natural place to live. The machinery mirrors
	// call.go: build a call dispatcher whose Runner recursively
	// executes functions (so nested ${call:...} works), build a render
	// dispatcher, evaluate prelude entries in declaration order.
	renderDisp := render.New(project.Templates, template.Default)

	// Stage 3c.2: always build the dispatch + doRun closure, not only
	// when there's a prelude. Target bodies can use ${call:fn(...)},
	// ${map:fn(items=L)}, and ${filter:fn(items=L)} which all need a
	// CallResolver at body-resolution time. Building eagerly is cheap;
	// the closure is just a value until something calls it.
	var dispatch *funcs.Dispatcher
	doRun := func(targetFn *ir.Function, boundArgs map[string]expr.Value) (expr.Value, error) {
		fnPrelude, perr := evaluatePrelude(project, targetFn.Prelude, boundArgs, dispatch, renderDisp)
		if perr != nil {
			return expr.NewNone(), perr
		}
		// Pure-data function: prelude but no body — result is the prelude map.
		if targetFn.Run == "" {
			return expr.NewMap(fnPrelude), nil
		}
		c := materialize.CallableFromFunction(targetFn)
		inv, merr := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
			ProjectRoot:   project.Root,
			Store:         st,
			Day:           day,
			Args:          boundArgs,
			PreludeValues: fnPrelude,
			Languages:     project.Languages,
			SourceFile:    targetFn.Source.File,
		})
		if merr != nil {
			return expr.NewNone(), merr
		}
		startTime := time.Now().UTC()
		if rerr := st.RecordStart(ctx, store.StartParams{
			Day:          day,
			Seq:          inv.Seq,
			CallableName: targetFn.Name,
			CallableKind: "function",
			StartedAt:    startTime,
			ArgsJSON:     jsonOrEmpty(boundArgs),
			PreludeJSON:  jsonOrEmpty(fnPrelude),
			SourceFile:   targetFn.Source.File,
		}); rerr != nil {
			return expr.NewNone(), rerr
		}
		res, rerr := runner.RunCallable(inv, runner.RunCallableOpts{
			Stdout:     os.Stderr,
			Stderr:     os.Stderr,
			InheritEnv: true,
		})
		finishStatus := "ok"
		exitCode := 0
		if rerr != nil {
			finishStatus = "error"
		} else if res != nil && res.ExitCode != 0 {
			finishStatus = "error"
			exitCode = res.ExitCode
		}
		endTime := time.Now().UTC()
		var resultJSON string
		if res != nil {
			if s, jerr := jsonMarshalValue(res.Value); jerr == nil {
				resultJSON = s
			}
		}
		_ = st.FinishRun(ctx, store.FinishParams{
			Day:        day,
			Seq:        inv.Seq,
			FinishedAt: endTime,
			Status:     finishStatus,
			ExitCode:   exitCode,
			ResultJSON: resultJSON,
		})
		if rerr != nil {
			return expr.NewNone(), rerr
		}
		if res != nil && res.ExitCode != 0 {
			return expr.NewNone(), fmt.Errorf("function %q body exited with code %d", targetFn.Name, res.ExitCode)
		}
		if res != nil {
			return res.Value, nil
		}
		return expr.NewNone(), nil
	}
	dispatch = funcs.New(project.Functions, doRun)

	var preludeValues map[string]expr.Value
	if len(t.Prelude) > 0 {
		var perr error
		preludeValues, perr = evaluatePrelude(project, t.Prelude, nil, dispatch, renderDisp)
		if perr != nil {
			return fmt.Errorf("target %q: prelude: %w", t.Name, perr)
		}
	}

	// Resolve ${...} substitutions in the target body. Stage 3c layers
	// prelude bindings on top of the project root scope so the body
	// can reference prelude vars by name (the common pattern: target
	// prelude assembles a Map, body splats it into render).
	var bodyVars expr.VarResolver
	if len(preludeValues) > 0 {
		bodyVars = &preludeScope{
			args:    nil,
			bound:   preludeValues,
			project: project,
		}
	}
	resolvedRun, err := resolve.ResolveStringFull(t.Run, project.RootScope, bodyVars, dispatch, renderDisp)
	if err != nil {
		return fmt.Errorf("target %q: body resolution: %w", t.Name, err)
	}

	c := materialize.CallableFromTarget(t)
	c.Run = resolvedRun
	c.Env = resolvedEnv
	c.Cwd = resolvedCwd

	inv, err := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
		ProjectRoot:   project.Root,
		Store:         st,
		Day:           day,
		Languages:     project.Languages,
		SourceFile:    t.Source.File,
		PreludeValues: preludeValues, // Stage 3c: now wired through
		// Args still empty: targets don't take caller-supplied args
		// today; that's a Stage 4 question (`gmk run target --arg k=v`).
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
