package commands

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/AhmedAburady/marina/internal/actions"
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

	rows := actions.ListHosts(cfg)
	t := ui.StyledTable("NAME", "USER", "ADDRESS", "KEY")
	for _, r := range rows {
		t.Row(r.Name, r.User, r.Address, r.Key)
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
  marina hosts add synology root@synology.tail -k ~/.ssh/id_rsa`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, raw := args[0], args[1]
			cfg, err := config.Load(gf.Config)
			if err != nil {
				return err
			}
			if err := actions.AddHost(cfg, gf.Config, name, raw, sshKey); err != nil {
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
			removed, notFound, err := actions.RemoveHosts(cfg, gf.Config, args...)
			if err != nil {
				return err
			}
			for _, n := range removed {
				cmd.Printf("Removed host %q\n", n)
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

			targets := make(map[string]struct{})
			if len(args) == 1 {
				if _, ok := cfg.Hosts[args[0]]; !ok {
					return fmt.Errorf("host %q not found", args[0])
				}
				targets[args[0]] = struct{}{}
			} else {
				for name := range cfg.Hosts {
					targets[name] = struct{}{}
				}
			}
			if len(targets) == 0 {
				cmd.Println("No hosts configured.")
				return nil
			}

			results := make(chan actions.TestResult, len(targets))
			var wg sync.WaitGroup
			for name := range targets {
				wg.Add(1)
				go func(n string) {
					defer wg.Done()
					results <- actions.TestHost(cmd.Context(), cfg, n)
				}(name)
			}
			go func() {
				wg.Wait()
				close(results)
			}()

			var collected []actions.TestResult
			for r := range results {
				collected = append(collected, r)
			}

			t := ui.StyledTable("HOST", "STATUS", "LATENCY")
			for _, r := range collected {
				if r.OK {
					t.Row(r.Host, "ok", r.Latency.Round(time.Millisecond).String())
				} else {
					t.Row(r.Host, r.Err.Error(), "-")
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), t.String())
			return nil
		},
	}
}
