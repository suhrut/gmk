package cli

// `gmk doc <name>` — show the full documentation page for a single
// callable. Pairs with `gmk list` (which gives the inventory).
//
// The doc page includes:
//
//   Header line with the kind, name, and language.
//   The user's doc string (from doc:).
//   Parameter list with name, type, default-presence, and per-param docs.
//   Result type and doc.
//   Prelude bindings (names + source expression).
//   The body, with line numbers for cross-referencing into source.
//
// With --shell-helpers, prints the embedded lib.sh contents instead.
// Useful for users who want to read what p_get/a_get/r_set actually do
// without poking at .gmk-cache.

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/load"
	"github.com/suhrut/gmk/internal/materialize"
)

func newDocCmd() *cobra.Command {
	var (
		file         string
		showHelpers  bool
		jsonOutput   bool
	)

	cmd := &cobra.Command{
		Use:   "doc [<name>]",
		Short: "Show documentation for a function, target, or the bash helper library",
		Long: `Show the doc page for one callable (function or target).

With --shell-helpers, print the lib.sh contents that bodies auto-source
(useful for understanding what p_get / a_get / r_set / log_info do).

With --json, emit the doc as a structured JSON document — useful for
editor LSPs.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if showHelpers {
				_, err := fmt.Fprint(cmd.OutOrStdout(), materialize.LibShContents())
				return err
			}
			if len(args) == 0 {
				return fmt.Errorf("provide a name (e.g. gmk doc greet), " +
					"or use --shell-helpers for the bash lib")
			}
			name := args[0]
			p, err := load.Load(file)
			if err != nil {
				return err
			}
			if fn, ok := p.Functions[name]; ok {
				if jsonOutput {
					return docFunctionJSON(cmd.OutOrStdout(), fn)
				}
				return docFunctionText(cmd.OutOrStdout(), fn)
			}
			if t, ok := p.Targets[name]; ok {
				if jsonOutput {
					return docTargetJSON(cmd.OutOrStdout(), t)
				}
				return docTargetText(cmd.OutOrStdout(), t)
			}
			return fmt.Errorf("no function or target named %q in %s", name, file)
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "build.yml", "path to build YAML")
	cmd.Flags().BoolVar(&showHelpers, "shell-helpers", false,
		"print the bash helper library (lib.sh) and exit")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON")
	return cmd
}

func docFunctionText(w io.Writer, fn *ir.Function) error {
	fmt.Fprintf(w, "function %s", fn.Name)
	if fn.Lang != "" && fn.Lang != "bash" {
		fmt.Fprintf(w, "  [%s]", fn.Lang)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  declared at %s\n", fn.Source)
	fmt.Fprintln(w)

	if fn.Doc != "" {
		fmt.Fprintln(w, indent(fn.Doc, "  "))
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "Parameters:")
	if len(fn.Params) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, p := range fn.Params {
		req := "required"
		if p.Default != nil {
			req = "optional"
		}
		fmt.Fprintf(w, "  %s: %s (%s)", p.Name, p.Type, req)
		if p.Doc != "" {
			fmt.Fprintf(w, "  — %s", firstLine(p.Doc))
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Result:")
	if fn.Result == nil {
		fmt.Fprintln(w, "  (undeclared — body decides)")
	} else {
		fmt.Fprintf(w, "  type: %s\n", fn.Result.Type)
		if fn.Result.Doc != "" {
			fmt.Fprintf(w, "  doc:  %s\n", fn.Result.Doc)
		}
	}
	fmt.Fprintln(w)

	if len(fn.Prelude) > 0 {
		fmt.Fprintln(w, "Prelude (gmk-time bindings):")
		for _, e := range fn.Prelude {
			fmt.Fprintf(w, "  %s = %s\n", e.Name, e.Expr.String())
		}
		fmt.Fprintln(w)
	}

	if fn.Run != "" {
		fmt.Fprintln(w, "Body:")
		for i, line := range strings.Split(fn.Run, "\n") {
			fmt.Fprintf(w, "  %3d  %s\n", i+1, line)
		}
	}
	return nil
}

func docFunctionJSON(w io.Writer, fn *ir.Function) error {
	doc := map[string]any{
		"kind":   "function",
		"name":   fn.Name,
		"lang":   fn.Lang,
		"doc":    fn.Doc,
		"source": fn.Source.String(),
		"body":   fn.Run,
	}
	params := make([]map[string]any, len(fn.Params))
	for i, p := range fn.Params {
		params[i] = map[string]any{
			"name":     p.Name,
			"type":     p.Type,
			"doc":      p.Doc,
			"required": p.Default == nil,
		}
	}
	doc["params"] = params
	if fn.Result != nil {
		doc["result"] = map[string]any{"type": fn.Result.Type, "doc": fn.Result.Doc}
	}
	prelude := make([]map[string]any, len(fn.Prelude))
	for i, e := range fn.Prelude {
		prelude[i] = map[string]any{"name": e.Name, "expr": e.Expr.String()}
	}
	doc["prelude"] = prelude
	return jsonEncode(w, doc)
}

func docTargetText(w io.Writer, t *ir.Target) error {
	fmt.Fprintf(w, "target %s", t.Name)
	if t.Lang != "" && t.Lang != "bash" {
		fmt.Fprintf(w, "  [%s]", t.Lang)
	}
	if t.Phony {
		fmt.Fprintf(w, "  (phony)")
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  declared at %s\n", t.Source)
	fmt.Fprintln(w)

	if t.Doc != "" {
		fmt.Fprintln(w, indent(t.Doc, "  "))
		fmt.Fprintln(w)
	}

	if len(t.Deps) > 0 {
		fmt.Fprintln(w, "Dependencies:")
		for _, d := range t.Deps {
			fmt.Fprintf(w, "  - %s\n", d)
		}
		fmt.Fprintln(w)
	}

	if len(t.Env) > 0 {
		fmt.Fprintln(w, "Env:")
		for k, v := range t.Env {
			fmt.Fprintf(w, "  %s = %s\n", k, v)
		}
		fmt.Fprintln(w)
	}

	if t.Cwd != "" {
		fmt.Fprintf(w, "Working dir: %s\n\n", t.Cwd)
	}

	if len(t.Prelude) > 0 {
		fmt.Fprintln(w, "Prelude (gmk-time bindings):")
		for _, e := range t.Prelude {
			fmt.Fprintf(w, "  %s = %s\n", e.Name, e.Expr.String())
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "Body:")
	for i, line := range strings.Split(t.Run, "\n") {
		fmt.Fprintf(w, "  %3d  %s\n", i+1, line)
	}
	return nil
}

func docTargetJSON(w io.Writer, t *ir.Target) error {
	doc := map[string]any{
		"kind":   "target",
		"name":   t.Name,
		"lang":   t.Lang,
		"phony":  t.Phony,
		"doc":    t.Doc,
		"source": t.Source.String(),
		"deps":   t.Deps,
		"env":    t.Env,
		"cwd":    t.Cwd,
		"body":   t.Run,
	}
	prelude := make([]map[string]any, len(t.Prelude))
	for i, e := range t.Prelude {
		prelude[i] = map[string]any{"name": e.Name, "expr": e.Expr.String()}
	}
	doc["prelude"] = prelude
	return jsonEncode(w, doc)
}

// indent prepends every non-empty line of s with prefix.
func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}
