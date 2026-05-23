package expr_test

import (
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/expr"
)

// helper: parse one template and return root node.
func parseTpl(t *testing.T, src string) expr.Node {
	t.Helper()
	n, err := expr.ParseTemplate(src, expr.Position{File: "test", Line: 1, Col: 1})
	if err != nil {
		t.Fatalf("ParseTemplate(%q): %v", src, err)
	}
	return n
}

// helper: parse a template expecting failure.
func parseFail(t *testing.T, src string, wantMsg string) {
	t.Helper()
	_, err := expr.ParseTemplate(src, expr.Position{File: "test", Line: 1, Col: 1})
	if err == nil {
		t.Fatalf("ParseTemplate(%q): expected error, got nil", src)
	}
	if wantMsg != "" && !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("ParseTemplate(%q): error %v doesn't contain %q", src, err, wantMsg)
	}
}

// findFirst walks the tree and returns the first Node matching pred.
// Helper for asserting that AST contains the expected node type.
func findFirst(root expr.Node, pred func(expr.Node) bool) expr.Node {
	if pred(root) {
		return root
	}
	switch n := root.(type) {
	case *expr.Concat:
		for _, p := range n.Parts {
			if got := findFirst(p, pred); got != nil {
				return got
			}
		}
	case *expr.FieldAccess:
		return findFirst(n.Inner, pred)
	case *expr.IndexAccess:
		if got := findFirst(n.Inner, pred); got != nil {
			return got
		}
		return findFirst(n.Key, pred)
	case *expr.Pipeline:
		if got := findFirst(n.Source, pred); got != nil {
			return got
		}
	case *expr.Modifier:
		return findFirst(n.Inner, pred)
	case *expr.BinaryOp:
		if got := findFirst(n.Lhs, pred); got != nil {
			return got
		}
		return findFirst(n.Rhs, pred)
	case *expr.NamedCall:
		for _, a := range n.Args {
			if got := findFirst(a.Value, pred); got != nil {
				return got
			}
		}
	case *expr.FuncCall:
		for _, a := range n.Args {
			if got := findFirst(a, pred); got != nil {
				return got
			}
		}
	}
	return nil
}

func TestParse_FieldAccess(t *testing.T) {
	n := parseTpl(t, "${user.name}")
	fa, ok := n.(*expr.FieldAccess)
	if !ok {
		t.Fatalf("expected FieldAccess, got %T (%v)", n, n)
	}
	if fa.Field != "name" {
		t.Errorf("field=%q, want name", fa.Field)
	}
	inner, ok := fa.Inner.(*expr.VarRef)
	if !ok {
		t.Fatalf("inner: expected VarRef, got %T", fa.Inner)
	}
	if inner.Name != "user" {
		t.Errorf("inner.Name=%q, want user", inner.Name)
	}
}

func TestParse_NestedFieldAccess(t *testing.T) {
	n := parseTpl(t, "${user.address.city}")
	// FieldAccess{Inner: FieldAccess{Inner: VarRef{user}, Field: address}, Field: city}
	outer, ok := n.(*expr.FieldAccess)
	if !ok {
		t.Fatalf("expected FieldAccess, got %T", n)
	}
	if outer.Field != "city" {
		t.Errorf("outer field=%q", outer.Field)
	}
	mid, ok := outer.Inner.(*expr.FieldAccess)
	if !ok {
		t.Fatalf("mid: expected FieldAccess, got %T", outer.Inner)
	}
	if mid.Field != "address" {
		t.Errorf("mid field=%q", mid.Field)
	}
	inner, ok := mid.Inner.(*expr.VarRef)
	if !ok {
		t.Fatalf("inner: expected VarRef, got %T", mid.Inner)
	}
	if inner.Name != "user" {
		t.Errorf("inner name=%q", inner.Name)
	}
}

func TestParse_IndexAccess_Int(t *testing.T) {
	n := parseTpl(t, "${hosts[0]}")
	idx, ok := n.(*expr.IndexAccess)
	if !ok {
		t.Fatalf("expected IndexAccess, got %T", n)
	}
	inner, ok := idx.Inner.(*expr.VarRef)
	if !ok || inner.Name != "hosts" {
		t.Errorf("inner: %v", idx.Inner)
	}
	keyLit, ok := idx.Key.(*expr.Literal)
	if !ok || keyLit.Value.Kind != expr.IntKind || keyLit.Value.Int != 0 {
		t.Errorf("key: %v", idx.Key)
	}
}

func TestParse_IndexAccess_String(t *testing.T) {
	n := parseTpl(t, `${cfg["host"]}`)
	idx, ok := n.(*expr.IndexAccess)
	if !ok {
		t.Fatalf("expected IndexAccess, got %T", n)
	}
	keyLit, ok := idx.Key.(*expr.Literal)
	if !ok || keyLit.Value.Str != "host" {
		t.Errorf("key: %v", idx.Key)
	}
}

