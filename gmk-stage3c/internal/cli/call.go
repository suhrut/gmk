package cli

// `gmk call <function> [k=v...]` — invoke a function and print its result.
//
// This is the user-facing entry point for the Stage 3b callable machinery.
// The flow:
//
//   1. Load the project from --file (default: discovered by walking up from $PWD).
//   2. Validate the named callable exists in p.Functions (or, with --target,
//      look in p.Targets too).
//   3. Parse args from either --json (whole JSON document), --json-in
//      (file path), stdin (if --stdin), or KEY=VAL CLI args.
//   4. Bind args to the function's params and evaluate any prelude bindings.
//   5. Materialize the callable into the per-run scratch dir.
//   6. Execute via runner.RunCallable.
//   7. Print result.json to stdout (or via --output mode).
//
// Result printing modes:
//   --json (default): print the parsed result as compact JSON to stdout
//   --raw: print bare value (no quotes for strings, no braces for null)
//   --pretty: print indented JSON

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/funcs"
	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/materialize"
	"github.com/suhrut/gmk/internal/resolve"
	"github.com/suhrut/gmk/internal/runner"
	"github.com/suhrut/gmk/internal/store"
)

func newCallCmd() *cobra.Command {
	var (
		file       string
		jsonIn     string
		jsonInFile string
		fromStdin  bool
		outputMode string
		allowTgt   bool
	)

	cmd := &cobra.Command{
		Use:   "call <function> [KEY=VALUE...]",
		Short: "Invoke a function and print its result",
		Long: `Invoke a function declared under functions: in the project YAML.

Arguments may be supplied in three ways (mutually exclusive):

  KEY=VALUE pairs:    gmk call greet name=Sam city="Mumbai"
  --json '{...}':     gmk call greet --json '{"name":"Sam"}'
  --json-in PATH:     gmk call greet --json-in args.json
  --stdin:            echo '{"name":"Sam"}' | gmk call greet --stdin

Output modes:
  --output json    (default): compact JSON on a single line
  --output pretty: indented JSON
  --output raw:    bare value for scalars (no JSON encoding)`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			kvArgs := args[1:]

			// Sanity-check argument-supply modes are mutually exclusive.
			modes := 0
			if jsonIn != "" {
				modes++
			}
			if jsonInFile != "" {
				modes++
			}
			if fromStdin {
				modes++
			}
			if len(kvArgs) > 0 {
				modes++
			}
			if modes > 1 {
				return errors.New("specify args via exactly one of: KEY=VALUE pairs, " +
					"--json, --json-in, or --stdin (not multiple)")
			}

			p, err := loadProject(file)
			if err != nil {
				return err
			}

			fn, isFunction := p.Functions[name]
			var tgt *ir.Target
			if !isFunction {
				if allowTgt {
					tgt = p.Targets[name]
				}
				if tgt == nil {
					return fmt.Errorf("no function %q in %s "+
						"(declare under functions:; or pass --target to allow targets)",
						name, p.SourcePath)
				}
			}

			// Parse the input args into a map[string]Value.
			argValues, err := parseCallArgs(kvArgs, jsonIn, jsonInFile, fromStdin, cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("parse args: %w", err)
			}

			res, err := runCallOnce(p, fn, tgt, argValues, cmd.OutOrStderr())
			if err != nil {
				return err
			}

			// Print the result. We separate the result from the body's
			// own stdout (which went to cmd.OutOrStderr() above) so the
			// caller can pipe `gmk call` output to jq cleanly.
			return printResult(cmd.OutOrStdout(), res.Value, outputMode)
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "path to a gmk.yml file (default: discovered by walking up from $PWD)")
	cmd.Flags().StringVar(&jsonIn, "json", "", "JSON object literal as args")
	cmd.Flags().StringVar(&jsonInFile, "json-in", "", "path to JSON file with args")
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "read JSON args from stdin")
	cmd.Flags().StringVar(&outputMode, "output", "json", "output mode: json|pretty|raw")
	cmd.Flags().BoolVar(&allowTgt, "target", false, "allow invoking a target as a callable")

	return cmd
}

