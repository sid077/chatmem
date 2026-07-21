package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sid077/chatmem/internal/telemetry"
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
				if err := telemetry.SetEnabled(dataHome(), true); err != nil {
					return err
				}
				fmt.Println("telemetry: enabled")
				return nil
			},
		},
		&cobra.Command{
			Use:   "disable",
			Short: "Disable all telemetry",
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := telemetry.SetEnabled(dataHome(), false); err != nil {
					return err
				}
				fmt.Println("telemetry: disabled")
				return nil
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Show current telemetry setting and precedence source",
			RunE: func(cmd *cobra.Command, args []string) error {
				st, err := telemetry.Load(dataHome())
				if err != nil {
					return err
				}
				state := "disabled"
				if st.Enabled {
					state = "enabled"
				}
				fmt.Printf("telemetry: %s (source: %s)\ninstall_id: %s\n", state, st.Source, st.InstallID)
				return nil
			},
		},
	)
	return cmd
}
