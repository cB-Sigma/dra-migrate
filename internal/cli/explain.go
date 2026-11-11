package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cb-Sigma/dra-migrate/internal/explain"
)

func newExplainCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "explain",
		Short: "Explain what the migration changes and why",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprint(cmd.OutOrStdout(), explain.Text)
		},
	}
}
