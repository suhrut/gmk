package expr

import (
	"errors"
	"strings"
	"testing"
)

func TestParseTemplate_PureLiteral(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"hello", "hello"},
		{"hello world", "hello world"},
		{"multi\nline", "multi\nline"},
	}
	for _, tc := range cases {
		n, err := ParseTemplate(tc.in, Position{})
		if err != nil {
			t.Errorf("ParseTemplate(%q) err: %v", tc.in, err)
			continue
		}
		lit, ok := n.(*Literal)
		if !ok {
			t.Errorf("ParseTemplate(%q) returned %T, want *Literal", tc.in, n)
			continue
		}
		if lit.Value.Str != tc.want {
			t.Errorf("ParseTemplate(%q) value = %q, want %q", tc.in, lit.Value.Str, tc.want)
		}
	}
}

func TestParseTemplate_DollarEscape(t *testing.T) {
	n, err := ParseTemplate("price: $$10", Position{})
	if err != nil {
		t.Fatal(err)
	}
	lit, ok := n.(*Literal)
	if !ok {
		t.Fatalf("got %T, want *Literal", n)
	}
	if lit.Value.Str != "price: $10" {
		t.Errorf("got %q, want %q", lit.Value.Str, "price: $10")
	}
}

func TestParseTemplate_SingleSubstitution(t *testing.T) {
	n, err := ParseTemplate("${name}", Position{})
	if err != nil {
		t.Fatal(err)
	}
	// Single ${...} should return the inner node directly, not wrapped in Concat.
	if _, ok := n.(*VarRef); !ok {
		t.Errorf("got %T, want *VarRef", n)
	}
	v := n.(*VarRef)
	if v.Name != "name" {
		t.Errorf("Name = %q", v.Name)
	}
}

func TestParseTemplate_MixedTextAndRefs(t *testing.T) {
	n, err := ParseTemplate("hello, ${name}!", Position{})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := n.(*Concat)
	if !ok {
		t.Fatalf("got %T, want *Concat", n)
	}
	if len(c.Parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(c.Parts))
	}
	if l, ok := c.Parts[0].(*Literal); !ok || l.Value.Str != "hello, " {
		t.Errorf("part[0] wrong: %+v", c.Parts[0])
	}
	if v, ok := c.Parts[1].(*VarRef); !ok || v.Name != "name" {
		t.Errorf("part[1] wrong: %+v", c.Parts[1])
	}
	if l, ok := c.Parts[2].(*Literal); !ok || l.Value.Str != "!" {
		t.Errorf("part[2] wrong: %+v", c.Parts[2])
	}
}

