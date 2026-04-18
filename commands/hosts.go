package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"

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
		newHostsEditCmd(gf),
		newHostsRemoveCmd(gf),
		newHostsTestCmd(gf),
		newHostsDisableCmd(gf),
		newHostsEnableCmd(gf),
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
	t := ui.StyledTable("NAME", "USER", "ADDRESS", "KEY", "STATUS")
	for _, r := range rows {
		status := "enabled"
		if r.Disabled {
			status = "disabled"
		}
		t.Row(r.Name, r.User, r.Address, r.Key, status)
	}
	fmt.Fprintln(cmd.OutOrStdout(), t.String())
	return nil
}

func newHostsDisableCmd(gf *GlobalFlags) *cobra.Command {
	return newHostsToggleCmd(gf, "disable", "Skip a host from fan-out operations without deleting it", true)
}

func newHostsEnableCmd(gf *GlobalFlags) *cobra.Command {
	return newHostsToggleCmd(gf, "enable", "Re-enable a previously disabled host", false)
}

func newHostsToggleCmd(gf *GlobalFlags, verb, short string, disabled bool) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " <name>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(gf.Config)
			if err != nil {
				return err
			}
			if err := actions.SetHostDisabled(cfg, gf.Config, args[0], disabled); err != nil {
				return err
			}
			if disabled {
				cmd.Printf("Disabled host %q\n", args[0])
			} else {
				cmd.Printf("Enabled host %q\n", args[0])
			}
			return nil
		},
	}
}

