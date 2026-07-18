package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newTelemetryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telemetry",
		Short: "Manage anonymous telemetry (enable / disable / status)",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "enable",
			Short: "Enable anonymous telemetry",
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Println("telemetry enable: not implemented yet")
				return nil
			},
		},
		&cobra.Command{
			Use:   "disable",
			Short: "Disable all telemetry",
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Println("telemetry disable: not implemented yet")
				return nil
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Show current telemetry setting and precedence source",
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Println("telemetry status: not implemented yet")
				return nil
			},
		},
	)
	return cmd
}
