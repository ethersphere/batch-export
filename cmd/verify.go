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
		Long: `Verifies that the snapshot at --new holds everything the snapshot at --old does,
byte for byte and in order, followed only by newer entries. On success the new
snapshot's last block number is printed to stdout in decimal; any failure exits
non-zero with nothing on stdout.`,
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
