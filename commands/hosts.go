package commands

import (
	"fmt"
	"strings"

	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/ui"
	"github.com/spf13/cobra"
)

func newHostsCmd(gf *GlobalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hosts",
		Short: "Manage remote Docker hosts",
		Long:  "List, add, remove, and test remote Docker hosts.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHostsList(cmd, gf)
		},
	}

	cmd.AddCommand(
		newHostsAddCmd(gf),
		newHostsRemoveCmd(gf),
		newHostsTestCmd(gf),
	)

	return cmd
}

func runHostsList(cmd *cobra.Command, gf *GlobalFlags) error {
	cfg, err := config.Load(gf.Config)
	if err != nil {
		return err
	}

	if len(cfg.Hosts) == 0 {
		cmd.Println("No hosts configured. Add one with: marina hosts add <name> <address>")
		return nil
	}

	// Build rows first so we can choose the output format afterwards.
	var rows [][]string
	for name, h := range cfg.Hosts {
		var userDisplay string
		if h.User != "" {
			userDisplay = h.User
		} else if cfg.Settings.Username != "" {
			userDisplay = cfg.Settings.Username + " (global)"
		} else {
			userDisplay = "(none)"
		}
		var keyDisplay string
		if h.SSHKey != "" {
			keyDisplay = h.SSHKey
		} else if cfg.Settings.SSHKey != "" {
			keyDisplay = cfg.Settings.SSHKey + " (global)"
		} else {
			keyDisplay = "(none)"
		}
		rows = append(rows, []string{name, userDisplay, h.Address, keyDisplay})
	}

	if gf.Plain {
		ui.PrintHostTablePlain(cmd.OutOrStdout(), rows)
		return nil
	}

	t := ui.StyledTable("NAME", "USER", "ADDRESS", "KEY")
	for _, row := range rows {
		t.Row(row...)
	}
	fmt.Fprintln(cmd.OutOrStdout(), t.String())
	return nil
}

func newHostsAddCmd(gf *GlobalFlags) *cobra.Command {
	var sshKey string
	cmd := &cobra.Command{
		Use:   "add <name> <address>",
		Short: "Add a remote Docker host",
		Example: `  marina hosts add gmktec 10.0.0.50
  marina hosts add pve-arr user@10.0.0.51
  marina hosts add synology root@synology.tail
  marina hosts add synology root@synology.tail -k ~/.ssh/id_rsa`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, raw := args[0], args[1]

			// Strip accidental ssh:// prefix.
			raw = strings.TrimPrefix(raw, "ssh://")

			// Split on @ to separate optional user from address.
			var user, address string
			if idx := strings.Index(raw, "@"); idx >= 0 {
				user = raw[:idx]
				address = raw[idx+1:]
			} else {
				address = raw
			}

			if address == "" {
				return fmt.Errorf("address must not be empty (got %q)", args[1])
			}

			cfg, err := config.Load(gf.Config)
			if err != nil {
				return err
			}

			if _, exists := cfg.Hosts[name]; exists {
				return fmt.Errorf("host %q already exists; remove it first with: marina hosts remove %s", name, name)
			}

			cfg.Hosts[name] = &config.HostConfig{Address: address, User: user, SSHKey: sshKey}
			if err := config.Save(cfg, gf.Config); err != nil {
				return err
			}

			hostCfg := cfg.Hosts[name]
			cmd.Printf("Added host %q (%s)\n", name, hostCfg.SSHAddress(cfg.Settings.Username))
			return nil
		},
	}
	cmd.Flags().StringVarP(&sshKey, "key", "k", "", "SSH key path for this host")
	return cmd
}

func newHostsRemoveCmd(gf *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "remove <name> [name...]",
		Aliases: []string{"rm"},
		Short:   "Remove one or more remote Docker hosts",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(gf.Config)
			if err != nil {
				return err
			}

			var notFound []string
			var removed []string
			for _, name := range args {
				if _, exists := cfg.Hosts[name]; !exists {
					notFound = append(notFound, name)
					continue
				}
				delete(cfg.Hosts, name)
				removed = append(removed, name)
			}

			if len(removed) > 0 {
				if err := config.Save(cfg, gf.Config); err != nil {
					return err
				}
				for _, name := range removed {
					cmd.Printf("Removed host %q\n", name)
				}
			}

			if len(notFound) > 0 {
				return fmt.Errorf("host(s) not found: %s", strings.Join(notFound, ", "))
			}
			return nil
		},
	}
}

func newHostsTestCmd(gf *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "test [name]",
		Short: "Test SSH connectivity to one or all hosts",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(gf.Config)
			if err != nil {
				return err
			}

			targets := make(map[string]*config.HostConfig)
			if len(args) == 1 {
				name := args[0]
				h, ok := cfg.Hosts[name]
				if !ok {
					return fmt.Errorf("host %q not found", name)
				}
				targets[name] = h
			} else {
				targets = cfg.Hosts
			}

			if len(targets) == 0 {
				cmd.Println("No hosts configured.")
				return nil
			}

			// SSH connectivity testing is implemented in Phase 3 (internal/ssh).
			// For now, report the host addresses that would be tested.
			for name, h := range targets {
				cmd.Printf("%-20s  %s  [SSH test coming in Phase 3]\n", name, h.SSHAddress(cfg.Settings.Username))
			}
			return nil
		},
	}
}
