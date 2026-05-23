package load

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SearchPath returns the ordered list of directories to search when
// resolving "<lib>"-style includes.
//
// Order (first match wins):
//  1. <projectRoot>/.gmk/lib                 — project-local libs, can be
//     vendored and git-committed to make a project self-sufficient
//  2. $GMK_PATH (colon-separated, in declaration order) — user override
//  3. $HOME/.gmk/lib                         — per-user library
//  4. /usr/local/share/gmk/lib               — site-installed library
//  5. /usr/share/gmk/lib                     — distro-packaged library
//
// projectRoot is the absolute directory of the top-level YAML being loaded.
// Pass "" to skip the project-local entry (useful when there is no project
// context, e.g. tests).
//
// Stage 11 will ship a built-in standard library that's resolved as if it
// were at /usr/share/gmk/lib; for now those directories are searched but
// may not exist.
func SearchPath(projectRoot string) []string {
	var out []string

	// 1. Project-local
	if projectRoot != "" {
		out = append(out, filepath.Join(projectRoot, ".gmk", "lib"))
	}

	// 2. $GMK_PATH
	if envPath := os.Getenv("GMK_PATH"); envPath != "" {
		for _, p := range filepath.SplitList(envPath) {
			if p != "" {
				out = append(out, p)
			}
		}
	}

	// 3. $HOME/.gmk/lib
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out, filepath.Join(home, ".gmk", "lib"))
	}

	// 4. /usr/local/share/gmk/lib
	out = append(out, "/usr/local/share/gmk/lib")

	// 5. /usr/share/gmk/lib
	out = append(out, "/usr/share/gmk/lib")

	return out
}

// ResolveLibInclude resolves a "<lib>"-style include spec to an absolute
// file path by searching SearchPath(projectRoot) in order.
//
// The spec is the unwrapped library name: for the YAML directive
// `includes: ["<probes/system.yml>"]`, pass "probes/system.yml".
//
// Returns ErrIncludeNotFound (wrapped with searched paths) if no
// directory in the search path contained the file.
func ResolveLibInclude(spec, projectRoot string) (string, error) {
	if spec == "" {
		return "", fmt.Errorf("ResolveLibInclude: empty spec")
	}
	if filepath.IsAbs(spec) {
		// Defensive: <lib> specs are not supposed to be absolute. If a
		// user wraps an absolute path in <>, treat it as IsAbs to avoid
		// silently producing a different path via search.
		return "", fmt.Errorf("library include %q must not be absolute (use local include syntax for absolute paths)", spec)
	}

	dirs := SearchPath(projectRoot)
	var searched []string
	for _, d := range dirs {
		candidate := filepath.Join(d, spec)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
		searched = append(searched, d)
	}

	return "", fmt.Errorf("%w: %q not found in search path:\n  %s",
		ErrIncludeNotFound, spec, strings.Join(searched, "\n  "))
}

// IsLibInclude reports whether a raw includes: entry uses the "<...>"
// library syntax. Returns the unwrapped spec on true.
//
// Used by load.Load to dispatch between local and library resolution.
func IsLibInclude(raw string) (spec string, isLib bool) {
	if len(raw) >= 2 && raw[0] == '<' && raw[len(raw)-1] == '>' {
		return raw[1 : len(raw)-1], true
	}
	return "", false
}

// IsLocalInclude reports whether a raw includes: entry uses local-path
// syntax. Returns false for anything ambiguous (which the caller must
// reject as a parse error).
//
// Local include syntax is any of:
//
//	"./relative/path.yml"
//	"../sibling/path.yml"
//	"/absolute/path.yml"
func IsLocalInclude(raw string) bool {
	if raw == "" {
		return false
	}
	switch {
	case strings.HasPrefix(raw, "./"):
		return true
	case strings.HasPrefix(raw, "../"):
		return true
	case strings.HasPrefix(raw, "/"):
		return true
	}
	return false
}
