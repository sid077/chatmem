package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Provision data dir, install pgvector, run initdb, print MCP config",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("init: not implemented yet")
			return nil
		},
	}
}