func newHostsAddCmd(gf *GlobalFlags) *cobra.Command {
	var (
		sshKey  string
		port    int
		trust   bool
		noTrust bool
	)
	cmd := &cobra.Command{
		Use:   "add <name> <address>",
		Short: "Add a remote Docker host",
		Example: `  marina hosts add gmktec 10.0.0.50
  marina hosts add pve-arr user@10.0.0.51
  marina hosts add synology root@synology.tail -k ~/.ssh/id_rsa
  marina hosts add bastion user@10.0.0.70 -p 2222
  marina hosts add ci-box ci@10.0.0.60 --trust`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, raw := args[0], args[1]
			if port < 0 || port > 65535 {
				return fmt.Errorf("--port must be 0–65535, got %d", port)
			}
			// If --port is supplied and the address doesn't already carry
			// one, tack it on. Lets users write `add myhost user@host -p 2222`
			// without remembering the host:port syntax.
			if port > 0 {
				parsed, err := actions.ParseAddress(raw)
				if err != nil {
					return err
				}
				if _, existing := actions.SplitAddressPort(parsed.Address); existing == "" {
					raw = actions.JoinUserAddress(parsed.User, actions.JoinHostPort(parsed.Address, port))
				}
			}
			cfg, err := config.Load(gf.Config)
			if err != nil {
				return err
			}
			if err := actions.AddHost(cfg, gf.Config, name, raw, sshKey); err != nil {
				return err
			}
			hostCfg := cfg.Hosts[name]
			cmd.Printf("Added host %q (%s)\n", name, hostCfg.SSHAddress(cfg.Settings.Username))

			// Already trusted? Skip the prompt — nothing to do. Lets users
			// re-add a host they already accepted without being asked again.
			if alreadyTrusted, _ := actions.IsHostTrusted(cmd.Context(), cfg, name); alreadyTrusted {
				return nil
			}

			// Trust decision: --trust / --no-trust short-circuit the prompt
			// (use these in scripts). Otherwise fall back to an interactive
			// yes/no — the non-interactive equivalent of OpenSSH's first-
			// connection "Are you sure you want to continue connecting?".
			accept := trust
			if !trust && !noTrust {
				if err := huh.NewForm(
					huh.NewGroup(
						huh.NewConfirm().
							Title(fmt.Sprintf("Trust SSH host key for %q?", name)).
							Description(fmt.Sprintf("Adds %s to ~/.ssh/known_hosts so marina can connect without\n"+
								"the interactive first-time prompt. Declining leaves the host untrusted —\n"+
								"run `marina hosts test %s` after you've accepted the key manually.",
								hostCfg.Address, name)).
							Affirmative("Yes, trust").
							Negative("No, skip").
							Value(&accept),
					),
				).Run(); err != nil {
					if errors.Is(err, huh.ErrUserAborted) {
						return nil // user cancelled — clean exit
					}
					return err // non-TTY, I/O failure, etc.
				}
			}
			if !accept {
				return nil
			}

			trustCtx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			if err := actions.TrustHost(trustCtx, cfg, name); err != nil {
				cmd.PrintErrf("Trust failed: %s\n", err)
				return nil
			}
			cmd.Printf("Trusted host %q\n", name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&sshKey, "key", "k", "", "SSH key path for this host")
	cmd.Flags().IntVarP(&port, "port", "p", 0, "SSH port (default 22). Ignored if the address already contains :port")
	cmd.Flags().BoolVar(&trust, "trust", false, "Trust the host key without prompting (scripts/CI)")
	cmd.Flags().BoolVar(&noTrust, "no-trust", false, "Skip trusting the host key entirely")
	return cmd
}

func newHostsEditCmd(gf *GlobalFlags) *cobra.Command {
	var (
		address string
		user    string
		sshKey  string
		port    int
		enable  bool
		disable bool
	)
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Edit a remote Docker host",
		Long: `Edit an existing host's address, user, SSH key, port, or enable/disable state.
Only the flags you pass are changed; the rest are left alone.`,
		Example: `  marina hosts edit gmktec --address 10.0.0.51
  marina hosts edit pve-arr --user admin
  marina hosts edit synology -k ~/.ssh/id_ed25519
  marina hosts edit bastion -p 2222
  marina hosts edit ci-box --disable`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if enable && disable {
				return fmt.Errorf("--enable and --disable are mutually exclusive")
			}
			if cmd.Flags().Changed("port") && (port < 0 || port > 65535) {
				return fmt.Errorf("--port must be 0–65535, got %d", port)
			}

			cfg, err := config.Load(gf.Config)
			if err != nil {
				return err
			}
			h, ok := cfg.Hosts[name]
			if !ok {
				return fmt.Errorf("host %q not found", name)
			}

			// Flag semantics: "not set" means "keep current value"; an
			// explicitly empty string means "clear it". cobra's Changed()
			// is the only way to distinguish the two.
			curHost, curPortStr := actions.SplitAddressPort(h.Address)

			newUser := h.User
			if cmd.Flags().Changed("user") {
				newUser = user
			}
			newHost := curHost
			if cmd.Flags().Changed("address") {
				newHost = address
			}
			newPortStr := curPortStr
			if cmd.Flags().Changed("port") {
				if port == 0 {
					newPortStr = ""
				} else {
					newPortStr = strconv.Itoa(port)
				}
			}
			newKey := h.SSHKey
			if cmd.Flags().Changed("key") {
				newKey = sshKey
			}
			newDisabled := h.Disabled
			if enable {
				newDisabled = false
			}
			if disable {
				newDisabled = true
			}

			// Recombine host + port back into the canonical Address form.
			// newPortStr is either "" (default SSH port) or a validated int.
			newAddress := newHost
			if newPortStr != "" {
				newAddress = newHost + ":" + newPortStr
			}
			if err := actions.UpdateHost(cfg, gf.Config, name, actions.JoinUserAddress(newUser, newAddress), newKey, newDisabled); err != nil {
				return err
			}
			cmd.Printf("Updated host %q\n", name)
			return nil
		},
	}
	cmd.Flags().StringVarP(&address, "address", "a", "", "Host address (host only — use --port for the port)")
	cmd.Flags().IntVarP(&port, "port", "p", 0, "SSH port (0 = default 22)")
	cmd.Flags().StringVarP(&user, "user", "u", "", "SSH user override (empty string clears)")
	cmd.Flags().StringVarP(&sshKey, "key", "k", "", "SSH key path (empty string clears)")
	cmd.Flags().BoolVar(&enable, "enable", false, "Enable this host")
	cmd.Flags().BoolVar(&disable, "disable", false, "Disable this host")
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

			var targets []string
			if len(args) == 1 {
				if _, ok := cfg.Hosts[args[0]]; !ok {
					return fmt.Errorf("host %q not found", args[0])
				}
				targets = append(targets, args[0])
			} else {
				for name := range actions.EnabledHosts(cfg) {
					targets = append(targets, name)
				}
			}
			if len(targets) == 0 {
				cmd.Println("No hosts configured.")
				return nil
			}

			var collected []actions.TestResult
			spinErr := spinner.New().
				Type(spinner.MiniDot).
				Title(fmt.Sprintf("Testing %d host(s)...", len(targets))).
				Action(func() {
					// Use the shared FanOut helper — same concurrency
					// primitive as actions.FetchAllHosts and checks.
					for r := range actions.FanOut(cmd.Context(), targets, 0,
						func(ctx context.Context, name string) actions.TestResult {
							return actions.TestHost(ctx, cfg, name)
						}) {
						collected = append(collected, r)
					}
				}).
				Run()
			if spinErr != nil {
				return spinErr
			}

			t := ui.StyledTable("HOST", "STATUS", "LATENCY")
			for _, r := range collected {
				if r.OK {
					t.Row(r.Host, "ok", r.Latency.Round(time.Millisecond).String())
				} else {
					t.Row(r.Host, "unreachable", "-")
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), t.String())
			return nil
		},
	}
}
