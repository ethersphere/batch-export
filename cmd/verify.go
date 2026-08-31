package cmd

import (
	"fmt"

	"github.com/ethersphere/batch-export/pkg/verify"
	"github.com/spf13/cobra"
)

func (c *command) initVerifyCmd() error {
	var (
		oldFile string
		newFile string
	)

	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify that a refreshed snapshot extends the one it was resumed from",
		Long: `Verifies that --new begins with the entire content of --old, byte for byte,
and that every entry after it is strictly newer. Prints the new snapshot's last
block number in decimal on stdout; any failure exits non-zero with empty stdout.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := verify.Verify(oldFile, newFile)
			if err != nil {
				return err
			}

			if result.OldTruncated {
				c.log.Warning("old snapshot ends in an interrupted write; the truncated tail was excluded from the comparison", "oldFile", oldFile)
			}
			c.log.Info("snapshot verified", "oldFile", oldFile, "newFile", newFile, "appended", result.Appended, "lastBlock", result.LastBlock)

			_, err = fmt.Fprintln(cmd.OutOrStdout(), result.LastBlock)
			return err
		},
	}

	cmd.Flags().StringVar(&oldFile, "old", "", "Snapshot the export resumed from (.ndjson, .gz or .gzip)")
	cmd.Flags().StringVar(&newFile, "new", "", "Freshly exported snapshot to check (.ndjson, .gz or .gzip)")
	for _, name := range []string{"old", "new"} {
		if err := cmd.MarkFlagRequired(name); err != nil {
			return err
		}
	}

	c.root.AddCommand(cmd)

	return nil
}
