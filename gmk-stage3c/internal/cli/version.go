package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newVersionCmd returns the `gmk version` command.
func newVersionCmd(bi BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and build commit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "gmk %s (%s)\n", bi.Version, bi.Commit)
			return err
		},
	}
}
