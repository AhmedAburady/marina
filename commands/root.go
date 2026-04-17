// Package commands contains all marina CLI commands.
package commands

import (
	"os"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/tui"
)

// GlobalFlags holds flags shared across all commands.
type GlobalFlags struct {
	Host      string
	Stack     string
	Container string
	Config    string
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
		// When invoked as bare `marina` inside an interactive terminal we
		// launch the full-screen dashboard; otherwise fall back to cobra's
		// default help output so piping (`marina | cat`) keeps working.
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return cmd.Help()
			}
			if !term.IsTerminal(os.Stdout.Fd()) || !term.IsTerminal(os.Stdin.Fd()) {
				return cmd.Help()
			}
			cfg, err := config.Load(gf.Config)
			if err != nil {
				return err
			}
			return tui.Run(cmd.Context(), cfg)
		},
	}

	// -H (capital) avoids the Cobra -h / --help reservation.
	root.PersistentFlags().StringVarP(&gf.Host, "host", "H", "", "Target host (name from config)")
	root.PersistentFlags().StringVarP(&gf.Stack, "stack", "s", "", "Target stack (compose project name)")
	root.PersistentFlags().StringVarP(&gf.Container, "container", "c", "", "Target container name or ID")
	root.PersistentFlags().StringVar(&gf.Config, "config", "", "Config file path (default ~/.config/marina/config.yaml)")
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
		newCheckCmd(&gf),
		newUpdateCmd(&gf),
		newPruneCmd(&gf),
	)

	return root
}
