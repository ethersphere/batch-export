package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/ethersphere/bee/v2/pkg/log"
	"github.com/spf13/cobra"
)

type command struct {
	root      *cobra.Command
	verbosity string
	log       log.Logger
}

func (c *command) Execute(ctx context.Context) (err error) {
	return c.root.ExecuteContext(ctx)
}

func Execute(ctx context.Context) (err error) {
	c, err := newCommand()
	if err != nil {
		return err
	}
	return c.Execute(ctx)
}

func newCommand() (c *command, err error) {
	c = &command{
		root: &cobra.Command{
			Use:           "batch-export",
			Short:         "A tool to track logs of a swarm node",
			Long:          "A tool to track logs of a swarm node",
			SilenceErrors: true,
			SilenceUsage:  true,
			PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
				var err error
				c.log, err = newLogger(c.verbosity)
				if err != nil {
					return fmt.Errorf("failed to create logger: %w", err)
				}
				return nil
			},
		},
	}

	c.root.PersistentFlags().StringVarP(&c.verbosity, "verbosity", "v", "info", "Log verbosity (silent, error, warn, info, debug)")

	if err := c.initExportCmd(); err != nil {
		return nil, err
	}

	return c, nil
}

func newLogger(verbosity string) (logger log.Logger, err error) {
	var level log.Level
	switch strings.ToLower(verbosity) {
	case "0", "silent":
		level = log.VerbosityNone
	case "1", "error":
		level = log.VerbosityError
	case "2", "warn":
		level = log.VerbosityWarning
	case "3", "info":
		level = log.VerbosityInfo
	case "4", "debug":
		level = log.VerbosityDebug
	default:
		return nil, fmt.Errorf("invalid verbosity level: %s", verbosity)
	}

	return log.NewLogger("batch-export", log.WithVerbosity(level), log.WithTimestamp()).Register(), nil
}