// parseCallArgs assembles the argument map from whichever input
// channel was specified. Exactly one is expected to be non-empty; the
// caller has already enforced that.
func parseCallArgs(kvArgs []string, jsonLiteral, jsonFile string, fromStdin bool, stdin io.Reader) (map[string]expr.Value, error) {
	switch {
	case jsonLiteral != "":
		return parseJSONArgs([]byte(jsonLiteral))
	case jsonFile != "":
		data, err := os.ReadFile(jsonFile)
		if err != nil {
			return nil, err
		}
		return parseJSONArgs(data)
	case fromStdin:
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, err
		}
		return parseJSONArgs(data)
	default:
		return parseKVArgs(kvArgs)
	}
}

// parseJSONArgs parses a JSON object into a Value map.
// {"x": 1, "y": "hi"} -> {"x": IntValue(1), "y": StringValue("hi")}.
// Non-object JSON is an error: args must be a map.
func parseJSONArgs(data []byte) (map[string]expr.Value, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return map[string]expr.Value{}, nil
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("args JSON must be an object (got " +
			fmt.Sprintf("%T", raw) + ")")
	}
	out := make(map[string]expr.Value, len(obj))
	for k, v := range obj {
		val, err := expr.FromJSON(v)
		if err != nil {
			return nil, fmt.Errorf("arg %q: %w", k, err)
		}
		out[k] = val
	}
	return out, nil
}

// parseKVArgs parses KEY=VALUE CLI args.
//
// Heuristics for value parsing (matches what shells do):
//   "true"/"false" → bool
//   integer-looking → int
//   float-looking → float (only if "." in value)
//   value starting with [ or { → JSON parse attempt; on failure, treat as string
//   everything else → string
//
// This is intentionally permissive — the user can always force a type by
// using --json instead.
func parseKVArgs(args []string) (map[string]expr.Value, error) {
	out := make(map[string]expr.Value, len(args))
	for _, a := range args {
		eq := strings.Index(a, "=")
		if eq <= 0 || eq == len(a)-1 && eq != 0 {
			// allow "key=" as the empty-string value, but not bare "key" without "="
			if eq < 0 {
				return nil, fmt.Errorf("arg %q must be of form KEY=VALUE", a)
			}
		}
		key, raw := a[:eq], a[eq+1:]
		out[key] = coerceArg(raw)
	}
	return out, nil
}

// coerceArg applies the heuristics described above to one raw value.
func coerceArg(raw string) expr.Value {
	if raw == "true" {
		return expr.NewBool(true)
	}
	if raw == "false" {
		return expr.NewBool(false)
	}
	if raw == "null" {
		return expr.NewNone()
	}
	// Try int.
	if n, err := parseInt64(raw); err == nil {
		return expr.NewInt(n)
	}
	// Try float (only if "." is present, to avoid ambiguity with strings
	// that happen to be all-digits with a leading zero, etc.).
	if strings.Contains(raw, ".") {
		if f, err := parseFloat64(raw); err == nil {
			return expr.NewFloat(f)
		}
	}
	// JSON-looking? Try a strict parse.
	if len(raw) > 0 && (raw[0] == '[' || raw[0] == '{') {
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber()
		var parsed any
		if err := dec.Decode(&parsed); err == nil {
			if v, err := expr.FromJSON(parsed); err == nil {
				return v
			}
		}
	}
	return expr.NewString(raw)
}

