// Package cache defines the on-disk layout for gmk's cache directory and
// the path conventions used by every other package that writes to it.
//
// Stage 1 only needs path computation for materialized scripts. Stage 5
// will add IR cache files, Stage 6 adds SQLite databases for probes/ttl/deps.
// All of those will live as new functions in this package, returning paths
// rooted at CacheDir.
//
// On-disk layout (final, see docs/spec.md):
//
//	.gmk-cache/
//	├── ir.msgpack          (S6) serialized IR
//	├── manifest.json       (S6) source-path → ir_hash mapping
//	├── probes.db           (S7) SQLite, configure-mode results
//	├── ttl.db              (S6) SQLite, TTL-mode results
//	├── deps.db             (S8) SQLite, target dep edges
//	└── code/               (S1) materialized scripts
//	    ├── global/         from <lib> includes (S2)
//	    └── local/          from ./ includes
//	        └── build.yml/hello.sh
//
// Stage 1 creates only the code/local/.../{target}.sh files.
package cache

import (
	"path/filepath"
	"strings"
)

// DirName is the cache directory's name relative to the project root.
const DirName = ".gmk-cache"

// CacheDir returns the absolute path to the cache directory for a project
// rooted at projectRoot.
func CacheDir(projectRoot string) string {
	return filepath.Join(projectRoot, DirName)
}

// CodeDir returns the directory where materialized scripts are stored:
//
//	.gmk-cache/code/
func CodeDir(projectRoot string) string {
	return filepath.Join(CacheDir(projectRoot), "code")
}

// ScriptPath returns the absolute on-disk path for a target's materialized
// script.
//
// Path convention:
//
//	<projectRoot>/.gmk-cache/code/local/<rel-source-yml>/<target>.<ext>
//
// Where:
//   - <rel-source-yml> mirrors the directory structure of sourceYAML relative
//     to projectRoot, including the YAML filename itself as a directory
//     component. This means examples/hello/build.yml under projectRoot
//     becomes .gmk-cache/code/local/examples/hello/build.yml/<target>.<ext>
//   - <ext> is chosen from lang via ExtensionForLang.
//
// Stage 2 introduces "global" alongside "local" for path-searched <lib>
// includes; the first path segment under code/ distinguishes them.
func ScriptPath(projectRoot, sourceYAML, targetName, lang string) string {
	rel, err := filepath.Rel(projectRoot, sourceYAML)
	if err != nil || strings.HasPrefix(rel, "..") {
		// Source YAML is outside the project root; this should not happen
		// in normal operation. Fall back to the bare filename so we still
		// produce a path rather than crashing. Stage 2 will tighten this
		// when include-resolution makes the source roots explicit.
		rel = filepath.Base(sourceYAML)
	}
	return filepath.Join(
		CodeDir(projectRoot),
		"local",
		rel, // includes the YAML filename as a directory component
		targetName+ExtensionForLang(lang),
	)
}

// ExtensionForLang maps a language identifier to its conventional file
// extension. Centralized here so materialize and exec agree.
//
// Stage 1 needs only bash; later stages add the rest as their support
// lands. Unknown languages default to ".sh" so the system fails gracefully
// rather than producing extensionless files.
func ExtensionForLang(lang string) string {
	switch lang {
	case "bash", "sh", "":
		return ".sh"
	case "python", "python3":
		return ".py"
	case "perl":
		return ".pl"
	case "ruby":
		return ".rb"
	case "node", "nodejs":
		return ".mjs"
	case "pwsh", "powershell":
		return ".ps1"
	case "cmd":
		return ".cmd"
	default:
		return ".sh"
	}
}

// InterpreterForLang returns the program name used to execute scripts of
// the given language. Used by the exec package.
//
// Stage 1 just dispatches by extension/lang; Stage 10 layers per-language
// invocation conventions (PowerShell needs -File, etc.) on top.
func InterpreterForLang(lang string) string {
	switch lang {
	case "bash", "":
		return "bash"
	case "sh":
		return "sh"
	case "python", "python3":
		return "python3"
	case "perl":
		return "perl"
	case "ruby":
		return "ruby"
	case "node", "nodejs":
		return "node"
	case "pwsh", "powershell":
		return "pwsh"
	case "cmd":
		return "cmd"
	default:
		return "bash"
	}
}
