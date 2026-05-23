package expr

import (
	"errors"
	"strings"
	"testing"
)

// stubResolver implements VarResolver with a static map for tests.
type stubResolver struct {
	vars  map[string]Value
	exprs map[string]string // optional: vars whose values are expression source
	calls int               // counts ResolveVar calls (for cycle/perf tests)
	stack []string          // for cycle detection
}

func (s *stubResolver) ResolveVar(name string) (Value, error) {
	s.calls++
	for _, prior := range s.stack {
		if prior == name {
			return NewNone(), NewEvalError(Position{}, ErrCycle,
				"%s -> %s", strings.Join(s.stack, " -> "), name)
		}
	}
	// Expression-valued vars get evaluated recursively.
	if src, ok := s.exprs[name]; ok {
		node, err := ParseTemplate(src, Position{})
		if err != nil {
			return NewNone(), err
		}
		s.stack = append(s.stack, name)
		defer func() { s.stack = s.stack[:len(s.stack)-1] }()
		e := newTestEvaluator(s)
		return e.Eval(node)
	}
	if v, ok := s.vars[name]; ok {
		return v, nil
	}
	return NewNone(), NewEvalError(Position{}, ErrUndefinedVar, "%s", name)
}

func newTestEvaluator(r VarResolver) *Evaluator {
	return &Evaluator{
		Vars: r,
		EnvProvider: func(name string) (string, bool) {
			switch name {
			case "HOME":
				return "/home/test", true
			case "EMPTY":
				return "", true
			}
			return "", false
		},
		CtxProvider: func(name string) (string, bool) {
			if name == "BRANCH" {
				return "main", true
			}
			return "", false
		},
		Funcs: DefaultFuncs(),
	}
}

func evalSrc(t *testing.T, src string, vars map[string]Value) (string, error) {
	t.Helper()
	n, err := ParseTemplate(src, Position{})
	if err != nil {
		return "", err
	}
	r := &stubResolver{vars: vars}
	e := newTestEvaluator(r)
	return e.EvalToString(n)
}

// ---- basic substitutions ----

func TestEval_PureLiteral(t *testing.T) {
	got, err := evalSrc(t, "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Errorf("got %q", got)
	}
}

func TestEval_VarRef(t *testing.T) {
	got, err := evalSrc(t, "${x}", map[string]Value{"x": NewString("world")})
	if err != nil {
		t.Fatal(err)
	}
	if got != "world" {
		t.Errorf("got %q", got)
	}
}

func TestEval_MixedTemplate(t *testing.T) {
	got, err := evalSrc(t, "hello, ${name}!", map[string]Value{"name": NewString("gss")})
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello, gss!" {
		t.Errorf("got %q", got)
	}
}

func TestEval_UndefinedVar(t *testing.T) {
	_, err := evalSrc(t, "${missing}", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrUndefinedVar) {
		t.Errorf("err = %v, want wrapping ErrUndefinedVar", err)
	}
}

// ---- typed refs ----

func TestEval_EnvRef(t *testing.T) {
	got, err := evalSrc(t, "${env:HOME}", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/home/test" {
		t.Errorf("got %q", got)
	}
}

func TestEval_EnvRef_UnsetIsEmpty(t *testing.T) {
	got, err := evalSrc(t, "${env:NOPE}", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("unset env should be empty, got %q", got)
	}
}