// runCallOnce wires together resolve + funcs.Dispatcher + materialize +
// runner to execute the callable once. The bodyOut writer is where the
// body's stdout/stderr go (typically os.Stderr so it doesn't mix with
// the gmk-call result printed to stdout).
//
// Each call (top-level and nested via ${call:...}) gets:
//   - a unique seq allocated via the SQLite store
//   - a 'running' row inserted at start
//   - a final UPDATE with status + duration + result at finish
//
// The store is opened once for the whole top-level invocation and
// passed to materialize/runner via the closure.
func runCallOnce(p *ir.Project, fn *ir.Function, tgt *ir.Target, args map[string]expr.Value, bodyOut io.Writer) (*runner.CallableResult, error) {
	ctx := context.Background()
	day := materialize.Today()

	st, err := store.Open(p.Root)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// Build a Runner closure that the dispatcher calls when expression
	// evaluation hits a ${call:...}. This is the same path the top-level
	// call uses, so nested calls work transparently.
	var dispatch *funcs.Dispatcher
	doRun := func(targetFn *ir.Function, boundArgs map[string]expr.Value) (expr.Value, error) {
		// Evaluate the function's prelude here using the dispatcher
		// itself (so prelude can reference other call:...).
		preludeValues, perr := evaluatePrelude(p, targetFn.Prelude, boundArgs, dispatch)
		if perr != nil {
			return expr.NewNone(), perr
		}
		// Pure-data function: prelude but no body. The function's
		// result is the prelude itself (as a map). This makes the
		// pattern of "function = computed map of values" first-class
		// without forcing the user to write `cat "$GMK_PRELUDE" > "$GMK_RESULT"`.
		if targetFn.Run == "" {
			return expr.NewMap(preludeValues), nil
		}
		c := materialize.CallableFromFunction(targetFn)
		inv, merr := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
			ProjectRoot:   p.Root,
			Store:         st,
			Day:           day,
			Args:          boundArgs,
			PreludeValues: preludeValues,
			Languages:     p.Languages,
			SourceFile:    targetFn.Source.File,
		})
		if merr != nil {
			return expr.NewNone(), merr
		}
		// Record start now that we have a seq.
		startTime := time.Now().UTC()
		argsJSON := jsonOrEmpty(boundArgs)
		preludeJSON := jsonOrEmpty(preludeValues)
		if err := st.RecordStart(ctx, store.StartParams{
			Day:          day,
			Seq:          inv.Seq,
			CallableName: targetFn.Name,
			CallableKind: "function",
			StartedAt:    startTime,
			ArgsJSON:     argsJSON,
			PreludeJSON:  preludeJSON,
			SourceFile:   targetFn.Source.File,
		}); err != nil {
			return expr.NewNone(), fmt.Errorf("store: record start: %w", err)
		}
		res, rerr := runner.RunCallable(inv, runner.RunCallableOpts{
			Stdout:     bodyOut,
			Stderr:     bodyOut,
			InheritEnv: true,
		})
		// Finalize the row regardless of how it ended.
		finishStatus := "ok"
		if rerr != nil {
			finishStatus = "error"
		} else if res.ExitCode != 0 {
			finishStatus = "fail"
		}
		var resultJSON string
		var exitCode int
		if res != nil {
			exitCode = res.ExitCode
			if res.Value.Kind != expr.NoneKind {
				if b, err := jsonMarshalValue(res.Value); err == nil {
					resultJSON = b
				}
			}
		}
		_ = st.FinishRun(ctx, store.FinishParams{
			Day:        day,
			Seq:        inv.Seq,
			FinishedAt: time.Now().UTC(),
			Status:     finishStatus,
			ExitCode:   exitCode,
			ResultJSON: resultJSON,
		})
		if rerr != nil {
			return expr.NewNone(), rerr
		}
		if res.ExitCode != 0 {
			return expr.NewNone(), fmt.Errorf("body exited with code %d", res.ExitCode)
		}
		return res.Value, nil
	}
	dispatch = funcs.New(p.Functions, doRun)

	// If it's a function, dispatch through the same path.
	if fn != nil {
		// Apply user args directly; dispatcher does binding/coercion.
		v, err := dispatch.ResolveCall(fn.Name, args)
		if err != nil {
			return nil, err
		}
		return &runner.CallableResult{Value: v, ExitCode: 0}, nil
	}

	// Otherwise it's a target invoked via --target. Treat it like a
	// function with no parameters — args are still passed through.
	preludeValues, perr := evaluatePrelude(p, tgt.Prelude, args, dispatch)
	if perr != nil {
		return nil, perr
	}
	c := materialize.CallableFromTarget(tgt)
	inv, err := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
		ProjectRoot:   p.Root,
		Store:         st,
		Day:           day,
		Args:          args,
		PreludeValues: preludeValues,
		Languages:     p.Languages,
		SourceFile:    tgt.Source.File,
	})
	if err != nil {
		return nil, err
	}
	startTime := time.Now().UTC()
	if err := st.RecordStart(ctx, store.StartParams{
		Day:          day,
		Seq:          inv.Seq,
		CallableName: tgt.Name,
		CallableKind: "target",
		StartedAt:    startTime,
		ArgsJSON:     jsonOrEmpty(args),
		PreludeJSON:  jsonOrEmpty(preludeValues),
		SourceFile:   tgt.Source.File,
	}); err != nil {
		return nil, err
	}
	res, rerr := runner.RunCallable(inv, runner.RunCallableOpts{
		Stdout:     bodyOut,
		Stderr:     bodyOut,
		InheritEnv: true,
	})
	finishStatus := "ok"
	if rerr != nil {
		finishStatus = "error"
	} else if res != nil && res.ExitCode != 0 {
		finishStatus = "fail"
	}
	var resultJSON string
	if res != nil && res.Value.Kind != expr.NoneKind {
		if b, jerr := jsonMarshalValue(res.Value); jerr == nil {
			resultJSON = b
		}
	}
	exitCode := 0
	if res != nil {
		exitCode = res.ExitCode
	}
	_ = st.FinishRun(ctx, store.FinishParams{
		Day:        day,
		Seq:        inv.Seq,
		FinishedAt: time.Now().UTC(),
		Status:     finishStatus,
		ExitCode:   exitCode,
		ResultJSON: resultJSON,
	})
	return res, rerr
}

