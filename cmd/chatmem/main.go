package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var version = "0.0.1-dev"

func main() {
	root := &cobra.Command{
		Use:           "chatmem",
		Short:         "Local LLM chat history, served over MCP",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(
		newInitCmd(),
		newDaemonCmd(),
		newMCPCmd(),
		newTelemetryCmd(),
		newDoctorCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