func TestParse_IndexAccess_VarKey(t *testing.T) {
	n := parseTpl(t, "${cfg[key]}")
	idx, ok := n.(*expr.IndexAccess)
	if !ok {
		t.Fatalf("expected IndexAccess, got %T", n)
	}
	keyVar, ok := idx.Key.(*expr.VarRef)
	if !ok || keyVar.Name != "key" {
		t.Errorf("key var: %v", idx.Key)
	}
}

func TestParse_ChainedAccess(t *testing.T) {
	// hosts[0].name -> FieldAccess{Inner: IndexAccess{Inner: VarRef, Key: 0}, Field: name}
	n := parseTpl(t, "${hosts[0].name}")
	fa, ok := n.(*expr.FieldAccess)
	if !ok {
		t.Fatalf("expected FieldAccess at outer, got %T", n)
	}
	if fa.Field != "name" {
		t.Errorf("field=%q", fa.Field)
	}
	idx, ok := fa.Inner.(*expr.IndexAccess)
	if !ok {
		t.Fatalf("expected IndexAccess at middle, got %T", fa.Inner)
	}
	_ = idx
}

func TestParse_IndexAfterField(t *testing.T) {
	// user.tags[0] -> IndexAccess{Inner: FieldAccess{Inner: VarRef, Field: tags}, Key: 0}
	n := parseTpl(t, "${user.tags[0]}")
	idx, ok := n.(*expr.IndexAccess)
	if !ok {
		t.Fatalf("expected IndexAccess at outer, got %T", n)
	}
	fa, ok := idx.Inner.(*expr.FieldAccess)
	if !ok {
		t.Fatalf("expected FieldAccess inside, got %T", idx.Inner)
	}
	if fa.Field != "tags" {
		t.Errorf("field=%q", fa.Field)
	}
}

func TestParse_NamedCall_NoArgs(t *testing.T) {
	n := parseTpl(t, "${call:git-version()}")
	nc, ok := n.(*expr.NamedCall)
	if !ok {
		t.Fatalf("expected NamedCall, got %T", n)
	}
	if nc.Kind != "call" {
		t.Errorf("kind=%q", nc.Kind)
	}
	if nc.Name != "git-version" {
		t.Errorf("name=%q", nc.Name)
	}
	if len(nc.Args) != 0 {
		t.Errorf("args len=%d", len(nc.Args))
	}
}

func TestParse_NamedCall_NamedArgs(t *testing.T) {
	n := parseTpl(t, `${call:render-tpl(src="Dockerfile.tmpl", vars=config)}`)
	nc, ok := n.(*expr.NamedCall)
	if !ok {
		t.Fatalf("expected NamedCall, got %T", n)
	}
	if len(nc.Args) != 2 {
		t.Fatalf("args len=%d", len(nc.Args))
	}
	if nc.Args[0].Name != "src" {
		t.Errorf("arg[0].name=%q", nc.Args[0].Name)
	}
	srcLit, ok := nc.Args[0].Value.(*expr.Literal)
	if !ok || srcLit.Value.Str != "Dockerfile.tmpl" {
		t.Errorf("arg[0].value: %v", nc.Args[0].Value)
	}
	if nc.Args[1].Name != "vars" {
		t.Errorf("arg[1].name=%q", nc.Args[1].Name)
	}
	varsRef, ok := nc.Args[1].Value.(*expr.VarRef)
	if !ok || varsRef.Name != "config" {
		t.Errorf("arg[1].value: %v", nc.Args[1].Value)
	}
}

func TestParse_NamedCall_PositionalArgs(t *testing.T) {
	n := parseTpl(t, `${call:concat("a", "b", "c")}`)
	nc, ok := n.(*expr.NamedCall)
	if !ok {
		t.Fatalf("expected NamedCall, got %T", n)
	}
	if len(nc.Args) != 3 {
		t.Fatalf("args len=%d", len(nc.Args))
	}
	for i, want := range []string{"a", "b", "c"} {
		if nc.Args[i].Name != "" {
			t.Errorf("arg[%d] should be positional (empty name), got %q", i, nc.Args[i].Name)
		}
		lit, ok := nc.Args[i].Value.(*expr.Literal)
		if !ok || lit.Value.Str != want {
			t.Errorf("arg[%d].value: %v want %q", i, nc.Args[i].Value, want)
		}
	}
}

func TestParse_NamedCall_MixedArgs(t *testing.T) {
	// Positional must precede named.
	n := parseTpl(t, `${call:fn("x", key=value)}`)
	nc, ok := n.(*expr.NamedCall)
	if !ok {
		t.Fatalf("expected NamedCall, got %T", n)
	}
	if len(nc.Args) != 2 {
		t.Fatalf("args len=%d", len(nc.Args))
	}
	if nc.Args[0].Name != "" {
		t.Errorf("first should be positional")
	}
	if nc.Args[1].Name != "key" {
		t.Errorf("second should be named")
	}
}

func TestParse_NamedCall_PositionalAfterNamedFails(t *testing.T) {
	parseFail(t, `${call:fn(key=value, "x")}`, "cannot follow named")
}