// jsonOrEmpty marshals a Value map to JSON. Returns "" on error or
// empty map so the column ends up as SQL NULL.
func jsonOrEmpty(m map[string]expr.Value) string {
	if len(m) == 0 {
		return ""
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v.ToJSON()
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

// jsonMarshalValue serializes a single Value to JSON for the
// result_json column.
func jsonMarshalValue(v expr.Value) (string, error) {
	b, err := json.Marshal(v.ToJSON())
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// evaluatePrelude evaluates each prelude entry in declaration order
// against the project's vars plus the caller-supplied args. Later
// entries can reference earlier ones via ${name}, since we feed
// each evaluated binding into a local var scope before the next
// entry's evaluation.
func evaluatePrelude(p *ir.Project, prelude []ir.PreludeEntry, args map[string]expr.Value, calls expr.CallResolver) (map[string]expr.Value, error) {
	if len(prelude) == 0 {
		return map[string]expr.Value{}, nil
	}

	// Build a layered resolver:
	//   - args first
	//   - already-evaluated prelude bindings
	//   - project vars (fallback)
	layered := &preludeScope{
		args:    args,
		bound:   make(map[string]expr.Value, len(prelude)),
		project: p,
	}

	out := make(map[string]expr.Value, len(prelude))
	for _, entry := range prelude {
		ev := &expr.Evaluator{
			Vars:        layered,
			EnvProvider: osEnvProvider,
			Funcs:       expr.DefaultFuncs(),
			Calls:       calls,
		}
		v, err := ev.Eval(entry.Expr)
		if err != nil {
			return nil, fmt.Errorf("prelude binding %s: %w", entry.Name, err)
		}
		layered.bound[entry.Name] = v
		out[entry.Name] = v
	}
	return out, nil
}

// preludeScope implements expr.VarResolver. It looks up names in the
// caller's args first, then the already-bound prelude entries, then
// falls through to the project's root scope vars (resolved as strings).
type preludeScope struct {
	args    map[string]expr.Value
	bound   map[string]expr.Value
	project *ir.Project
}

func (s *preludeScope) ResolveVar(name string) (expr.Value, error) {
	if v, ok := s.args[name]; ok {
		return v, nil
	}
	if v, ok := s.bound[name]; ok {
		return v, nil
	}
	// Fall through to project vars via resolve (Stage 1 path).
	if s.project != nil {
		if _, ok := s.project.Vars[name]; ok {
			str, err := resolve.ResolveString("${"+name+"}", s.project)
			if err == nil {
				return expr.NewString(str), nil
			}
		}
	}
	return expr.NewNone(), expr.ErrUndefinedVar
}

func osEnvProvider(name string) (string, bool) {
	v, ok := os.LookupEnv(name)
	return v, ok
}

// printResult writes the result Value to w in the requested mode.
func printResult(w io.Writer, v expr.Value, mode string) error {
	switch mode {
	case "raw":
		switch v.Kind {
		case expr.NoneKind:
			return nil // nothing on raw output for null
		case expr.StringKind:
			_, err := fmt.Fprintln(w, v.Str)
			return err
		default:
			_, err := fmt.Fprintln(w, v.AsString())
			return err
		}
	case "pretty":
		b, err := json.MarshalIndent(v.ToJSON(), "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	case "json", "":
		b, err := json.Marshal(v.ToJSON())
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}
	return fmt.Errorf("unknown output mode %q (want: json, pretty, raw)", mode)
}

// parseInt64 wraps strconv.ParseInt to keep the import local.
func parseInt64(s string) (int64, error) {
	if len(s) == 0 {
		return 0, errors.New("empty")
	}
	// Strip leading + for uniformity with strconv.
	return parseInt64Std(s)
}
