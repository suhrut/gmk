package expr

import (
	"strings"
	"testing"
)

// callFunc is a small test helper: looks up a func in the default registry
// and invokes it with the given args, returning the (value, error).
func callFunc(t *testing.T, name string, args ...Value) (Value, error) {
	t.Helper()
	fn, ok := DefaultFuncs().Get(name)
	if !ok {
		t.Fatalf("default registry should have %q", name)
	}
	return fn(args)
}

func TestFuncs_Registry(t *testing.T) {
	r := DefaultFuncs()
	// Stage 3a built-ins (15) + Stage 3b structured-value helpers (6) = 21.
	want := []string{
		"upper", "lower", "trim", "trim_left", "trim_right",
		"to_string", "to_int", "len",
		"starts_with", "ends_with", "contains", "replace",
		"default", "coalesce", "join",
		// Stage 3b:
		"keys", "values", "first", "last", "to_json", "from_json",
	}
	for _, name := range want {
		if _, ok := r.Get(name); !ok {
			t.Errorf("missing builtin: %q", name)
		}
	}
	if got := len(r.Names()); got != len(want) {
		t.Errorf("registry has %d funcs, want %d (%v)", got, len(want), r.Names())
	}
}

func TestFuncs_Registry_Register_OverwritesExisting(t *testing.T) {
	r := NewFuncRegistry()
	r.Register("x", func(args []Value) (Value, error) { return NewString("first"), nil })
	r.Register("x", func(args []Value) (Value, error) { return NewString("second"), nil })
	fn, _ := r.Get("x")
	v, _ := fn(nil)
	if v.Str != "second" {
		t.Errorf("got %q, want overwrite", v.Str)
	}
}

func TestFn_Upper_Lower(t *testing.T) {
	v, err := callFunc(t, "upper", NewString("hello"))
	if err != nil || v.Str != "HELLO" {
		t.Errorf("upper(\"hello\") = %v, err=%v", v, err)
	}
	v, err = callFunc(t, "lower", NewString("HELLO"))
	if err != nil || v.Str != "hello" {
		t.Errorf("lower(\"HELLO\") = %v, err=%v", v, err)
	}
	// Cross-kind coercion: numbers stringify first.
	v, _ = callFunc(t, "upper", NewInt(42))
	if v.Str != "42" {
		t.Errorf("upper(42) = %q, want \"42\"", v.Str)
	}
}

func TestFn_Trim(t *testing.T) {
	v, err := callFunc(t, "trim", NewString("  hi  "))
	if err != nil || v.Str != "hi" {
		t.Errorf("trim got %v err %v", v, err)
	}
	// Empty -> empty.
	v, _ = callFunc(t, "trim", NewString(""))
	if v.Str != "" {
		t.Errorf("trim(\"\") = %q", v.Str)
	}
}

func TestFn_TrimLeft_Right(t *testing.T) {
	// Default cutset: whitespace.
	v, err := callFunc(t, "trim_left", NewString("  hi  "))
	if err != nil || v.Str != "hi  " {
		t.Errorf("trim_left got %v err %v", v, err)
	}
	v, err = callFunc(t, "trim_right", NewString("  hi  "))
	if err != nil || v.Str != "  hi" {
		t.Errorf("trim_right got %v err %v", v, err)
	}
	// Custom cutset.
	v, _ = callFunc(t, "trim_left", NewString("xxxhi"), NewString("x"))
	if v.Str != "hi" {
		t.Errorf("trim_left(\"xxxhi\", \"x\") = %q", v.Str)
	}
	v, _ = callFunc(t, "trim_right", NewString("hi/"), NewString("/"))
	if v.Str != "hi" {
		t.Errorf("trim_right got %q", v.Str)
	}
}

func TestFn_ToString(t *testing.T) {
	cases := []struct {
		in   Value
		want string
	}{
		{NewInt(42), "42"},
		{NewFloat(3.14), "3.14"},
		{NewBool(true), "true"},
		{NewString("hi"), "hi"},
		{NewNone(), ""},
	}
	for _, tc := range cases {
		v, err := callFunc(t, "to_string", tc.in)
		if err != nil {
			t.Errorf("to_string(%+v) err: %v", tc.in, err)
			continue
		}
		if v.Str != tc.want {
			t.Errorf("to_string(%+v) = %q, want %q", tc.in, v.Str, tc.want)
		}
		if v.Kind != StringKind {
			t.Errorf("to_string(%+v) kind = %v, want StringKind", tc.in, v.Kind)
		}
	}
}

func TestFn_ToInt(t *testing.T) {
	v, err := callFunc(t, "to_int", NewString("42"))
	if err != nil || v.Int != 42 || v.Kind != IntKind {
		t.Errorf("to_int(\"42\") = %v err %v", v, err)
	}
	v, err = callFunc(t, "to_int", NewFloat(3.7))
	if err != nil || v.Int != 3 {
		t.Errorf("to_int(3.7) = %v err %v", v, err)
	}
	_, err = callFunc(t, "to_int", NewString("not_num"))
	if err == nil {
		t.Errorf("to_int(non-numeric) should fail")
	}
}