func TestParseTemplate_UnterminatedSubstitution(t *testing.T) {
	_, err := ParseTemplate("hello ${name", Position{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrParse) {
		t.Errorf("err = %v, want ErrParse", err)
	}
}

func TestParseExpr_BareIdent(t *testing.T) {
	n, err := ParseExpr("foo", Position{})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := n.(*VarRef); !ok || v.Name != "foo" {
		t.Errorf("got %+v", n)
	}
}

func TestParseExpr_TypedRef(t *testing.T) {
	cases := []struct {
		in       string
		wantKind string
		wantPath string
	}{
		{"env:HOME", "env", "HOME"},
		{"ctx:TOKEN", "ctx", "TOKEN"},
		{"var:explicit", "var", "explicit"},
		{"probe:has_tool.docker", "probe", "has_tool.docker"},
	}
	for _, tc := range cases {
		n, err := ParseExpr(tc.in, Position{})
		if err != nil {
			t.Errorf("ParseExpr(%q): %v", tc.in, err)
			continue
		}
		tr, ok := n.(*TypedRef)
		if !ok {
			t.Errorf("ParseExpr(%q) -> %T, want *TypedRef", tc.in, n)
			continue
		}
		if tr.Kind != tc.wantKind || tr.Path != tc.wantPath {
			t.Errorf("ParseExpr(%q) = %+v, want kind=%q path=%q", tc.in, tr, tc.wantKind, tc.wantPath)
		}
	}
}

func TestParseExpr_Literals(t *testing.T) {
	cases := []struct {
		in   string
		want Value
	}{
		{`"hello"`, NewString("hello")},
		{`42`, NewInt(42)},
		{`-7`, NewInt(-7)},
		{`3.14`, NewFloat(3.14)},
		{`true`, NewBool(true)},
		{`false`, NewBool(false)},
	}
	for _, tc := range cases {
		n, err := ParseExpr(tc.in, Position{})
		if err != nil {
			t.Errorf("ParseExpr(%q): %v", tc.in, err)
			continue
		}
		lit, ok := n.(*Literal)
		if !ok {
			t.Errorf("ParseExpr(%q) -> %T, want *Literal", tc.in, n)
			continue
		}
		if !lit.Value.Equal(tc.want) {
			t.Errorf("ParseExpr(%q) = %+v, want %+v", tc.in, lit.Value, tc.want)
		}
	}
}

func TestParseExpr_FuncCall(t *testing.T) {
	n, err := ParseExpr(`upper("hello")`, Position{})
	if err != nil {
		t.Fatal(err)
	}
	f, ok := n.(*FuncCall)
	if !ok {
		t.Fatalf("got %T", n)
	}
	if f.Name != "upper" {
		t.Errorf("Name = %q", f.Name)
	}
	if len(f.Args) != 1 {
		t.Fatalf("expected 1 arg, got %d", len(f.Args))
	}
	if l, ok := f.Args[0].(*Literal); !ok || l.Value.Str != "hello" {
		t.Errorf("arg[0] = %+v", f.Args[0])
	}
}

func TestParseExpr_FuncCall_MultipleArgs(t *testing.T) {
	n, err := ParseExpr(`replace(x, "a", "b")`, Position{})
	if err != nil {
		t.Fatal(err)
	}
	f := n.(*FuncCall)
	if len(f.Args) != 3 {
		t.Fatalf("expected 3 args, got %d", len(f.Args))
	}
}

func TestParseExpr_FuncCall_Empty(t *testing.T) {
	n, err := ParseExpr(`pi()`, Position{})
	if err != nil {
		t.Fatal(err)
	}
	f := n.(*FuncCall)
	if len(f.Args) != 0 {
		t.Errorf("expected 0 args, got %d", len(f.Args))
	}
}

func TestParseExpr_Pipeline(t *testing.T) {
	n, err := ParseExpr("name | upper | trim", Position{})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := n.(*Pipeline)
	if !ok {
		t.Fatalf("got %T", n)
	}
	if v, ok := p.Source.(*VarRef); !ok || v.Name != "name" {
		t.Errorf("Source = %+v", p.Source)
	}
	if len(p.Stages) != 2 {
		t.Fatalf("expected 2 stages, got %d", len(p.Stages))
	}
	if p.Stages[0].Name != "upper" || p.Stages[1].Name != "trim" {
		t.Errorf("stage names: %q %q", p.Stages[0].Name, p.Stages[1].Name)
	}
}

func TestParseExpr_PipelineExplicitCall(t *testing.T) {
	n, err := ParseExpr(`name | replace("a", "b")`, Position{})
	if err != nil {
		t.Fatal(err)
	}
	p := n.(*Pipeline)
	if len(p.Stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(p.Stages))
	}
	if p.Stages[0].Name != "replace" {
		t.Errorf("name = %q", p.Stages[0].Name)
	}
	if len(p.Stages[0].Args) != 2 {
		t.Errorf("expected 2 args in stage, got %d", len(p.Stages[0].Args))
	}
}

func TestParseExpr_PipelineRequiresFunc(t *testing.T) {
	_, err := ParseExpr(`x | 42`, Position{})
	if err == nil {
		t.Fatal("expected error for non-func after pipe")
	}
}

func TestParseExpr_Comparison(t *testing.T) {
	cases := []string{
		`a == b`,
		`a == "prod"`,
		`x != 0`,
		`true == false`,
	}
	for _, src := range cases {
		n, err := ParseExpr(src, Position{})
		if err != nil {
			t.Errorf("ParseExpr(%q): %v", src, err)
			continue
		}
		if _, ok := n.(*BinaryOp); !ok {
			t.Errorf("ParseExpr(%q) -> %T, want *BinaryOp", src, n)
		}
	}
}

