package expr_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/expr"
)

func TestValueMap_AsString_JSON(t *testing.T) {
	m := expr.NewMap(map[string]expr.Value{
		"name": expr.NewString("api"),
		"port": expr.NewInt(8080),
	})
	got := m.AsString()
	// Map serialization should be valid JSON. Key order is sorted in
	// our JSON marshaller, but to keep the test resilient we re-parse
	// rather than string-compare.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("AsString didn't produce valid JSON: %s — %v", got, err)
	}
	if parsed["name"] != "api" {
		t.Errorf("name field: %v", parsed["name"])
	}
	if int(parsed["port"].(float64)) != 8080 {
		t.Errorf("port field: %v", parsed["port"])
	}
}

func TestValueList_AsString_JSON(t *testing.T) {
	l := expr.NewList([]expr.Value{
		expr.NewString("a"),
		expr.NewInt(2),
		expr.NewBool(true),
	})
	got := l.AsString()
	if got != `["a",2,true]` {
		t.Errorf("got %q, want [\"a\",2,true]", got)
	}
}

func TestValueMap_AsBool(t *testing.T) {
	if expr.NewMap(map[string]expr.Value{}).AsBool() {
		t.Error("empty map should be false")
	}
	if !expr.NewMap(map[string]expr.Value{"k": expr.NewString("v")}).AsBool() {
		t.Error("non-empty map should be true")
	}
}

func TestValueMap_IsEmpty(t *testing.T) {
	if !expr.NewMap(map[string]expr.Value{}).IsEmpty() {
		t.Error("empty map should be empty")
	}
	if expr.NewMap(map[string]expr.Value{"k": expr.NewString("v")}).IsEmpty() {
		t.Error("non-empty map should not be empty")
	}
}

func TestValueMap_Equal(t *testing.T) {
	a := expr.NewMap(map[string]expr.Value{
		"x": expr.NewInt(1),
		"y": expr.NewString("two"),
	})
	b := expr.NewMap(map[string]expr.Value{
		"y": expr.NewString("two"),
		"x": expr.NewInt(1),
	})
	if !a.Equal(b) {
		t.Error("maps with same contents should be equal regardless of order")
	}

	c := expr.NewMap(map[string]expr.Value{"x": expr.NewInt(1)})
	if a.Equal(c) {
		t.Error("maps with different sizes shouldn't be equal")
	}

	d := expr.NewMap(map[string]expr.Value{
		"x": expr.NewInt(99),
		"y": expr.NewString("two"),
	})
	if a.Equal(d) {
		t.Error("maps with different values shouldn't be equal")
	}
}

func TestValueToJSON(t *testing.T) {
	cases := []struct {
		name string
		v    expr.Value
		want any
	}{
		{"none", expr.NewNone(), nil},
		{"bool true", expr.NewBool(true), true},
		{"bool false", expr.NewBool(false), false},
		{"int", expr.NewInt(42), int64(42)},
		{"float", expr.NewFloat(3.14), 3.14},
		{"string", expr.NewString("hi"), "hi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.v.ToJSON()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v (%T), want %v (%T)", got, got, tc.want, tc.want)
			}
		})
	}
}

func TestValueToJSON_List(t *testing.T) {
	v := expr.NewList([]expr.Value{
		expr.NewString("a"),
		expr.NewInt(2),
	})
	got := v.ToJSON()
	want := []any{"a", int64(2)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestValueToJSON_Map(t *testing.T) {
	v := expr.NewMap(map[string]expr.Value{
		"name": expr.NewString("api"),
		"port": expr.NewInt(8080),
	})
	got := v.ToJSON().(map[string]any)
	if got["name"] != "api" {
		t.Errorf("name: got %v", got["name"])
	}
	if got["port"].(int64) != 8080 {
		t.Errorf("port: got %v", got["port"])
	}
}

func TestValueFromJSON_Scalars(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want expr.ValueKind
	}{
		{"nil", nil, expr.NoneKind},
		{"bool", true, expr.BoolKind},
		{"int", 42, expr.IntKind},
		{"int64", int64(42), expr.IntKind},
		{"float", 3.14, expr.FloatKind},
		{"string", "hi", expr.StringKind},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expr.FromJSON(tc.in)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got.Kind != tc.want {
				t.Errorf("got kind %v, want %v", got.Kind, tc.want)
			}
		})
	}
}

