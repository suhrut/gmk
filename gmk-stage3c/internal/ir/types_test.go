package ir

import "testing"

func TestSourceLocString(t *testing.T) {
	tests := []struct {
		name string
		loc  SourceLoc
		want string
	}{
		{"empty", SourceLoc{}, "<unknown>"},
		{"file only", SourceLoc{File: "gmk.yml"}, "gmk.yml"},
		{"file and line", SourceLoc{File: "gmk.yml", Line: 42}, "gmk.yml:42"},
		{"full", SourceLoc{File: "gmk.yml", Line: 42, Column: 5}, "gmk.yml:42:5"},
		{"line without file", SourceLoc{Line: 10}, "<unknown>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.loc.String(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestItoa(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{0, "0"}, {1, "1"}, {9, "9"}, {10, "10"},
		{99, "99"}, {100, "100"}, {12345, "12345"},
	}
	for _, tc := range tests {
		if got := itoa(tc.in); got != tc.want {
			t.Errorf("itoa(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIncludeKindString(t *testing.T) {
	tests := []struct {
		k    IncludeKind
		want string
	}{
		{IncludeLocal, "local"},
		{IncludeLibrary, "library"},
		{IncludeKind(99), "unknown"},
	}
	for _, tc := range tests {
		if got := tc.k.String(); got != tc.want {
			t.Errorf("IncludeKind(%d).String() = %q, want %q", tc.k, got, tc.want)
		}
	}
}

func TestScopeStruct_ZeroValueSafe(t *testing.T) {
	// A zero-value Scope must be safe to read from (defensive: scope.Lookup
	// returns nil for an empty scope rather than panicking).
	s := &Scope{}
	if s.Vars != nil {
		t.Errorf("zero Scope.Vars should be nil, got %v", s.Vars)
	}
	if s.Parent != nil {
		t.Errorf("zero Scope.Parent should be nil, got %v", s.Parent)
	}
	if len(s.Includes) != 0 {
		t.Errorf("zero Scope.Includes should be empty, got %d", len(s.Includes))
	}
}

func TestScopeStruct_BasicTree(t *testing.T) {
	// Verify we can construct a scope tree with the documented relationships.
	parent := &Scope{
		Path: "/",
		Vars: map[string]*Var{"a": {Name: "a", Value: "1"}},
	}
	child := &Scope{
		Path:   "/target[x]",
		Parent: parent,
		Vars:   map[string]*Var{"b": {Name: "b", Value: "2"}},
	}

	if child.Parent != parent {
		t.Error("child.Parent should reference parent")
	}
	if child.Parent.Vars["a"].Value != "1" {
		t.Error("parent var should be reachable via child.Parent")
	}
}
