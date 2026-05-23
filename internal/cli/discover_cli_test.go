package cli

// CLI-level coverage for the gmk.yml walk-up discovery (Stage 3c).
//
// Unit-level coverage of FindProjectFile lives in internal/load. These
// tests verify the *integration* — that omitting -f actually triggers
// the walk-up from CWD, that the error from a missing gmk.yml reaches
// the user, and that explicit -f still bypasses discovery.

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suhrut/gmk/internal/load"
)

// chdirT wraps os.Chdir with t.Cleanup restoration. Centralized so each
// test doesn't repeat the boilerplate. Resolves symlinks first because
// on macOS t.TempDir() returns /var/folders/... which symlinks to
// /private/var/folders/... and os.Getwd() returns the resolved form.
func chdirT(t *testing.T, dir string) {
	t.Helper()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("evalsymlinks %s: %v", dir, err)
	}
	if err := os.Chdir(resolved); err != nil {
		t.Fatalf("chdir %s: %v", resolved, err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })
}

// TestRun_DiscoversGmkYmlFromCWD: no -f flag, gmk.yml is in $PWD,
// run discovers it and executes the target.
func TestRun_DiscoversGmkYmlFromCWD(t *testing.T) {
	dir := t.TempDir()
	gmkYML := filepath.Join(dir, "gmk.yml")
	content := `
targets:
  hello:
    run: |
      echo "discovered"
`
	if err := os.WriteFile(gmkYML, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	chdirT(t, dir)

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "hello"}) // no -f

	var out bytes.Buffer
	r.SetOut(&out)
	r.SetErr(&out)
	if err := r.Execute(); err != nil {
		t.Fatalf("Execute: %v (output: %s)", err, out.String())
	}

	// Cache must live next to the discovered gmk.yml (project root),
	// not in the user's home, not nowhere. This is the whole point
	// of the convention.
	bodiesDir := filepath.Join(dir, ".gmk-cache", "bodies")
	found := false
	_ = filepath.WalkDir(bodiesDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, "/body.sh") {
			found = true
		}
		return nil
	})
	if !found {
		t.Errorf(".gmk-cache/bodies/<hash>/body.sh not created next to discovered gmk.yml at %s", dir)
	}
}

// TestRun_DiscoversGmkYmlByWalkingUp: gmk.yml is at the project root,
// CWD is several levels deep; walk-up should find it.
func TestRun_DiscoversGmkYmlByWalkingUp(t *testing.T) {
	root := t.TempDir()
	gmkYML := filepath.Join(root, "gmk.yml")
	content := `
targets:
  ping:
    run: |
      echo pong
`
	if err := os.WriteFile(gmkYML, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	deep := filepath.Join(root, "services", "auth", "src")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	chdirT(t, deep)

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "ping"})
	if err := r.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// The .gmk-cache directory belongs at the project root (where
	// the discovered gmk.yml lives), not at the deep CWD.
	if _, err := os.Stat(filepath.Join(root, ".gmk-cache", "gmk.db")); err != nil {
		t.Errorf("gmk.db should exist at project root %s, got: %v", root, err)
	}
	if _, err := os.Stat(filepath.Join(deep, ".gmk-cache")); err == nil {
		t.Errorf(".gmk-cache should NOT exist at deep CWD %s", deep)
	}
}

// TestRun_NotAGmkProject: no gmk.yml in CWD or anywhere above; the
// command must error out clearly with ErrNotAGmkProject.
//
// Using a temp dir (which never has a gmk.yml ancestor unless something
// is very weird about the host) keeps this hermetic.
func TestRun_NotAGmkProject(t *testing.T) {
	dir := t.TempDir()
	chdirT(t, dir)

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"run", "anything"})

	var out bytes.Buffer
	r.SetOut(&out)
	r.SetErr(&out)
	err := r.Execute()
	if err == nil {
		t.Fatalf("expected error, got nil (output: %s)", out.String())
	}
	if !errors.Is(err, load.ErrNotAGmkProject) {
		t.Errorf("error %v should wrap load.ErrNotAGmkProject", err)
	}
}

// TestList_ExplicitFlagBypassesDiscovery: -f points outside the CWD's
// project tree (which has no gmk.yml). Discovery would fail; the
// explicit path should succeed regardless.
func TestList_ExplicitFlagBypassesDiscovery(t *testing.T) {
	cwdDir := t.TempDir()   // no gmk.yml here
	projDir := t.TempDir()  // gmk.yml lives here
	gmkYML := filepath.Join(projDir, "gmk.yml")
	content := `
targets:
  hi:
    run: |
      echo hi
`
	if err := os.WriteFile(gmkYML, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	chdirT(t, cwdDir)

	r := Root(BuildInfo{Version: "test", Commit: "test"})
	r.SetArgs([]string{"list", "--targets", "-f", gmkYML})

	var out bytes.Buffer
	r.SetOut(&out)
	r.SetErr(&out)
	if err := r.Execute(); err != nil {
		t.Fatalf("Execute: %v (output: %s)", err, out.String())
	}
	if !strings.Contains(out.String(), "hi") {
		t.Errorf("output should list target 'hi', got: %s", out.String())
	}
}
