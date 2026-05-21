package expr

import "testing"

func TestValueKindString(t *testing.T) {
	cases := []struct {
		k    ValueKind
		want string
	}{
		{NoneKind, "none"},
		{StringKind, "string"},
		{IntKind, "int"},
		{FloatKind, "float"},
		{BoolKind, "bool"},
		{ListKind, "list"},
		{ValueKind(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.k.String(); got != tc.want {
			t.Errorf("ValueKind(%d).String() = %q, want %q", tc.k, got, tc.want)
		}
	}
}

func TestValue_Constructors(t *testing.T) {
	if v := NewNone(); v.Kind != NoneKind {
		t.Errorf("NewNone wrong kind: %v", v.Kind)
	}
	if v := NewString("hi"); v.Kind != StringKind || v.Str != "hi" {
		t.Errorf("NewString wrong: %+v", v)
	}
	if v := NewInt(42); v.Kind != IntKind || v.Int != 42 {
		t.Errorf("NewInt wrong: %+v", v)
	}
	if v := NewFloat(3.14); v.Kind != FloatKind || v.Flt != 3.14 {
		t.Errorf("NewFloat wrong: %+v", v)
	}
	if v := NewBool(true); v.Kind != BoolKind || !v.Bool {
		t.Errorf("NewBool wrong: %+v", v)
	}
	items := []Value{NewString("a"), NewString("b")}
	if v := NewList(items); v.Kind != ListKind || len(v.List) != 2 {
		t.Errorf("NewList wrong: %+v", v)
	}
}

func TestValue_AsString(t *testing.T) {
	cases := []struct {
		v    Value
		want string
	}{
		{NewNone(), ""},
		{NewString(""), ""},
		{NewString("hello"), "hello"},
		{NewInt(0), "0"},
		{NewInt(42), "42"},
		{NewInt(-7), "-7"},
		{NewFloat(3.14), "3.14"},
		{NewBool(true), "true"},
		{NewBool(false), "false"},
		{NewList([]Value{NewString("a"), NewString("b")}), "[a, b]"},
		{NewList(nil), "[]"},
	}
	for _, tc := range cases {
		if got := tc.v.AsString(); got != tc.want {
			t.Errorf("%+v.AsString() = %q, want %q", tc.v, got, tc.want)
		}
	}
}

func TestValue_AsInt(t *testing.T) {
	cases := []struct {
		v       Value
		want    int64
		wantErr bool
	}{
		{NewInt(42), 42, false},
		{NewFloat(3.7), 3, false}, // truncation, not rounding
		{NewBool(true), 1, false},
		{NewBool(false), 0, false},
		{NewString("42"), 42, false},
		{NewString("-7"), -7, false},
		{NewString("not_a_number"), 0, true},
		{NewNone(), 0, true},
		{NewList(nil), 0, true},
	}
	for _, tc := range cases {
		got, err := tc.v.AsInt()
		if (err != nil) != tc.wantErr {
			t.Errorf("%+v.AsInt() err = %v, wantErr %v", tc.v, err, tc.wantErr)
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("%+v.AsInt() = %d, want %d", tc.v, got, tc.want)
		}
	}
}

func TestValue_AsBool(t *testing.T) {
	// Truthiness rules with bash-ish semantics for strings.
	cases := []struct {
		v    Value
		want bool
	}{
		{NewBool(true), true},
		{NewBool(false), false},
		{NewNone(), false},
		{NewString(""), false},
		{NewString("0"), false},
		{NewString("false"), false},
		{NewString("False"), false},
		{NewString("FALSE"), false},
		{NewString("no"), false},
		{NewString("off"), false},
		{NewString("anything_else"), true},
		{NewString("1"), true},
		{NewInt(0), false},
		{NewInt(1), true},
		{NewInt(-1), true},
		{NewFloat(0.0), false},
		{NewFloat(0.1), true},
		{NewList(nil), false},
		{NewList([]Value{NewString("x")}), true},
	}
	for _, tc := range cases {
		if got := tc.v.AsBool(); got != tc.want {
			t.Errorf("%+v.AsBool() = %v, want %v", tc.v, got, tc.want)
		}
	}
}

func TestValue_IsEmpty(t *testing.T) {
	// IsEmpty != AsBool. IsEmpty matches bash ${var:-...} semantics:
	// unset OR empty string. Numbers (even 0) and booleans (even false)
	// are NOT empty.
	cases := []struct {
		v    Value
		want bool
	}{
		{NewNone(), true},
		{NewString(""), true},
		{NewString("0"), false}, // <-- 0 is not empty, unlike AsBool
		{NewString("false"), false},
		{NewString("hi"), false},
		{NewInt(0), false},
		{NewBool(false), false},
		{NewList(nil), true},
		{NewList([]Value{NewString("x")}), false},
	}
	for _, tc := range cases {
		if got := tc.v.IsEmpty(); got != tc.want {
			t.Errorf("%+v.IsEmpty() = %v, want %v", tc.v, got, tc.want)
		}
	}
}

func TestValue_Equal_SameKind(t *testing.T) {
	cases := []struct {
		a, b Value
		want bool
	}{
		{NewString("hi"), NewString("hi"), true},
		{NewString("hi"), NewString("bye"), false},
		{NewInt(42), NewInt(42), true},
		{NewInt(42), NewInt(43), false},
		{NewBool(true), NewBool(true), true},
		{NewBool(true), NewBool(false), false},
		{NewNone(), NewNone(), true},
		{NewList([]Value{NewString("a")}), NewList([]Value{NewString("a")}), true},
		{NewList([]Value{NewString("a")}), NewList([]Value{NewString("b")}), false},
		{NewList(nil), NewList(nil), true},
	}
	for _, tc := range cases {
		if got := tc.a.Equal(tc.b); got != tc.want {
			t.Errorf("%+v.Equal(%+v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestValue_Equal_CrossKindStringCoerce(t *testing.T) {
	// YAML-friendly: "1" == 1 because users frequently write numbers as
	// strings in YAML.
	if !NewString("1").Equal(NewInt(1)) {
		t.Errorf("\"1\" should equal Int(1)")
	}
	if !NewString("true").Equal(NewBool(true)) {
		t.Errorf("\"true\" should equal Bool(true)")
	}
	if NewString("hi").Equal(NewInt(1)) {
		t.Errorf("\"hi\" should not equal Int(1)")
	}
}

func TestLowercase(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"abc", "abc"},
		{"ABC", "abc"},
		{"AbC123", "abc123"},
		{"!@#", "!@#"},
	}
	for _, tc := range cases {
		if got := lowercase(tc.in); got != tc.want {
			t.Errorf("lowercase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