func TestEval_CtxRef(t *testing.T) {
	got, err := evalSrc(t, "${ctx:BRANCH}", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "main" {
		t.Errorf("got %q", got)
	}
}

func TestEval_VarTypedRef(t *testing.T) {
	// ${var:name} is explicit form of ${name}
	got, err := evalSrc(t, "${var:x}", map[string]Value{"x": NewString("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if got != "hi" {
		t.Errorf("got %q", got)
	}
}

func TestEval_UnknownTypedRefKind(t *testing.T) {
	_, err := evalSrc(t, "${probe:x}", nil)
	if err == nil {
		t.Fatal("expected error (probe not supported in S3a)")
	}
}

// ---- modifiers ----

func TestEval_Modifier_Default_TriggersOnEmpty(t *testing.T) {
	got, err := evalSrc(t, "${env:NOPE:-fallback}", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "fallback" {
		t.Errorf("got %q", got)
	}
}

func TestEval_Modifier_Default_TriggersOnEmptyString(t *testing.T) {
	got, err := evalSrc(t, "${env:EMPTY:-fallback}", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "fallback" {
		t.Errorf("got %q", got)
	}
}

func TestEval_Modifier_Default_NotTriggered(t *testing.T) {
	got, err := evalSrc(t, "${env:HOME:-fallback}", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/home/test" {
		t.Errorf("got %q", got)
	}
}

func TestEval_Modifier_Required_FiresOnEmpty(t *testing.T) {
	_, err := evalSrc(t, "${env:NOPE:?TOKEN must be set}", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrRequired) {
		t.Errorf("err = %v, want wrapping ErrRequired", err)
	}
	if !strings.Contains(err.Error(), "TOKEN must be set") {
		t.Errorf("err should include the user message, got %v", err)
	}
}

func TestEval_Modifier_Required_OnUndefinedVarAlsoFires(t *testing.T) {
	_, err := evalSrc(t, "${missing:?need it}", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrRequired) {
		t.Errorf("err = %v, want wrapping ErrRequired", err)
	}
}

func TestEval_Modifier_Required_PassesWhenSet(t *testing.T) {
	got, err := evalSrc(t, "${env:HOME:?HOME required}", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/home/test" {
		t.Errorf("got %q", got)
	}
}

func TestEval_Modifier_Alternate_OnSet(t *testing.T) {
	got, err := evalSrc(t, "${env:HOME:+--verbose}", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "--verbose" {
		t.Errorf("got %q", got)
	}
}

func TestEval_Modifier_Alternate_OnUnset(t *testing.T) {
	got, err := evalSrc(t, "${env:NOPE:+--verbose}", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("got %q", got)
	}
}

// ---- functions and pipelines ----

func TestEval_FuncCall_Upper(t *testing.T) {
	got, err := evalSrc(t, `${upper("hi")}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "HI" {
		t.Errorf("got %q", got)
	}
}

func TestEval_Pipeline(t *testing.T) {
	got, err := evalSrc(t, "${name | upper}", map[string]Value{"name": NewString("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if got != "HI" {
		t.Errorf("got %q", got)
	}
}

func TestEval_Pipeline_MultiStage(t *testing.T) {
	got, err := evalSrc(t, `${name | upper | replace("HI", "BYE")}`, map[string]Value{"name": NewString("  hi  ")})
	if err != nil {
		t.Fatal(err)
	}
	if got != "  BYE  " {
		t.Errorf("got %q", got)
	}
}

func TestEval_Pipeline_TrimChain(t *testing.T) {
	got, err := evalSrc(t, "${name | trim | upper}", map[string]Value{"name": NewString("  hi  ")})
	if err != nil {
		t.Fatal(err)
	}
	if got != "HI" {
		t.Errorf("got %q", got)
	}
}

func TestEval_UnknownFunc(t *testing.T) {
	_, err := evalSrc(t, `${nope("x")}`, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrUndefinedFunc) {
		t.Errorf("err = %v, want wrapping ErrUndefinedFunc", err)
	}
}

// ---- comparison ----

func TestEval_Comparison_StringsEqual(t *testing.T) {
	got, err := evalSrc(t, `${a == "prod"}`, map[string]Value{"a": NewString("prod")})
	if err != nil {
		t.Fatal(err)
	}
	if got != "true" {
		t.Errorf("got %q", got)
	}
}

func TestEval_Comparison_NotEqual(t *testing.T) {
	got, err := evalSrc(t, `${a != "prod"}`, map[string]Value{"a": NewString("dev")})
	if err != nil {
		t.Fatal(err)
	}
	if got != "true" {
		t.Errorf("got %q", got)
	}
}

// ---- nested var resolution ----

func TestEval_NestedVarRefs(t *testing.T) {
	r := &stubResolver{
		exprs: map[string]string{
			"greeting": "hello, ${name}",
			"name":     "world",
		},
	}
	e := newTestEvaluator(r)
	n, _ := ParseTemplate("${greeting}!", Position{})
	got, err := e.EvalToString(n)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello, world!" {
		t.Errorf("got %q", got)
	}
}

func TestEval_CycleDetected(t *testing.T) {
	r := &stubResolver{
		exprs: map[string]string{
			"a": "${b}",
			"b": "${a}",
		},
	}
	e := newTestEvaluator(r)
	n, _ := ParseTemplate("${a}", Position{})
	_, err := e.EvalToString(n)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !errors.Is(err, ErrCycle) {
		t.Errorf("err = %v, want wrapping ErrCycle", err)
	}
}

func TestEval_DeepNestedRef(t *testing.T) {
	r := &stubResolver{
		exprs: map[string]string{
			"a": "${b}",
			"b": "${c}",
			"c": "${d}",
			"d": "deep",
		},
	}
	e := newTestEvaluator(r)
	n, _ := ParseTemplate("${a}", Position{})
	got, err := e.EvalToString(n)
	if err != nil {
		t.Fatal(err)
	}
	if got != "deep" {
		t.Errorf("got %q", got)
	}
}

// ---- error position propagation ----

func TestEval_ErrorIncludesPosition(t *testing.T) {
	pos := Position{File: "gmk.yml", Line: 7, Col: 3}
	n, _ := ParseTemplate("${missing}", pos)
	r := &stubResolver{}
	e := newTestEvaluator(r)
	_, err := e.Eval(n)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "gmk.yml") {
		t.Errorf("err should include file path, got %v", err)
	}
}
