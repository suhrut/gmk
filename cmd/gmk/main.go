// Command gmk is a build orchestrator that explains itself.
//
// See https://github.com/suhrut/gmk for documentation.
package main

import (
	"fmt"
	"os"

	"github.com/suhrut/gmk/internal/cli"
	"github.com/suhrut/gmk/internal/template"

	// Stage 3c: blank-import each template engine so its package
	// init() registers itself with template.Default. The "go" engine
	// registers from internal/template itself; jinja from its own
	// subpackage. Adding a new engine later (e.g. mustache via a
	// Stage 3f plugin) becomes "add one blank import line here".
	_ "github.com/suhrut/gmk/internal/template/jinja"
)

// These are set at build time via -ldflags:
//
//	go build -ldflags "-X main.version=v0.0.1 -X main.commit=$(git rev-parse --short HEAD)"
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	// Stage 3c: declare the default template engine. We do this here
	// rather than in any engine's init() because "what's the default"
	// is a top-level policy choice, not an engine's self-knowledge.
	// jinja was chosen for its popularity in the ops/devops world
	// (Ansible, Salt, helm-template) and its richer logic story
	// versus Go's text/template.
	template.SetDefault("jinja")

	root := cli.Root(cli.BuildInfo{Version: version, Commit: commit})
	if err := root.Execute(); err != nil {
		// Cobra already prints the error; just set the exit code.
		// Using fmt.Fprintln on a nil-checked err keeps formatting consistent
		// for callers that pipe stderr.
		fmt.Fprintln(os.Stderr)
		os.Exit(1)
	}
}
