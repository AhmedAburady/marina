package commands

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/discovery"
	"github.com/AhmedAburady/marina/internal/ui"
	"github.com/spf13/cobra"
)

func newStacksCmd(gf *GlobalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stacks",
		Short: "List Docker Compose stacks across all hosts",
		Long:  "Show all running Docker Compose projects grouped by stack. Use -H to target a single host.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStacks(cmd, gf)
		},
	}

	cmd.AddCommand(
		newStacksAddCmd(gf),
		newStacksRemoveCmd(gf),
	)

	return cmd
}

func newStacksAddCmd(gf *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "add <name> <dir>",
		Short: "Register a stack (compose project) on a host",
		Long:  "Register a stack so it appears in listings even when all its containers are stopped.",
		Example: `  marina stacks add media /home/user/docker/media -H dockerworld
  marina stacks add monitoring /opt/stacks/monitoring -H myhost`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if gf.Host == "" {
				return fmt.Errorf("host is required: use -H <host>")
			}
			name, dir := args[0], args[1]

			cfg, err := config.Load(gf.Config)
			if err != nil {
				return err
			}

			h, ok := cfg.Hosts[gf.Host]
			if !ok {
				return fmt.Errorf("host %q not found", gf.Host)
			}

			if h.Stacks == nil {
				h.Stacks = make(map[string]string)
			}

			if _, exists := h.Stacks[name]; exists {
				return fmt.Errorf("stack %q already registered on host %q", name, gf.Host)
			}

			h.Stacks[name] = dir
			if err := config.Save(cfg, gf.Config); err != nil {
				return err
			}

			cmd.Printf("Registered stack %q on %q (%s)\n", name, gf.Host, dir)
			return nil
		},
	}
}

func newStacksRemoveCmd(gf *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "remove <name> [name...]",
		Aliases: []string{"rm"},
		Short:   "Unregister a stack from a host",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if gf.Host == "" {
				return fmt.Errorf("host is required: use -H <host>")
			}

			cfg, err := config.Load(gf.Config)
			if err != nil {
				return err
			}

			h, ok := cfg.Hosts[gf.Host]
			if !ok {
				return fmt.Errorf("host %q not found", gf.Host)
			}

			var notFound []string
			var removed []string
			for _, name := range args {
				if _, exists := h.Stacks[name]; !exists {
					notFound = append(notFound, name)
					continue
				}
				delete(h.Stacks, name)
				removed = append(removed, name)
			}

			if len(removed) > 0 {
				if err := config.Save(cfg, gf.Config); err != nil {
					return err
				}
				for _, name := range removed {
					cmd.Printf("Removed stack %q from host %q\n", name, gf.Host)
				}
			}

			if len(notFound) > 0 {
				return fmt.Errorf("stack(s) not found on host %q: %s", gf.Host, strings.Join(notFound, ", "))
			}
			return nil
		},
	}
}

type hostStacks struct {
	stacks []discovery.Stack
	err    error
	host   string
}

func runStacks(cmd *cobra.Command, gf *GlobalFlags) error {
	cfg, err := config.Load(gf.Config)
	if err != nil {
		return err
	}

	if len(cfg.Hosts) == 0 {
		cmd.Println("No hosts configured. Add one with: marina hosts add <name> <address>")
		return nil
	}

	// Determine target host map: -H flag, --all flag, or interactive selector.
	targets := cfg.Hosts
	if gf.Host != "" {
		h, ok := cfg.Hosts[gf.Host]
		if !ok {
			return fmt.Errorf("host %q not found in config", gf.Host)
		}
		targets = map[string]*config.HostConfig{gf.Host: h}
	} else if !gf.All {
		hostNames := make([]string, 0, len(cfg.Hosts))
		for name := range cfg.Hosts {
			hostNames = append(hostNames, name)
		}
		sort.Strings(hostNames)
		selected, err := ui.SelectHost(hostNames)
		if err != nil {
			return err
		}
		if selected != "" {
			targets = map[string]*config.HostConfig{selected: cfg.Hosts[selected]}
		}
	}

	// Fan out to all target hosts in parallel.
	results := make(chan hostStacks, len(targets))
	var wg sync.WaitGroup

	for name, h := range targets {
		wg.Add(1)
		go func(name, address, sshKey string, hostCfg *config.HostConfig) {
			defer wg.Done()
			containers, err := fetchContainers(cmd.Context(), address, sshKey)
			if err != nil {
				results <- hostStacks{host: name, err: fmt.Errorf("connect or list: %w", err)}
				return
			}
			stacks := discovery.GroupByStack(name, containers, hostCfg.Stacks)
			results <- hostStacks{host: name, stacks: stacks}
		}(name, h.SSHAddress(cfg.Settings.Username), h.ResolvedSSHKey(cfg.Settings.SSHKey), h)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// Aggregate all stacks then print a single unified table.
	var allStacks []discovery.Stack
	for r := range results {
		if r.err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: host %q: %v\n", r.host, r.err)
			continue
		}
		allStacks = append(allStacks, r.stacks...)
	}

	if len(allStacks) == 0 {
		cmd.Println("No stacks found.")
		return nil
	}

	if gf.Plain {
		ui.PrintStackTablePlain(cmd.OutOrStdout(), allStacks)
	} else {
		ui.PrintStackTable(cmd.OutOrStdout(), allStacks)
	}
	return nil
}