func TestParseExpr_Modifier_Default(t *testing.T) {
	n, err := ParseExpr(`user:-anonymous`, Position{})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := n.(*Modifier)
	if !ok {
		t.Fatalf("got %T", n)
	}
	if m.Kind != ModDefault {
		t.Errorf("Kind = %v, want ModDefault", m.Kind)
	}
	if m.Arg != "anonymous" {
		t.Errorf("Arg = %q", m.Arg)
	}
	if _, ok := m.Inner.(*VarRef); !ok {
		t.Errorf("Inner = %T", m.Inner)
	}
}

func TestParseExpr_Modifier_OnTypedRef(t *testing.T) {
	n, err := ParseExpr(`env:HOME:-/tmp`, Position{})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := n.(*Modifier)
	if !ok {
		t.Fatalf("got %T", n)
	}
	if _, ok := m.Inner.(*TypedRef); !ok {
		t.Errorf("Inner = %T, want *TypedRef", m.Inner)
	}
	if m.Arg != "/tmp" {
		t.Errorf("Arg = %q", m.Arg)
	}
}

func TestParseExpr_Modifier_Required(t *testing.T) {
	n, err := ParseExpr(`token:?token required`, Position{})
	if err != nil {
		t.Fatal(err)
	}
	m := n.(*Modifier)
	if m.Kind != ModRequired {
		t.Errorf("Kind = %v, want ModRequired", m.Kind)
	}
	if m.Arg != "token required" {
		t.Errorf("Arg = %q", m.Arg)
	}
}

func TestParseExpr_Modifier_Alternate(t *testing.T) {
	n, err := ParseExpr(`debug:+--verbose`, Position{})
	if err != nil {
		t.Fatal(err)
	}
	m := n.(*Modifier)
	if m.Kind != ModAlternate {
		t.Errorf("Kind = %v, want ModAlternate", m.Kind)
	}
}

func TestParseExpr_Modifier_OnlyOnRefs(t *testing.T) {
	// Modifier on a literal -> error.
	_, err := ParseExpr(`"hi":-default`, Position{})
	if err == nil {
		t.Fatal("expected error for modifier on literal")
	}
}

func TestParseExpr_Parens(t *testing.T) {
	n, err := ParseExpr(`(a == b)`, Position{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n.(*BinaryOp); !ok {
		t.Errorf("got %T", n)
	}
}

func TestParseExpr_NestedFuncCalls(t *testing.T) {
	n, err := ParseExpr(`upper(trim(name))`, Position{})
	if err != nil {
		t.Fatal(err)
	}
	f := n.(*FuncCall)
	if f.Name != "upper" {
		t.Errorf("outer = %q", f.Name)
	}
	inner := f.Args[0].(*FuncCall)
	if inner.Name != "trim" {
		t.Errorf("inner = %q", inner.Name)
	}
}

func TestParseExpr_ErrorPositionPropagates(t *testing.T) {
	pos := Position{File: "build.yml", Line: 10, Col: 5}
	_, err := ParseExpr(`@invalid`, pos)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "build.yml") {
		t.Errorf("err should carry file context, got %v", err)
	}
}

func TestParseExpr_BareKeywordTrueFalse(t *testing.T) {
	// `true` and `false` should be parsed as bool literals, not as VarRefs.
	for _, kw := range []string{"true", "false"} {
		n, err := ParseExpr(kw, Position{})
		if err != nil {
			t.Errorf("ParseExpr(%q): %v", kw, err)
			continue
		}
		if _, ok := n.(*Literal); !ok {
			t.Errorf("ParseExpr(%q) = %T, want *Literal", kw, n)
		}
	}
}

func TestParseExpr_BraceInString(t *testing.T) {
	// A } inside a string literal must not terminate the surrounding ${...}.
	n, err := ParseTemplate(`prefix ${replace(name, "}", "_")} suffix`, Position{})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := n.(*Concat)
	if !ok {
		t.Fatalf("got %T", n)
	}
	if len(c.Parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(c.Parts))
	}
	// Middle part is the FuncCall.
	if _, ok := c.Parts[1].(*FuncCall); !ok {
		t.Errorf("part[1] = %T, want *FuncCall", c.Parts[1])
	}
}

func TestParseExpr_NestedBraces(t *testing.T) {
	// Stage 4 may add proper nested ${} handling. For Stage 3a we
	// at least confirm one level of ${...} parses cleanly even when its
	// body contains balanced { }.
	_, err := ParseExpr(`replace(name, "{", "_")`, Position{})
	if err != nil {
		t.Fatal(err)
	}
}
