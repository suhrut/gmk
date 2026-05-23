package load

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeGmkYML creates an empty gmk.yml at dir. We don't need real content
// for discovery tests — discovery only cares about presence, not contents.
func writeGmkYML(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, ProjectFileName)
	if err := os.WriteFile(path, []byte("# test fixture\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestFindProjectFile_FoundAtStartDir is the simplest happy path:
// gmk.yml is in startDir itself, no walking needed.
func TestFindProjectFile_FoundAtStartDir(t *testing.T) {
	dir := t.TempDir()
	want := writeGmkYML(t, dir)

	got, err := FindProjectFile(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantResolved, _ := filepath.EvalSymlinks(want)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestFindProjectFile_FoundUpTheTree exercises actual walk-up: gmk.yml
// is several levels above startDir.
func TestFindProjectFile_FoundUpTheTree(t *testing.T) {
	root := t.TempDir()
	want := writeGmkYML(t, root)
	sub := filepath.Join(root, "services", "auth", "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := FindProjectFile(sub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantResolved, _ := filepath.EvalSymlinks(want)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestFindProjectFile_NestedInnerWins documents (and locks in via test)
// the behavior that when a gmk.yml exists at multiple levels, the
// closest one to startDir wins. By convention this shouldn't happen,
// but if it does the algorithm picks the inner one deterministically
// and we don't try to be clever about detecting the violation.
func TestFindProjectFile_NestedInnerWins(t *testing.T) {
	outer := t.TempDir()
	writeGmkYML(t, outer)

	inner := filepath.Join(outer, "subproject")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	wantInner := writeGmkYML(t, inner)

	startFrom := filepath.Join(inner, "deep", "nested")
	if err := os.MkdirAll(startFrom, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := FindProjectFile(startFrom)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantResolved, _ := filepath.EvalSymlinks(wantInner)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("got %s, want inner %s", got, wantInner)
	}
}

// TestFindProjectFile_NotAGmkProject exercises the error path: no
// gmk.yml anywhere from startDir up to root. We force this by walking
// up from a t.TempDir() — the temp tree contains no gmk.yml and the
// real filesystem doesn't either (the test would be flaky if someone
// had /gmk.yml, but that's so unlikely we ignore it).
func TestFindProjectFile_NotAGmkProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Skip on Windows: the "no gmk.yml all the way to root" assumption
		// can be defeated by drive-letter quirks. Linux/macOS is enough.
		t.Skip("skipping on windows")
	}

	dir := t.TempDir()
	_, err := FindProjectFile(dir)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, ErrNotAGmkProject) {
		t.Errorf("error is %v, want it to wrap ErrNotAGmkProject", err)
	}
}

// TestFindProjectFile_EmptyStartDirUsesCWD ensures the "" startDir
// contract works: it should defer to os.Getwd(). We change into a
// temp directory with a gmk.yml, then call with "".
func TestFindProjectFile_EmptyStartDirUsesCWD(t *testing.T) {
	dir := t.TempDir()
	want := writeGmkYML(t, dir)

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// EvalSymlinks: on macOS, t.TempDir() returns a /var/folders path
	// that's actually a symlink to /private/var/folders. os.Getwd()
	// after chdir resolves the symlink, so the discovered path differs
	// textually from `dir`. Resolve both ends to compare reliably.
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}
	if err := os.Chdir(resolvedDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	got, err := FindProjectFile("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantResolved, _ := filepath.EvalSymlinks(want)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestFindProjectFile_DirectoryNamedGmkYmlIgnored: if there's a
// *directory* called gmk.yml (silly but possible), it should not be
// taken as the project file. We only accept regular files.
func TestFindProjectFile_DirectoryNamedGmkYmlIgnored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows for the same reason as NotAGmkProject")
	}

	dir := t.TempDir()
	// Make a directory (not a file) at dir/gmk.yml — should be skipped.
	if err := os.Mkdir(filepath.Join(dir, ProjectFileName), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, err := FindProjectFile(dir)
	if err == nil {
		t.Fatalf("expected ErrNotAGmkProject when gmk.yml is a directory, got nil")
	}
	if !errors.Is(err, ErrNotAGmkProject) {
		t.Errorf("error is %v, want it to wrap ErrNotAGmkProject", err)
	}
}
