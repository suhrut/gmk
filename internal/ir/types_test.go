package ir

import "testing"

func TestSourceLocString(t *testing.T) {
	tests := []struct {
		name string
		loc  SourceLoc
		want string
	}{
		{"empty", SourceLoc{}, "<unknown>"},
		{"file only", SourceLoc{File: "build.yml"}, "build.yml"},
		{"file and line", SourceLoc{File: "build.yml", Line: 42}, "build.yml:42"},
		{"full", SourceLoc{File: "build.yml", Line: 42, Column: 5}, "build.yml:42:5"},
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
		{0, "0"},
		{1, "1"},
		{9, "9"},
		{10, "10"},
		{99, "99"},
		{100, "100"},
		{12345, "12345"},
	}
	for _, tc := range tests {
		if got := itoa(tc.in); got != tc.want {
			t.Errorf("itoa(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
