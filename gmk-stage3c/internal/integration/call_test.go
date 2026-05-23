package integration

// Stage 3b integration test: exercise the gmk-call flow end-to-end
// against the example files. For each function in each examples/*/gmk.yml,
// invoke it through the same machinery `gmk call` uses (load + dispatcher
// + materialize + runner) and assert that the call succeeds with no error.
//
// This complements TestExamplesRun (which runs targets) and TestExamplesLoad
// (which only loads). It catches regressions in the new Stage 3b stack
// before they reach the CLI.

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/expr"
	"github.com/suhrut/gmk/internal/funcs"
	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/load"
	"github.com/suhrut/gmk/internal/materialize"
	"github.com/suhrut/gmk/internal/resolve"
	"github.com/suhrut/gmk/internal/runner"
)

func TestExamplesCall(t *testing.T) {
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
			if len(project.Functions) == 0 {
				t.Skip("no functions in this example")
			}

			for _, fnName := range project.FunctionOrder {
				fn := project.Functions[fnName]
				args := defaultArgsFor(fn)
				t.Run(fnName, func(t *testing.T) {
					if err := callFunctionUnderTest(t, project, fn, args); err != nil {
						t.Errorf("call %s: %v", fnName, err)
					}
				})
			}

			// Cleanup the scratch dir.
			t.Cleanup(func() {
				cacheDir := filepath.Join(project.Root, ".gmk-cache")
				removeAllSilent(cacheDir)
			})
		})
	}
}

// defaultArgsFor returns a sample arg map for a function: all params with
// defaults stay defaulted; required params get a type-appropriate stub.
// This lets the integration test exercise functions without each
// example having to declare a "test invocation" alongside.
func defaultArgsFor(fn *ir.Function) map[string]expr.Value {
	args := map[string]expr.Value{}
	for _, p := range fn.Params {
		if p.Default != nil {
			continue // dispatcher will fill it in
		}
		// Required param — supply a stub of the right type.
		switch p.Type {
		case "string":
			args[p.Name] = expr.NewString("test")
		case "int":
			args[p.Name] = expr.NewInt(1)
		case "float":
			args[p.Name] = expr.NewFloat(1.0)
		case "bool":
			args[p.Name] = expr.NewBool(false)
		case "list":
			args[p.Name] = expr.NewList(nil)
		case "map":
			args[p.Name] = expr.NewMap(map[string]expr.Value{})
		}
	}
	return args
}

// callFunctionUnderTest replicates the cli/call.go path without the CLI:
// build the dispatcher, evaluate prelude, materialize, run.
func callFunctionUnderTest(t *testing.T, p *ir.Project, fn *ir.Function, args map[string]expr.Value) error {
	t.Helper()
	day := materialize.Today()

	var dispatch *funcs.Dispatcher
	doRun := func(targetFn *ir.Function, boundArgs map[string]expr.Value) (expr.Value, error) {
		preludeValues, perr := evalPreludeForTest(p, targetFn.Prelude, boundArgs, dispatch)
		if perr != nil {
			return expr.NewNone(), perr
		}
		if targetFn.Run == "" {
			return expr.NewMap(preludeValues), nil
		}
		c := materialize.CallableFromFunction(targetFn)
		inv, merr := materialize.MaterializeCallable(c, materialize.MaterializeOpts{
			ProjectRoot:   p.Root,
			Day:           day,
			Args:          boundArgs,
			PreludeValues: preludeValues,
			Languages:     p.Languages,
		})
		if merr != nil {
			return expr.NewNone(), merr
		}
		// Discard the body's stdout/stderr — tests don't care about
		// them, only about exit code and result value.
		var sink bytes.Buffer
		res, rerr := runner.RunCallable(inv, runner.RunCallableOpts{
			Stdout:     &sink,
			Stderr:     &sink,
			InheritEnv: true,
		})
		if rerr != nil {
			return expr.NewNone(), rerr
		}
		if res.ExitCode != 0 {
			return expr.NewNone(), &exitErr{code: res.ExitCode, name: targetFn.Name, output: sink.String()}
		}
		return res.Value, nil
	}
	dispatch = funcs.New(p.Functions, doRun)

	_, err := dispatch.ResolveCall(fn.Name, args)
	if err != nil {
		// Some examples (multi-language) include languages that may
		// not be present in the test environment. Skip rather than
		// fail when the interpreter is missing.
		if strings.Contains(err.Error(), "cannot find interpreter") {
			t.Skipf("interpreter not available: %v", err)
			return nil
		}
		return err
	}
	return nil
}

// evalPreludeForTest is the same as the CLI's evaluatePrelude, duplicated
// to keep this test independent of internal/cli (which would create a
// test-only import cycle).
func evalPreludeForTest(p *ir.Project, prelude []ir.PreludeEntry, args map[string]expr.Value, calls expr.CallResolver) (map[string]expr.Value, error) {
	if len(prelude) == 0 {
		return map[string]expr.Value{}, nil
	}
	layered := &preludeScopeForTest{args: args, bound: map[string]expr.Value{}, project: p}
	out := make(map[string]expr.Value, len(prelude))
	for _, entry := range prelude {
		ev := &expr.Evaluator{
			Vars:        layered,
			EnvProvider: func(string) (string, bool) { return "", false },
			Funcs:       expr.DefaultFuncs(),
			Calls:       calls,
		}
		v, err := ev.Eval(entry.Expr)
		if err != nil {
			return nil, err
		}
		layered.bound[entry.Name] = v
		out[entry.Name] = v
	}
	return out, nil
}

type preludeScopeForTest struct {
	args    map[string]expr.Value
	bound   map[string]expr.Value
	project *ir.Project
}

func (s *preludeScopeForTest) ResolveVar(name string) (expr.Value, error) {
	if v, ok := s.args[name]; ok {
		return v, nil
	}
	if v, ok := s.bound[name]; ok {
		return v, nil
	}
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

type exitErr struct {
	code   int
	name   string
	output string
}

func (e *exitErr) Error() string {
	return "body of " + e.name + " exited " + itoaTest(e.code) + " — output:\n" + e.output
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// removeAllSilent is a test-helper wrapper for os.RemoveAll that
// discards errors (the cleanup is best-effort; a leftover scratch dir
// isn't a test failure).
func removeAllSilent(path string) {
	_ = osRemoveAll(path)
}