func TestParse_NamedCall_TrailingComma(t *testing.T) {
	n := parseTpl(t, `${call:fn(a=1, b=2,)}`)
	nc, ok := n.(*expr.NamedCall)
	if !ok {
		t.Fatalf("expected NamedCall, got %T", n)
	}
	if len(nc.Args) != 2 {
		t.Errorf("args len=%d", len(nc.Args))
	}
}

func TestParse_NamedCall_WithFieldAccess(t *testing.T) {
	// Result of a NamedCall can have postfix accessors.
	n := parseTpl(t, `${call:get-config().database.host}`)
	fa, ok := n.(*expr.FieldAccess)
	if !ok {
		t.Fatalf("expected FieldAccess at outer, got %T", n)
	}
	if fa.Field != "host" {
		t.Errorf("outer field=%q", fa.Field)
	}
	mid, ok := fa.Inner.(*expr.FieldAccess)
	if !ok {
		t.Fatalf("expected FieldAccess at middle, got %T", fa.Inner)
	}
	if mid.Field != "database" {
		t.Errorf("middle field=%q", mid.Field)
	}
	nc, ok := mid.Inner.(*expr.NamedCall)
	if !ok {
		t.Fatalf("expected NamedCall at inner, got %T", mid.Inner)
	}
	if nc.Name != "get-config" {
		t.Errorf("call name=%q", nc.Name)
	}
}

func TestParse_TypedRefStillWorks(t *testing.T) {
	// Ensure the kind:name disambiguation doesn't break TypedRef.
	n := parseTpl(t, "${env:USER}")
	tr, ok := n.(*expr.TypedRef)
	if !ok {
		t.Fatalf("expected TypedRef, got %T", n)
	}
	if tr.Kind != "env" || tr.Path != "USER" {
		t.Errorf("kind=%q path=%q", tr.Kind, tr.Path)
	}
}

func TestParse_TypedRefDottedPathStillWorks(t *testing.T) {
	n := parseTpl(t, "${probe:has_tool.docker}")
	tr, ok := n.(*expr.TypedRef)
	if !ok {
		t.Fatalf("expected TypedRef, got %T", n)
	}
	if tr.Path != "has_tool.docker" {
		t.Errorf("path=%q", tr.Path)
	}
}

func TestParse_NamedCallInTemplate(t *testing.T) {
	// NamedCall inside a mixed template.
	n := parseTpl(t, `prefix-${call:f()}-suffix`)
	c, ok := n.(*expr.Concat)
	if !ok {
		t.Fatalf("expected Concat, got %T", n)
	}
	if len(c.Parts) != 3 {
		t.Fatalf("parts len=%d", len(c.Parts))
	}
	if _, ok := c.Parts[1].(*expr.NamedCall); !ok {
		t.Errorf("middle part: %T", c.Parts[1])
	}
}

func TestParse_FieldOnFuncResult(t *testing.T) {
	// Stage 3a function call results can have postfix accessors too.
	n := parseTpl(t, `${default(config, fallback).host}`)
	fa, ok := n.(*expr.FieldAccess)
	if !ok {
		t.Fatalf("expected FieldAccess, got %T", n)
	}
	if _, ok := fa.Inner.(*expr.FuncCall); !ok {
		t.Errorf("inner: %T", fa.Inner)
	}
}

func TestParse_IndexInPipeline(t *testing.T) {
	// Index access works at pipeline source position.
	n := parseTpl(t, `${hosts[0] | upper}`)
	pl, ok := n.(*expr.Pipeline)
	if !ok {
		t.Fatalf("expected Pipeline, got %T", n)
	}
	if _, ok := pl.Source.(*expr.IndexAccess); !ok {
		t.Errorf("source: %T", pl.Source)
	}
}

func TestParse_StringMethods(t *testing.T) {
	// FuncCall.String() output should be stable for ${default(x, "y")}.
	n := parseTpl(t, `${default(x, "y")}`)
	fc, ok := n.(*expr.FuncCall)
	if !ok {
		t.Fatalf("expected FuncCall, got %T", n)
	}
	if got := fc.String(); !strings.Contains(got, "default") {
		t.Errorf("FuncCall.String() = %q", got)
	}

	// FieldAccess.String()
	fa := parseTpl(t, "${a.b.c}")
	if got := fa.String(); got != "${a}.b.c" {
		// VarRef.String returns "${a}" so chained gives "${a}.b.c"
		t.Errorf("FieldAccess.String() = %q", got)
	}

	// IndexAccess.String()
	ix := parseTpl(t, "${a[0]}")
	if got := ix.String(); !strings.Contains(got, "[") {
		t.Errorf("IndexAccess.String() = %q", got)
	}

	// NamedCall.String()
	nc := parseTpl(t, `${call:f(x=1, y=2)}`)
	if got := nc.String(); !strings.Contains(got, "call:f") {
		t.Errorf("NamedCall.String() = %q", got)
	}
}
