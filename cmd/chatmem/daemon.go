package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the long-lived local daemon (manages Postgres + serves HTTP MCP)",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("daemon: not implemented yet")
			return nil
		},
	}
}
