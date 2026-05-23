// Package cli implements gmk's command-line interface using cobra.
//
// All commands use constructor style: each subcommand is built by a
// newXxxCmd() function. Root() composes them into the root command tree.
// This is more testable than the init()+package-global pattern because
// tests can construct fresh command trees without sharing state.
package cli

import (
	"github.com/spf13/cobra"
)

// BuildInfo carries version and commit metadata injected at build time
// via -ldflags. It's passed into Root() rather than read from package-level
// globals so tests can supply their own values.
type BuildInfo struct {
	Version string
	Commit  string
}

// Root constructs the root cobra command with all subcommands wired up.
//
// Adding a new subcommand: create internal/cli/<name>.go with a
// newXxxCmd() function, then add a r.AddCommand(...) call here.
func Root(bi BuildInfo) *cobra.Command {
	r := &cobra.Command{
		Use:   "gmk",
		Short: "A build orchestrator that explains itself",
		Long: `gmk is a declarative task orchestrator with environment binding.

It runs YAML-defined targets across multiple languages with materialized
scripts on disk, autoconf-style probes, and explicit semantics throughout.

See https://github.com/suhrut/gmk for the spec and examples.`,
		SilenceUsage:  true, // Don't print usage on every error
		SilenceErrors: false,
	}

	r.AddCommand(newVersionCmd(bi))
	r.AddCommand(newRunCmd())
	r.AddCommand(newDryRunCmd())
	r.AddCommand(newCallCmd())   // Stage 3b: gmk call <function>
	r.AddCommand(newListCmd())   // Stage 3b: gmk list
	r.AddCommand(newDocCmd())    // Stage 3b: gmk doc <name>
	r.AddCommand(newSchemaCmd()) // Stage 3b: gmk schema

	return r
}
