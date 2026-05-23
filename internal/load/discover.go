// Discovery of the canonical project file (gmk.yml) by walking up from a
// starting directory toward the filesystem root.
//
// Convention (locked in design notes, see docs/LAYOUT.md):
//
//   - A "gmk project" is a directory tree whose root contains exactly one
//     gmk.yml file. Splits live in gmk/*.yml and are pulled in via the
//     top-level gmk.yml's includes: block. Sub-projects, if any, may have
//     local *.yml files in their own directories, but those are always
//     transitively reachable from the top-level gmk.yml.
//
//   - If no gmk.yml is found anywhere from $PWD up to /, the current
//     directory is not part of a gmk project and commands fail with
//     ErrNotAGmkProject.
//
//   - When the user passes -f explicitly, no discovery happens; the given
//     file is used directly (this preserves the rare-but-legitimate case
//     of pointing at a YAML outside the conventional project layout, and
//     keeps existing tests that construct ad-hoc temp-dir fixtures
//     working without ceremony).
//
// We deliberately do not detect nested gmk.yml files, warn about them,
// or implement "highest in the chain wins" workspace-style logic. The
// algorithm is plain "first match walking up" — by convention the first
// match is the only match. If someone violates the convention by nesting,
// the inner one wins because that's what the algorithm produces. We do
// not promise more than that. (See gmk handoff notes: "complexity should
// be a far-future requirement, if at all".)

package load

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ProjectFileName is the canonical name of the top-level gmk project file.
// Renamed from "gmk.yml" in Stage 3c: gmk has outgrown pure build and
// is now a polyglot orchestrator. The filename follows the tool, matching
// the convention used by Cargo.toml, go.mod, package.json, pyproject.toml.
const ProjectFileName = "gmk.yml"

// ErrNotAGmkProject indicates that walking up from the starting directory
// to the filesystem root found no gmk.yml. Mirrors the shape of git's
// "not a git repository" error: the user is somewhere that isn't part
// of any gmk project, and no recovery is possible without either
// creating a gmk.yml or moving into a directory that has one above it.
var ErrNotAGmkProject = errors.New("not a gmk project (no gmk.yml found)")

// FindProjectFile walks up from startDir toward the filesystem root,
// returning the absolute path of the first gmk.yml encountered. If no
// such file exists at any level, ErrNotAGmkProject is returned wrapped
// with the starting directory so the user can tell what was searched.
//
// startDir may be relative; it is made absolute as the first step. An
// empty startDir is interpreted as os.Getwd().
//
// Stop conditions: only the filesystem root. We do not detect mount
// boundaries (git does, but it's complexity we don't need; if anyone
// ever hits an issue, we'll revisit). The walk terminates when
// filepath.Dir returns the same path as its input — the universal
// "we hit the root" signal that works on both POSIX (where root is "/")
// and Windows (where it's "C:\" or similar).
func FindProjectFile(startDir string) (string, error) {
	if startDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("discover gmk.yml: get cwd: %w", err)
		}
		startDir = cwd
	}

	abs, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("discover gmk.yml: resolve %s: %w", startDir, err)
	}

	dir := abs
	for {
		candidate := filepath.Join(dir, ProjectFileName)
		// We use os.Stat (follows symlinks) rather than os.Lstat: if a user
		// has set up gmk.yml as a symlink to a real file elsewhere — say,
		// to share configuration across a multi-checkout workspace — we
		// want to treat that as a perfectly valid project marker.
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root without finding anything.
			return "", fmt.Errorf("%w: searched from %s up to %s",
				ErrNotAGmkProject, abs, dir)
		}
		dir = parent
	}
}
