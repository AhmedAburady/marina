package commands

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/huh/v2"
	"github.com/AhmedAburady/marina/internal/config"
	internalssh "github.com/AhmedAburady/marina/internal/ssh"
	"github.com/AhmedAburady/marina/internal/ui"
	"github.com/spf13/cobra"
)

func newPruneCmd(gf *GlobalFlags) *cobra.Command {
	var images, volumes, system, force bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove unused Docker resources on remote hosts",
		Example: `  marina prune -H myhost
  marina prune --all --images
  marina prune -H myhost --volumes -y`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPrune(cmd, gf, images, volumes, system, force)
		},
	}

	cmd.Flags().BoolVar(&images, "images", false, "Prune dangling images only")
	cmd.Flags().BoolVar(&volumes, "volumes", false, "Prune unused volumes only")
	cmd.Flags().BoolVar(&system, "system", false, "Full system prune (default)")
	cmd.Flags().BoolVarP(&force, "force", "y", false, "Skip confirmation prompt")
	return cmd
}

// pruneCommand returns the docker command string based on the chosen flags.
// Defaults to system prune when no specific resource flag is set.
func pruneCommand(images, volumes bool) string {
	if images {
		return "docker image prune -f"
	}
	if volumes {
		return "docker volume prune -f"
	}
	return "docker system prune -f"
}

func runPrune(cmd *cobra.Command, gf *GlobalFlags, images, volumes, system, force bool) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	dockerCmd := pruneCommand(images, volumes)

	// ── Resolve target hosts ──────────────────────────────────────────────────
	var targets []*hostContext

	if gf.Host != "" {
		// Single host via -H flag.
		hc, err := resolveHost(gf)
		if err != nil {
			return err
		}
		targets = []*hostContext{hc}
	} else {
		// Load config for --all or interactive selector.
		cfg, err := config.Load(gf.Config)
		if err != nil {
			return err
		}
		if len(cfg.Hosts) == 0 {
			cmd.Println("No hosts configured. Add one with: marina hosts add <name> <address>")
			return nil
		}

		hostNames := make([]string, 0, len(cfg.Hosts))
		for name := range cfg.Hosts {
			hostNames = append(hostNames, name)
		}
		sort.Strings(hostNames)

		var selectedNames []string

		if gf.All {
			selectedNames = hostNames
		} else {
			// Interactive host selector.
			selected, err := ui.SelectHost(hostNames)
			if err != nil {
				return err
			}
			if selected == "" {
				// "All hosts" chosen in the selector.
				selectedNames = hostNames
			} else {
				selectedNames = []string{selected}
			}
		}

		for _, name := range selectedNames {
			h := cfg.Hosts[name]
			targets = append(targets, &hostContext{
				cfg:  cfg,
				host: h,
				name: name,
				sshCfg: internalssh.Config{
					Address: h.SSHAddress(cfg.Settings.Username),
					KeyPath: h.ResolvedSSHKey(cfg.Settings.SSHKey),
				},
			})
		}
	}

	// ── Confirmation prompt (unless --force / -y) ─────────────────────────────
	if !force {
		names := make([]string, 0, len(targets))
		for _, hc := range targets {
			names = append(names, hc.name)
		}
		title := fmt.Sprintf("Prune resources on %s?", strings.Join(names, ", "))

		var confirmed bool
		err := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title(title).
					Value(&confirmed),
			),
		).Run()
		if err != nil {
			return err
		}
		if !confirmed {
			cmd.Println("Aborted.")
			return nil
		}
	}

	// ── Run prune sequentially (destructive operation) ────────────────────────
	for _, hc := range targets {
		err := execWithSpinner(ctx, w, hc,
			fmt.Sprintf("Pruning Docker resources on %s...", hc.name),
			dockerCmd,
			fmt.Sprintf("Pruned Docker resources on %q", hc.name),
		)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: host %q: %v\n", hc.name, err)
		}
	}

	return nil
}
