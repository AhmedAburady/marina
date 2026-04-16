package commands

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"charm.land/huh/v2"
	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/discovery"
	internalssh "github.com/AhmedAburady/marina/internal/ssh"
	"github.com/AhmedAburady/marina/internal/state"
	"github.com/AhmedAburady/marina/internal/ui"
	"github.com/docker/docker/api/types/container"
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
		newStacksPurgeCmd(gf),
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
	stacks     []discovery.Stack
	containers []container.Summary
	err        error
	host       string
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
			results <- hostStacks{host: name, stacks: stacks, containers: containers}
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
			// Attempt to load last-known state from cache.
			store, loadErr := state.Load("")
			if loadErr == nil {
				if snap, ok := store.Hosts[r.host]; ok {
					indicator := cachedIndicator(snap.UpdatedAt)
					containers := convertFromStateContainers(snap.Containers)
					hostCfg := targets[r.host]
					stacks := discovery.GroupByStack(r.host+indicator, containers, hostCfg.Stacks)
					allStacks = append(allStacks, stacks...)
					continue
				}
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: host %q: %v\n", r.host, r.err)
			continue
		}
		// Save live data to state cache (best-effort).
		snapshot := &state.HostSnapshot{
			Containers: convertToStateContainers(r.containers),
		}
		_ = state.SaveHostSnapshot(r.host, snapshot, "")
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

func newStacksPurgeCmd(gf *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "purge",
		Short: "Completely remove a stack: stop containers, delete files, remove from config",
		Long: `Purge fully removes a stack from a remote host:

  1. Runs docker compose down to stop and remove containers
  2. Deletes the stack directory (compose file and all contents)
  3. Removes the stack from marina config (if registered)

This action is IRREVERSIBLE. The stack directory and all its files will be permanently deleted.`,
		Example: `  marina stacks purge -H myhost -s mystack`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if gf.Host == "" {
				return fmt.Errorf("host is required: use -H <host>")
			}
			if gf.Stack == "" {
				return fmt.Errorf("stack is required: use -s <stack>")
			}

			hc, err := resolveHost(gf)
			if err != nil {
				return err
			}

			// Resolve the stack directory.
			dir, err := findStackDir(cmd.Context(), hc, gf.Stack)
			if err != nil {
				return err
			}

			// Show warning and confirm.
			warning := fmt.Sprintf(
				"This will PERMANENTLY:\n\n"+
					"  1. Stop and remove all containers in stack %q\n"+
					"  2. Delete directory %s on %s\n"+
					"  3. Remove %q from marina config (if registered)\n\n"+
					"This cannot be undone. Continue?",
				gf.Stack, dir, gf.Host, gf.Stack,
			)

			var confirmed bool
			if err := huh.NewForm(
				huh.NewGroup(
					huh.NewConfirm().
						Title(warning).
						Value(&confirmed),
				),
			).Run(); err != nil {
				return err
			}
			if !confirmed {
				cmd.Println("Aborted.")
				return nil
			}

			w := cmd.OutOrStdout()
			ctx := cmd.Context()

			// Step 1: docker compose down
			err = execWithSpinner(ctx, w, hc,
				fmt.Sprintf("Stopping stack %s on %s...", gf.Stack, gf.Host),
				fmt.Sprintf("cd %s && docker compose down --remove-orphans", dir),
				fmt.Sprintf("Stack %q stopped and containers removed", gf.Stack),
			)
			if err != nil {
				return fmt.Errorf("compose down failed: %w", err)
			}

			// Step 2: delete the stack directory
			err = execWithSpinner(ctx, w, hc,
				fmt.Sprintf("Deleting %s on %s...", dir, gf.Host),
				fmt.Sprintf("rm -rf %s", dir),
				fmt.Sprintf("Directory %s deleted", dir),
			)
			if err != nil {
				return fmt.Errorf("delete directory failed: %w", err)
			}

			// Step 3: remove from config if registered
			cfg, err := config.Load(gf.Config)
			if err == nil {
				if h, ok := cfg.Hosts[gf.Host]; ok && h.Stacks != nil {
					if _, registered := h.Stacks[gf.Stack]; registered {
						delete(h.Stacks, gf.Stack)
						if err := config.Save(cfg, gf.Config); err != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not update config: %v\n", err)
						} else {
							cmd.Printf("Removed %q from config\n", gf.Stack)
						}
					}
				}
			}

			// Step 4: prune dangling images left behind
			_, _ = internalssh.Exec(ctx, hc.sshCfg, "docker image prune -f")

			cmd.Printf("\nStack %q purged from %s\n", gf.Stack, gf.Host)
			return nil
		},
	}
}
