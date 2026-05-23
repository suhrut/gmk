// Helpers shared across the CLI commands (run, call, dryrun, list, doc,
// schema) for resolving which gmk.yml file to operate on.
//
// Every command takes a -f/--file flag. When unset (the default), we
// walk up from the current working directory to find a gmk.yml. When
// set, we use the given path verbatim — this is the escape hatch for
// CI scripts, tests, and the rare case of pointing gmk at a YAML
// outside the conventional project layout.

package cli

import (
	"github.com/suhrut/gmk/internal/ir"
	"github.com/suhrut/gmk/internal/load"
)

// resolveProjectFile returns the gmk.yml path the command should load.
//
// If flagFile is non-empty it is returned as-is (caller used -f
// explicitly). Otherwise we walk up from $PWD via load.FindProjectFile
// and return the discovered path. The returned error wraps
// load.ErrNotAGmkProject when no gmk.yml is found, which the CLI
// surfaces directly to the user.
//
// Why a single function: there are six commands today and more coming
// (inspect, init, list-runs). Centralizing this one decision means
// every command obeys the same convention and any future tweak (a new
// env var, a config-file override) lands in one place.
func resolveProjectFile(flagFile string) (string, error) {
	if flagFile != "" {
		return flagFile, nil
	}
	return load.FindProjectFile("")
}

// loadProject is the convenience wrapper used by commands that just
// need a loaded *ir.Project. It does the resolve step, then hands off
// to load.Load. Errors from either are returned unchanged so callers
// (and ultimately the user) get the most specific error message.
func loadProject(flagFile string) (*ir.Project, error) {
	path, err := resolveProjectFile(flagFile)
	if err != nil {
		return nil, err
	}
	return load.Load(path)
}
