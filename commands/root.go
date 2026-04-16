// Package commands contains all marina CLI commands.
package commands

import (
	"github.com/spf13/cobra"
)

// GlobalFlags holds flags shared across all commands.
type GlobalFlags struct {
	Host      string
	Stack     string
	Container string
	Config    string
	Plain     bool
	All       bool
}

// NewRootCmd builds the root cobra command and registers all subcommands.
func NewRootCmd(version string) *cobra.Command {
	var gf GlobalFlags

	root := &cobra.Command{
		Use:   "marina",
		Short: "Multi-host Docker management via SSH",
		Long: `Marina manages Docker containers across multiple homelab hosts over SSH.
Zero setup required on target hosts — marina connects via native SSH.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// -H (capital) avoids the Cobra -h / --help reservation.
	root.PersistentFlags().StringVarP(&gf.Host, "host", "H", "", "Target host (name from config)")
	root.PersistentFlags().StringVarP(&gf.Stack, "stack", "s", "", "Target stack (compose project name)")
	root.PersistentFlags().StringVarP(&gf.Container, "container", "c", "", "Target container name or ID")
	root.PersistentFlags().StringVar(&gf.Config, "config", "", "Config file path (default ~/.config/marina/config.yaml)")
	root.PersistentFlags().BoolVar(&gf.Plain, "plain", false, "Plain text output (no borders or styling)")
	root.PersistentFlags().BoolVar(&gf.All, "all", false, "Target all hosts (skip host selector)")

	root.AddCommand(
		newHostsCmd(&gf),
		newConfigCmd(&gf),
		newVersionCmd(version),
		newPsCmd(&gf),
		newStacksCmd(&gf),
		newRestartCmd(&gf),
		newStopCmd(&gf),
		newStartCmd(&gf),
		newPullCmd(&gf),
		newLogsCmd(&gf),
	)

	return root
}
