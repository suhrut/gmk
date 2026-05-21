package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	gexec "github.com/suhrut/gmk/internal/exec"
	"github.com/suhrut/gmk/internal/load"
	"github.com/suhrut/gmk/internal/materialize"
)

// newRunCmd returns the `gmk run <target>` command.
//
// Stage 1: loads the YAML file from --file (default ./build.yml), finds
// the named target, materializes its script, and executes it.
//
// Stage 2 will add: dependency resolution (run deps first).
// Stage 4 will add: condition evaluation (skip targets whose when: is false).
// Stage 5 will add: -v / --verbose flags propagating to MYBUILD_V.
// Stage 8 will add: dep tracking writes to deps.db.
func newRunCmd() *cobra.Command {
	var file string

	cmd := &cobra.Command{
		Use:   "run <target>",
		Short: "Run a target from a gmk YAML file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetName := args[0]

			project, err := load.Load(file)
			if err != nil {
				return err
			}

			target, ok := project.Targets[targetName]
			if !ok {
				return fmt.Errorf("target %q not found in %s", targetName, file)
			}

			scriptPath, err := materialize.WriteScript(target, project)
			if err != nil {
				return err
			}

			if err := gexec.Run(scriptPath, gexec.Options{Lang: target.Lang}); err != nil {
				// Surface exit codes from script failures directly to the
				// shell so CI pipelines and chains behave correctly.
				var exitErr *gexec.ExitError
				if errors.As(err, &exitErr) {
					// Set the process exit code via cobra by returning a
					// fmt error; main.go's os.Exit(1) handles propagation.
					// Stage 5 will refine this to preserve the exact exit code.
					return fmt.Errorf("target %q failed: %w", targetName, err)
				}
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "build.yml", "Path to the gmk YAML file")

	return cmd
}
