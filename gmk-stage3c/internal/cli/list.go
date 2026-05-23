package cli

// `gmk list` — show what's available in this project.
//
// Subgroups (controlled by flags so the default output is dense and
// chronological):
//
//   --targets    (default if no flag)  show declared targets
//   --functions                         show declared functions
//   --languages                         show language interpreters
//   --loggers                           show addressable logger names
//   --all                               show everything
//
// Output is plain text by default; --json emits a single JSON document
// describing the whole inventory (machine-readable, used by editors
// for completion).

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/suhrut/gmk/internal/ir"
)

func newListCmd() *cobra.Command {
	var (
		file           string
		showTargets    bool
		showFunctions  bool
		showLanguages  bool
		showLoggers    bool
		showAll        bool
		jsonOutput     bool
		verboseListing bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List targets, functions, and addressable logger names",
		Long: `Print an inventory of what this project defines:

  Targets:     declared under targets: in YAML.
  Functions:   declared under functions: in YAML.
  Languages:   user-defined language interpreters.
  Loggers:     hierarchical names you can use in --log-config files.

By default, lists targets and functions. Use --all to show everything,
or pick specific groups with --targets/--functions/--languages/--loggers.

With --json, output is a single JSON document — useful for editor
completion and tooling.

With --verbose, show docstrings (where present) and parameter signatures.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := loadProject(file)
			if err != nil {
				return err
			}

			if !showAll && !showTargets && !showFunctions && !showLanguages && !showLoggers {
				// Default: show targets and functions.
				showTargets = true
				showFunctions = true
			}
			if showAll {
				showTargets, showFunctions, showLanguages, showLoggers = true, true, true, true
			}

			if jsonOutput {
				return printListJSON(cmd.OutOrStdout(), p, showTargets, showFunctions, showLanguages, showLoggers)
			}
			return printListText(cmd.OutOrStdout(), p, showTargets, showFunctions, showLanguages, showLoggers, verboseListing)
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "path to a gmk.yml file (default: discovered by walking up from $PWD)")
	cmd.Flags().BoolVar(&showTargets, "targets", false, "list targets")
	cmd.Flags().BoolVar(&showFunctions, "functions", false, "list functions")
	cmd.Flags().BoolVar(&showLanguages, "languages", false, "list language interpreters")
	cmd.Flags().BoolVar(&showLoggers, "loggers", false, "list addressable logger names")
	cmd.Flags().BoolVar(&showAll, "all", false, "list everything")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	cmd.Flags().BoolVarP(&verboseListing, "verbose", "v", false, "include docstrings and param signatures")

	return cmd
}

// printListText writes a human-readable listing.
func printListText(w io.Writer, p *ir.Project, showT, showF, showL, showLog, verbose bool) error {
	if showT {
		fmt.Fprintln(w, "Targets:")
		names := make([]string, 0, len(p.Targets))
		for n := range p.Targets {
			names = append(names, n)
		}
		sort.Strings(names)
		if len(names) == 0 {
			fmt.Fprintln(w, "  (none)")
		}
		for _, n := range names {
			t := p.Targets[n]
			line := "  " + n
			if t.Lang != "" && t.Lang != "bash" {
				line += " [" + t.Lang + "]"
			}
			if t.Doc != "" {
				line += "  — " + firstLine(t.Doc)
			}
			fmt.Fprintln(w, line)
			if verbose && len(t.Prelude) > 0 {
				fmt.Fprintf(w, "      prelude: %d binding(s)\n", len(t.Prelude))
			}
		}
		fmt.Fprintln(w)
	}

	if showF {
		fmt.Fprintln(w, "Functions:")
		// Functions: declaration order, not alphabetical, since author intent matters.
		if len(p.FunctionOrder) == 0 {
			fmt.Fprintln(w, "  (none)")
		}
		for _, n := range p.FunctionOrder {
			fn := p.Functions[n]
			line := "  " + n
			if fn.Lang != "" && fn.Lang != "bash" {
				line += " [" + fn.Lang + "]"
			}
			line += "(" + paramSignature(fn.Params) + ")"
			if fn.Result != nil && fn.Result.Type != "" {
				line += " -> " + fn.Result.Type
			}
			if fn.Doc != "" {
				line += "  — " + firstLine(fn.Doc)
			}
			fmt.Fprintln(w, line)
			if verbose {
				for _, p := range fn.Params {
					def := ""
					if p.Default != nil {
						def = " (has default)"
					}
					fmt.Fprintf(w, "      %s: %s%s", p.Name, p.Type, def)
					if p.Doc != "" {
						fmt.Fprintf(w, " — %s", firstLine(p.Doc))
					}
					fmt.Fprintln(w)
				}
				if len(fn.Prelude) > 0 {
					fmt.Fprintf(w, "      prelude: %d binding(s)\n", len(fn.Prelude))
				}
			}
		}
		fmt.Fprintln(w)
	}

	if showL {
		fmt.Fprintln(w, "Languages (user-defined; built-ins always available):")
		if len(p.Languages) == 0 {
			fmt.Fprintln(w, "  (none — using built-ins: bash, sh, python, ruby, node, perl)")
		}
		names := make([]string, 0, len(p.Languages))
		for n := range p.Languages {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			l := p.Languages[n]
			fmt.Fprintf(w, "  %s -> %s", n, l.Interpreter)
			if len(l.Args) > 0 {
				fmt.Fprintf(w, " %v", l.Args)
			}
			if l.Ext != "" {
				fmt.Fprintf(w, " (ext %s)", l.Ext)
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w)
	}

	if showLog {
		fmt.Fprintln(w, "Logger names (for --log-config files):")
		fmt.Fprintln(w, "  Internal:")
		for _, n := range internalLoggerNames() {
			fmt.Fprintf(w, "    %s\n", n)
		}
		if len(p.Targets) > 0 {
			fmt.Fprintln(w, "  Targets:")
			tNames := make([]string, 0, len(p.Targets))
			for n := range p.Targets {
				tNames = append(tNames, n)
			}
			sort.Strings(tNames)
			for _, n := range tNames {
				fmt.Fprintf(w, "    gmk.target.%s\n", n)
			}
		}
		if len(p.Functions) > 0 {
			fmt.Fprintln(w, "  Functions:")
			for _, n := range p.FunctionOrder {
				fmt.Fprintf(w, "    gmk.fn.%s\n", n)
			}
		}
		fmt.Fprintln(w)
	}
	return nil
}

// printListJSON writes a single machine-readable JSON document.
func printListJSON(w io.Writer, p *ir.Project, showT, showF, showL, showLog bool) error {
	doc := map[string]any{}
	if showT {
		targets := make([]map[string]any, 0, len(p.Targets))
		names := make([]string, 0, len(p.Targets))
		for n := range p.Targets {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			t := p.Targets[n]
			targets = append(targets, map[string]any{
				"name":    t.Name,
				"lang":    t.Lang,
				"doc":     t.Doc,
				"deps":    t.Deps,
				"phony":   t.Phony,
				"prelude": preludeNames(t.Prelude),
			})
		}
		doc["targets"] = targets
	}
	if showF {
		fns := make([]map[string]any, 0, len(p.Functions))
		for _, n := range p.FunctionOrder {
			fn := p.Functions[n]
			params := make([]map[string]any, len(fn.Params))
			for i, p := range fn.Params {
				params[i] = map[string]any{
					"name":     p.Name,
					"type":     p.Type,
					"required": p.Default == nil,
					"doc":      p.Doc,
				}
			}
			entry := map[string]any{
				"name":    fn.Name,
				"lang":    fn.Lang,
				"doc":     fn.Doc,
				"params":  params,
				"prelude": preludeNames(fn.Prelude),
			}
			if fn.Result != nil {
				entry["result"] = map[string]any{
					"type": fn.Result.Type,
					"doc":  fn.Result.Doc,
				}
			}
			fns = append(fns, entry)
		}
		doc["functions"] = fns
	}
	if showL {
		langs := make([]map[string]any, 0, len(p.Languages))
		names := make([]string, 0, len(p.Languages))
		for n := range p.Languages {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			l := p.Languages[n]
			langs = append(langs, map[string]any{
				"name":        l.Name,
				"interpreter": l.Interpreter,
				"args":        l.Args,
				"ext":         l.Ext,
			})
		}
		doc["languages"] = langs
	}
	if showLog {
		loggers := []string{}
		loggers = append(loggers, internalLoggerNames()...)
		for n := range p.Targets {
			loggers = append(loggers, "gmk.target."+n)
		}
		for _, n := range p.FunctionOrder {
			loggers = append(loggers, "gmk.fn."+n)
		}
		sort.Strings(loggers)
		doc["loggers"] = loggers
	}
	enc := jsonEncode
	return enc(w, doc)
}

// paramSignature produces a "host: string, port: int=8080" form for
// the listing. Defaults are marked with "=" + a placeholder; the actual
// value is omitted (it's an expression and may include refs).
func paramSignature(ps []ir.FunctionParam) string {
	if len(ps) == 0 {
		return ""
	}
	parts := make([]string, len(ps))
	for i, p := range ps {
		s := p.Name + ": " + p.Type
		if p.Default != nil {
			s += "=…"
		}
		parts[i] = s
	}
	return strings.Join(parts, ", ")
}

// preludeNames extracts just the names from prelude entries (for the
// JSON listing's compact form; full bodies would be noisy).
func preludeNames(p []ir.PreludeEntry) []string {
	out := make([]string, len(p))
	for i, e := range p {
		out[i] = e.Name
	}
	return out
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		if t != "" {
			return t
		}
	}
	return ""
}

// internalLoggerNames returns the static list of internal logger names
// that gmk currently emits from. Kept in code (rather than discovered
// at runtime by scanning) so it's deterministic and so `gmk list
// --loggers` works even before any code path has fired.
//
// Add a new package's logger name here when you introduce one.
func internalLoggerNames() []string {
	return []string{
		"gmk.expr.parser",
		"gmk.expr.eval",
		"gmk.load",
		"gmk.dag",
		"gmk.materialize",
		"gmk.runner.callable",
		"gmk.funcs.dispatch",
		"gmk.cache",
	}
}
