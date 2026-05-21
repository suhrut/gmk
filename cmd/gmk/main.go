// Command gmk is a build orchestrator that explains itself.
//
// See https://github.com/suhrut/gmk for documentation.
package main

import (
	"fmt"
	"os"

	"github.com/suhrut/gmk/internal/cli"
)

// These are set at build time via -ldflags:
//
//	go build -ldflags "-X main.version=v0.0.1 -X main.commit=$(git rev-parse --short HEAD)"
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	root := cli.Root(cli.BuildInfo{Version: version, Commit: commit})
	if err := root.Execute(); err != nil {
		// Cobra already prints the error; just set the exit code.
		// Using fmt.Fprintln on a nil-checked err keeps formatting consistent
		// for callers that pipe stderr.
		fmt.Fprintln(os.Stderr)
		os.Exit(1)
	}
}