func TestFn_Len(t *testing.T) {
	cases := []struct {
		in   Value
		want int64
	}{
		{NewString(""), 0},
		{NewString("hello"), 5},
		{NewList(nil), 0},
		{NewList([]Value{NewString("a"), NewString("b")}), 2},
		{NewNone(), 0},
		{NewInt(123), 3}, // coerces to string first
	}
	for _, tc := range cases {
		v, err := callFunc(t, "len", tc.in)
		if err != nil {
			t.Errorf("len(%+v) err: %v", tc.in, err)
			continue
		}
		if v.Int != tc.want {
			t.Errorf("len(%+v) = %d, want %d", tc.in, v.Int, tc.want)
		}
	}
}

func TestFn_StartsWith(t *testing.T) {
	v, _ := callFunc(t, "starts_with", NewString("hello world"), NewString("hello"))
	if !v.Bool {
		t.Errorf("expected true")
	}
	v, _ = callFunc(t, "starts_with", NewString("hello world"), NewString("world"))
	if v.Bool {
		t.Errorf("expected false")
	}
}

func TestFn_EndsWith(t *testing.T) {
	v, _ := callFunc(t, "ends_with", NewString("hello world"), NewString("world"))
	if !v.Bool {
		t.Errorf("expected true")
	}
	v, _ = callFunc(t, "ends_with", NewString("hello world"), NewString("hello"))
	if v.Bool {
		t.Errorf("expected false")
	}
}

func TestFn_Contains(t *testing.T) {
	v, _ := callFunc(t, "contains", NewString("hello world"), NewString("o w"))
	if !v.Bool {
		t.Errorf("expected true")
	}
	v, _ = callFunc(t, "contains", NewString("hello"), NewString("xyz"))
	if v.Bool {
		t.Errorf("expected false")
	}
}

func TestFn_Replace(t *testing.T) {
	v, _ := callFunc(t, "replace", NewString("hello world"), NewString("world"), NewString("there"))
	if v.Str != "hello there" {
		t.Errorf("got %q", v.Str)
	}
	// ReplaceAll behaviour: every occurrence.
	v, _ = callFunc(t, "replace", NewString("aaa"), NewString("a"), NewString("b"))
	if v.Str != "bbb" {
		t.Errorf("got %q", v.Str)
	}
}

func TestFn_Default(t *testing.T) {
	// Empty -> use default.
	v, _ := callFunc(t, "default", NewString(""), NewString("fallback"))
	if v.Str != "fallback" {
		t.Errorf("got %q", v.Str)
	}
	v, _ = callFunc(t, "default", NewNone(), NewString("fallback"))
	if v.Str != "fallback" {
		t.Errorf("got %q", v.Str)
	}
	// Non-empty -> pass through.
	v, _ = callFunc(t, "default", NewString("val"), NewString("fallback"))
	if v.Str != "val" {
		t.Errorf("got %q", v.Str)
	}
	// 0 and false are NOT empty (matches IsEmpty semantics).
	v, _ = callFunc(t, "default", NewInt(0), NewString("fallback"))
	if v.Kind != IntKind || v.Int != 0 {
		t.Errorf("default(0, ...) should return 0, got %+v", v)
	}
}

func TestFn_Coalesce(t *testing.T) {
	// First non-empty wins.
	v, _ := callFunc(t, "coalesce", NewNone(), NewString(""), NewString("found"), NewString("after"))
	if v.Str != "found" {
		t.Errorf("got %q", v.Str)
	}
	// All empty -> None.
	v, _ = callFunc(t, "coalesce", NewNone(), NewString(""))
	if v.Kind != NoneKind {
		t.Errorf("got %+v, want None", v)
	}
	// Single non-empty.
	v, _ = callFunc(t, "coalesce", NewString("x"))
	if v.Str != "x" {
		t.Errorf("got %q", v.Str)
	}
	// No args -> None.
	v, _ = callFunc(t, "coalesce")
	if v.Kind != NoneKind {
		t.Errorf("got %+v", v)
	}
}

func TestFn_Join_SepFirstThenList(t *testing.T) {
	list := NewList([]Value{NewString("a"), NewString("b"), NewString("c")})
	v, err := callFunc(t, "join", NewString(", "), list)
	if err != nil {
		t.Fatal(err)
	}
	if v.Str != "a, b, c" {
		t.Errorf("got %q", v.Str)
	}
}

func TestFn_Join_ListPipedSepSecond(t *testing.T) {
	// Pipeline form: list piped in first, sep second.
	list := NewList([]Value{NewString("a"), NewString("b")})
	v, err := callFunc(t, "join", list, NewString("-"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Str != "a-b" {
		t.Errorf("got %q", v.Str)
	}
}

func TestFn_Join_VariadicStrings(t *testing.T) {
	// Non-list args: first is sep, rest are joined.
	v, err := callFunc(t, "join", NewString("/"), NewString("a"), NewString("b"), NewString("c"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Str != "a/b/c" {
		t.Errorf("got %q", v.Str)
	}
}

func TestFn_Arity_Mismatch(t *testing.T) {
	// Every fixed-arity func should reject wrong arg counts.
	cases := []struct {
		name string
		args []Value
	}{
		{"upper", nil},
		{"upper", []Value{NewString("a"), NewString("b")}},
		{"replace", []Value{NewString("a")}},
		{"starts_with", []Value{NewString("a")}},
	}
	for _, tc := range cases {
		fn, _ := DefaultFuncs().Get(tc.name)
		_, err := fn(tc.args)
		if err == nil {
			t.Errorf("%s with %d args should err", tc.name, len(tc.args))
			continue
		}
		// Error message should name the function for usability.
		if !strings.Contains(err.Error(), tc.name) {
			t.Errorf("%s err should mention func name, got %v", tc.name, err)
		}
	}
}
