package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Stdio MCP shim that proxies to the local daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("mcp: not implemented yet")
			return nil
		},
	}
}
