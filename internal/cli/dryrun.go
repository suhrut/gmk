package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/suhrut/gmk/internal/cache"
	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/resolve"
)

// newDryRunCmd returns the `gmk dryrun <target>` command.
//
// Equivalent to `gmk run <target> --dry-run`, but lives as its own
// subcommand because it's a frequently-used debugging operation and a
// dedicated command surfaces it in --help discovery.
//
// Output shows: dep order, resolved env, materialized script path, cwd —
// everything needed to reproduce what `gmk run` would do, without
// touching any subprocess.
func newDryRunCmd() *cobra.Command {
	var file string

	cmd := &cobra.Command{
		Use:   "dryrun <target>",
		Short: "Show what would run for a target without executing it",
		Long: `dryrun prints the resolved execution plan for a target:
  - the dep order that would be scheduled
  - per-target: script path, working directory, resolved env

It does not materialize scripts to disk (nothing is written, nothing runs).
Equivalent to: gmk run <target> --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return executeRun(cmd.OutOrStdout(), file, args[0], true)
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "build.yml", "Path to the gmk YAML file")

	return cmd
}

// printDryRunPlan prints the would-execute plan for a project + ordered
// target list. Stage 2 keeps the format simple and parsable; Stage 8 will
// extend with the scope provenance that `gmk explain` shows.
func printDryRunPlan(out io.Writer, project *ir.Project, order []string) error {
	fmt.Fprintf(out, "gmk: resolved dep order for %s:\n", order[len(order)-1])
	for i, name := range order {
		fmt.Fprintf(out, "  %d. %s\n", i+1, name)
	}
	fmt.Fprintln(out)

	for _, name := range order {
		t := project.Targets[name]
		if err := printOneDryRunTarget(out, project, t); err != nil {
			return err
		}
		fmt.Fprintln(out)
	}

	fmt.Fprintln(out, "gmk: nothing executed (--dry-run)")
	return nil
}

func printOneDryRunTarget(out io.Writer, project *ir.Project, t *ir.Target) error {
	fmt.Fprintf(out, "gmk: target %q\n", t.Name)

	scriptPath := cache.ScriptPath(project.Root, project.SourcePath, t.Name, t.Lang)
	fmt.Fprintf(out, "     script: %s\n", scriptPath)

	cwd := t.Cwd
	if cwd == "" {
		cwd = project.Root + "  (default)"
	}
	fmt.Fprintf(out, "     cwd:    %s\n", cwd)

	if len(t.Env) == 0 {
		fmt.Fprintf(out, "     env:    (inherited only)\n")
	} else {
		fmt.Fprintf(out, "     env:\n")
		// Sort env keys for stable output.
		keys := make([]string, 0, len(t.Env))
		for k := range t.Env {
			keys = append(keys, k)
		}
		stringSliceSort(keys)
		for _, k := range keys {
			resolved, err := resolve.ResolveStringInScope(t.Env[k], project.RootScope)
			if err != nil {
				return fmt.Errorf("dryrun: target %q env[%q]: %w", t.Name, k, err)
			}
			fmt.Fprintf(out, "       %s=%s\n", k, resolved)
		}
	}

	interp := cache.InterpreterForLang(t.Lang)
	fmt.Fprintf(out, "     would run: %s %s\n", interp, scriptPath)
	return nil
}

// formatList renders a slice of names as "[a, b, c]" for log lines.
func formatList(names []string) string {
	if len(names) == 0 {
		return "[]"
	}
	return "[" + strings.Join(names, ", ") + "]"
}

// stringSliceSort is a tiny insertion sort to avoid pulling "sort" into
// this file just for stable env-key output. (sort is already imported
// elsewhere; this keeps dryrun.go's import surface minimal for clarity.)
func stringSliceSort(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
