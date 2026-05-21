package load

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchPath_ProjectLocalFirst(t *testing.T) {
	t.Setenv("GMK_PATH", "")
	got := SearchPath("/home/u/proj")
	if len(got) == 0 || got[0] != "/home/u/proj/.gmk/lib" {
		t.Errorf("first entry should be project-local, got %v", got)
	}
}

func TestSearchPath_NoProjectRoot(t *testing.T) {
	t.Setenv("GMK_PATH", "")
	got := SearchPath("")
	for _, d := range got {
		if strings.Contains(d, ".gmk/lib") && !strings.HasPrefix(d, "/home") && !strings.HasPrefix(d, "/root") {
			// Should not include a project-local entry; only $HOME based.
		}
	}
	// Should still include $HOME/.gmk/lib and the system paths.
	hasSys := false
	for _, d := range got {
		if d == "/usr/local/share/gmk/lib" {
			hasSys = true
		}
	}
	if !hasSys {
		t.Errorf("expected /usr/local/share/gmk/lib in path, got %v", got)
	}
}

func TestSearchPath_GMKPathExpansion(t *testing.T) {
	t.Setenv("GMK_PATH", "/a:/b:/c")
	got := SearchPath("/proj")

	// Expected: [/proj/.gmk/lib, /a, /b, /c, ~/.gmk/lib, /usr/local/..., /usr/...]
	pos := func(s string) int {
		for i, d := range got {
			if d == s {
				return i
			}
		}
		return -1
	}
	if pos("/proj/.gmk/lib") != 0 {
		t.Errorf("project-local should be first, got %v", got)
	}
	if pos("/a") < pos("/proj/.gmk/lib") || pos("/a") > pos("/b") {
		t.Errorf("GMK_PATH entries should appear after project-local in order, got %v", got)
	}
	if pos("/b") > pos("/c") {
		t.Errorf("GMK_PATH entries should preserve declaration order, got %v", got)
	}
}

func TestSearchPath_EmptyGMKPathSegments(t *testing.T) {
	t.Setenv("GMK_PATH", "::/a::")
	got := SearchPath("")
	count := 0
	for _, d := range got {
		if d == "" {
			count++
		}
		if d == "/a" {
			count--
		}
	}
	// Empty segments should be skipped; /a should appear.
	hasA := false
	for _, d := range got {
		if d == "/a" {
			hasA = true
		}
	}
	if !hasA {
		t.Errorf("expected /a in path, got %v", got)
	}
	for _, d := range got {
		if d == "" {
			t.Errorf("empty path segment leaked into search list: %v", got)
		}
	}
}

func TestResolveLibInclude_FoundInProjectLocal(t *testing.T) {
	t.Setenv("GMK_PATH", "")
	projRoot := t.TempDir()
	libDir := filepath.Join(projRoot, ".gmk", "lib", "probes")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	libFile := filepath.Join(libDir, "system.yml")
	if err := os.WriteFile(libFile, []byte("vars: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveLibInclude("probes/system.yml", projRoot)
	if err != nil {
		t.Fatalf("ResolveLibInclude: %v", err)
	}
	if got != libFile {
		t.Errorf("got %q, want %q", got, libFile)
	}
}

func TestResolveLibInclude_FoundViaGMKPath(t *testing.T) {
	libRoot := t.TempDir()
	libDir := filepath.Join(libRoot, "extra")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	libFile := filepath.Join(libDir, "tool.yml")
	if err := os.WriteFile(libFile, []byte("vars: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GMK_PATH", libRoot)

	got, err := ResolveLibInclude("extra/tool.yml", "")
	if err != nil {
		t.Fatalf("ResolveLibInclude: %v", err)
	}
	if got != libFile {
		t.Errorf("got %q, want %q", got, libFile)
	}
}

func TestResolveLibInclude_NotFound(t *testing.T) {
	t.Setenv("GMK_PATH", "")
	_, err := ResolveLibInclude("does/not/exist.yml", t.TempDir())
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !errors.Is(err, ErrIncludeNotFound) {
		t.Errorf("err should wrap ErrIncludeNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "search path") {
		t.Errorf("err should mention searched paths, got %v", err)
	}
}

func TestResolveLibInclude_RejectAbsolute(t *testing.T) {
	_, err := ResolveLibInclude("/abs/path.yml", "")
	if err == nil {
		t.Fatal("expected error for absolute lib include")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("err should explain reason, got %v", err)
	}
}

func TestResolveLibInclude_EmptySpec(t *testing.T) {
	_, err := ResolveLibInclude("", "")
	if err == nil {
		t.Fatal("expected error for empty spec")
	}
}

func TestIsLibInclude(t *testing.T) {
	cases := []struct {
		raw      string
		wantSpec string
		wantOK   bool
	}{
		{"<probes/system.yml>", "probes/system.yml", true},
		{"<x>", "x", true},
		{"<>", "", true}, // syntactically a lib include with empty spec; handler rejects
		{"./local.yml", "", false},
		{"/abs.yml", "", false},
		{"plain", "", false},
		{"<unclosed", "", false},
		{"unopened>", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			spec, ok := IsLibInclude(tc.raw)
			if ok != tc.wantOK || spec != tc.wantSpec {
				t.Errorf("IsLibInclude(%q) = (%q, %v), want (%q, %v)",
					tc.raw, spec, ok, tc.wantSpec, tc.wantOK)
			}
		})
	}
}

func TestIsLocalInclude(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"./local.yml", true},
		{"./nested/file.yml", true},
		{"../sibling.yml", true},
		{"/abs/path.yml", true},
		{"<lib>", false},
		{"plain.yml", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			if got := IsLocalInclude(tc.raw); got != tc.want {
				t.Errorf("IsLocalInclude(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
