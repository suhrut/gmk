package resolve

import (
	"errors"
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/ir"
)

func newProject(vars map[string]string) *ir.Project {
	p := &ir.Project{
		Vars:     make(map[string]*ir.Var, len(vars)),
		VarOrder: make([]string, 0, len(vars)),
	}
	for k, v := range vars {
		p.Vars[k] = &ir.Var{Name: k, Value: v}
		p.VarOrder = append(p.VarOrder, k)
	}
	return p
}

func TestResolveString_Plain(t *testing.T) {
	p := newProject(map[string]string{"x": "hello"})
	got, err := ResolveString("just text, no refs", p)
	if err != nil {
		t.Fatal(err)
	}
	if want := "just text, no refs"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveString_SingleRef(t *testing.T) {
	p := newProject(map[string]string{"name": "world"})
	got, err := ResolveString("hello ${name}!", p)
	if err != nil {
		t.Fatal(err)
	}
	if want := "hello world!"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveString_MultipleRefs(t *testing.T) {
	p := newProject(map[string]string{"a": "alpha", "b": "beta"})
	got, err := ResolveString("${a}+${b}=${a}${b}", p)
	if err != nil {
		t.Fatal(err)
	}
	if want := "alpha+beta=alphabeta"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveString_NestedRef(t *testing.T) {
	// a -> "${b}", b -> "deep"
	p := newProject(map[string]string{"a": "${b}", "b": "deep"})
	got, err := ResolveString("value=${a}", p)
	if err != nil {
		t.Fatal(err)
	}
	if want := "value=deep"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveString_DollarEscape(t *testing.T) {
	p := newProject(map[string]string{"x": "value"})
	got, err := ResolveString("literal $$ then ${x}", p)
	if err != nil {
		t.Fatal(err)
	}
	if want := "literal $ then value"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveString_Cycle(t *testing.T) {
	// a -> ${b}, b -> ${a}  (direct cycle)
	p := newProject(map[string]string{"a": "${b}", "b": "${a}"})
	_, err := ResolveString("${a}", p)
	if err == nil {
		t.Fatal("expected cyclic error, got nil")
	}
	if !strings.Contains(err.Error(), "cyclic") {
		t.Errorf("error %q should mention cycle", err)
	}
}

func TestResolveString_LongerCycle(t *testing.T) {
	// a -> b -> c -> a
	p := newProject(map[string]string{
		"a": "${b}",
		"b": "${c}",
		"c": "${a}",
	})
	_, err := ResolveString("${a}", p)
	if err == nil {
		t.Fatal("expected cyclic error, got nil")
	}
	if !strings.Contains(err.Error(), "cyclic") {
		t.Errorf("error %q should mention cycle", err)
	}
}

func TestResolveString_Undefined(t *testing.T) {
	p := newProject(map[string]string{})
	_, err := ResolveString("${missing}", p)
	if err == nil {
		t.Fatal("expected undefined error, got nil")
	}
	if !errors.Is(err, ErrUndefined) {
		t.Errorf("error %v should wrap ErrUndefined", err)
	}
}

func TestResolveString_UnterminatedRef(t *testing.T) {
	p := newProject(map[string]string{})
	_, err := ResolveString("${unterminated", p)
	if err == nil {
		t.Fatal("expected unterminated error, got nil")
	}
	if !strings.Contains(err.Error(), "unterminated") {
		t.Errorf("error %q should mention unterminated", err)
	}
}

func TestResolveString_EmptyRef(t *testing.T) {
	p := newProject(map[string]string{})
	_, err := ResolveString("${}", p)
	if err == nil {
		t.Fatal("expected empty ref error, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error %q should mention empty", err)
	}
}

func TestResolveString_InvalidName(t *testing.T) {
	p := newProject(map[string]string{})
	// digit-start, env: prefix (typed ref - Stage 3), and special chars all rejected in Stage 1.
	cases := []string{"${1abc}", "${env:HOME}", "${a-b}", "${a.b}"}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, err := ResolveString(in, p)
			if err == nil {
				t.Fatalf("expected invalid name error for %q, got nil", in)
			}
		})
	}
}

func TestResolve_Var(t *testing.T) {
	p := newProject(map[string]string{
		"greeting": "hello",
		"who":      "${greeting} world",
	})
	got, err := Resolve("who", p)
	if err != nil {
		t.Fatal(err)
	}
	if want := "hello world"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolve_Missing(t *testing.T) {
	p := newProject(map[string]string{})
	_, err := Resolve("missing", p)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrUndefined) {
		t.Errorf("err %v should wrap ErrUndefined", err)
	}
}

func TestValidVarName(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"x", true},
		{"_", true},
		{"foo", true},
		{"_foo", true},
		{"foo_bar", true},
		{"FOO_BAR_123", true},
		{"", false},
		{"1foo", false},
		{"foo-bar", false},
		{"foo.bar", false},
		{"foo bar", false},
	}
	for _, tc := range tests {
		t.Run(tc.s, func(t *testing.T) {
			if got := validVarName(tc.s); got != tc.want {
				t.Errorf("validVarName(%q) = %v, want %v", tc.s, got, tc.want)
			}
		})
	}
}