func TestValueFromJSON_JsonNumber(t *testing.T) {
	// When parsed with UseNumber, integers come through as json.Number.
	// FromJSON should preserve int-vs-float by looking at the form.
	v, err := expr.FromJSON(json.Number("42"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != expr.IntKind || v.Int != 42 {
		t.Errorf("json.Number(\"42\"): got %+v", v)
	}

	v, err = expr.FromJSON(json.Number("3.14"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != expr.FloatKind {
		t.Errorf("json.Number(\"3.14\"): got %+v", v)
	}
}

func TestValueFromJSON_Nested(t *testing.T) {
	raw := `{
		"version": "1.2.3",
		"port": 8080,
		"enabled": true,
		"hosts": ["a", "b"],
		"config": {"replicas": 3, "labels": {"team": "infra"}}
	}`
	var generic any
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		t.Fatal(err)
	}
	v, err := expr.FromJSON(generic)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	if v.Kind != expr.MapKind {
		t.Fatalf("root kind: %v", v.Kind)
	}
	if v.Map["version"].Kind != expr.StringKind {
		t.Errorf("version kind: %v", v.Map["version"].Kind)
	}
	if v.Map["hosts"].Kind != expr.ListKind {
		t.Errorf("hosts kind: %v", v.Map["hosts"].Kind)
	}
	if len(v.Map["hosts"].List) != 2 {
		t.Errorf("hosts len: %d", len(v.Map["hosts"].List))
	}
	if v.Map["config"].Kind != expr.MapKind {
		t.Errorf("config kind: %v", v.Map["config"].Kind)
	}
	team := v.Map["config"].Map["labels"].Map["team"].Str
	if team != "infra" {
		t.Errorf("deep field: got %q", team)
	}
}

func TestValueFromJSON_Roundtrip(t *testing.T) {
	original := expr.NewMap(map[string]expr.Value{
		"a": expr.NewString("x"),
		"b": expr.NewInt(7),
		"c": expr.NewList([]expr.Value{expr.NewBool(true), expr.NewBool(false)}),
		"d": expr.NewMap(map[string]expr.Value{"inner": expr.NewString("y")}),
	})

	// Value -> JSON-go -> []byte -> back to JSON-go -> Value
	js := original.ToJSON()
	bs, err := json.Marshal(js)
	if err != nil {
		t.Fatal(err)
	}
	var back any
	if err := json.Unmarshal(bs, &back); err != nil {
		t.Fatal(err)
	}
	restored, err := expr.FromJSON(back)
	if err != nil {
		t.Fatal(err)
	}

	// json.Unmarshal defaults to float64 for numbers, so b will be
	// FloatKind after round-trip. That's expected/documented.
	if restored.Map["a"].Str != "x" {
		t.Errorf("a: %v", restored.Map["a"])
	}
	if restored.Map["c"].Kind != expr.ListKind {
		t.Errorf("c kind: %v", restored.Map["c"].Kind)
	}
	if restored.Map["d"].Map["inner"].Str != "y" {
		t.Errorf("d.inner: %v", restored.Map["d"].Map["inner"])
	}
}

func TestValueIndex_List(t *testing.T) {
	l := expr.NewList([]expr.Value{
		expr.NewString("a"),
		expr.NewString("b"),
		expr.NewString("c"),
	})
	cases := []struct {
		idx  int64
		want string
		err  bool
	}{
		{0, "a", false},
		{1, "b", false},
		{2, "c", false},
		{-1, "c", false},
		{-3, "a", false},
		{3, "", true},
		{-4, "", true},
	}
	for _, tc := range cases {
		got, err := l.Index(expr.NewInt(tc.idx))
		if (err != nil) != tc.err {
			t.Errorf("Index(%d): err=%v want err=%v", tc.idx, err, tc.err)
		}
		if !tc.err && got.AsString() != tc.want {
			t.Errorf("Index(%d): got %q, want %q", tc.idx, got.AsString(), tc.want)
		}
	}
}

func TestValueIndex_Map(t *testing.T) {
	m := expr.NewMap(map[string]expr.Value{
		"host": expr.NewString("db"),
		"port": expr.NewInt(5432),
	})
	v, err := m.Index(expr.NewString("host"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if v.Str != "db" {
		t.Errorf("got %q", v.Str)
	}

	_, err = m.Index(expr.NewString("missing"))
	if err == nil {
		t.Error("missing key should error")
	}
}

func TestValueIndex_String(t *testing.T) {
	s := expr.NewString("hello")
	v, err := s.Index(expr.NewInt(0))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if v.Str != "h" {
		t.Errorf("got %q", v.Str)
	}
	v, err = s.Index(expr.NewInt(-1))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if v.Str != "o" {
		t.Errorf("got %q", v.Str)
	}
}

func TestValueIndex_OnScalar(t *testing.T) {
	_, err := expr.NewInt(5).Index(expr.NewInt(0))
	if err == nil {
		t.Error("indexing into int should error")
	}
}

func TestValueField(t *testing.T) {
	m := expr.NewMap(map[string]expr.Value{
		"name": expr.NewString("api"),
	})
	v, err := m.Field("name")
	if err != nil {
		t.Fatal(err)
	}
	if v.Str != "api" {
		t.Errorf("got %q", v.Str)
	}

	_, err = m.Field("missing")
	if err == nil || !strings.Contains(err.Error(), "no such field") {
		t.Errorf("expected 'no such field' error, got %v", err)
	}

	_, err = expr.NewString("x").Field("y")
	if err == nil {
		t.Error("Field on non-map should error")
	}
}

func TestValueKeys(t *testing.T) {
	m := expr.NewMap(map[string]expr.Value{
		"z": expr.NewInt(1),
		"a": expr.NewInt(2),
		"m": expr.NewInt(3),
	})
	ks := m.Keys()
	want := []string{"a", "m", "z"}
	if !reflect.DeepEqual(ks, want) {
		t.Errorf("got %v, want %v (sorted)", ks, want)
	}

	l := expr.NewList([]expr.Value{
		expr.NewString("x"),
		expr.NewString("y"),
	})
	if !reflect.DeepEqual(l.Keys(), []string{"0", "1"}) {
		t.Errorf("list keys: got %v", l.Keys())
	}
}
