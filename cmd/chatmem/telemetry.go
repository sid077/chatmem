package main

import (
	"fmt"
	"log/slog"
	"os"

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
				log := slog.New(slog.NewTextHandler(os.Stderr, nil))
				client := telemetry.NewClient(st, dataHome(), log, telemetry.Options{Version: version})
				url := client.IngestURL()
				switch {
				case url == "":
					fmt.Println("ingest_url: (unset — pings are collected locally but not shipped)")
				case os.Getenv("CHATMEM_TELEMETRY_URL") != "":
					fmt.Printf("ingest_url: %s (from CHATMEM_TELEMETRY_URL)\n", url)
				default:
					fmt.Printf("ingest_url: %s (baked into this binary)\n", url)
				}
				return nil
			},
		},
		&cobra.Command{
			Use:   "dump",
			Short: "List pending telemetry payloads (unshipped pings) with their sizes",
			RunE: func(cmd *cobra.Command, args []string) error {
				st, err := telemetry.Load(dataHome())
				if err != nil {
					return err
				}
				log := slog.New(slog.NewTextHandler(os.Stderr, nil))
				client := telemetry.NewClient(st, dataHome(), log, telemetry.Options{Version: version})
				files, err := client.PendingFiles()
				if err != nil {
					return err
				}
				if len(files) == 0 {
					fmt.Println("no pending telemetry payloads")
					return nil
				}
				for _, f := range files {
					fi, err := os.Stat(f)
					if err != nil {
						fmt.Printf("  %s  (stat error: %v)\n", f, err)
						continue
					}
					fmt.Printf("  %s  %d bytes  %s\n", f, fi.Size(), fi.ModTime().Format("2006-01-02 15:04:05"))
				}
				return nil
			},
		},
	)
	return cmd
}
