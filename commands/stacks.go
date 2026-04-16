package commands

import (
	"fmt"
	"sync"

	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/discovery"
	"github.com/AhmedAburady/marina/internal/ui"
	"github.com/spf13/cobra"
)

func newStacksCmd(gf *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "stacks",
		Short: "List Docker Compose stacks across all hosts",
		Long:  "Show all running Docker Compose projects grouped by stack. Use -H to target a single host.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStacks(cmd, gf)
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

	// Build target host map: all hosts or just the one specified by -H.
	targets := cfg.Hosts
	if gf.Host != "" {
		h, ok := cfg.Hosts[gf.Host]
		if !ok {
			return fmt.Errorf("host %q not found in config", gf.Host)
		}
		targets = map[string]*config.HostConfig{gf.Host: h}
	}

	if len(targets) == 0 {
		cmd.Println("No hosts configured. Add one with: marina hosts add <name> <address>")
		return nil
	}

	// Fan out to all target hosts in parallel.
	results := make(chan hostStacks, len(targets))
	var wg sync.WaitGroup

	for name, h := range targets {
		wg.Add(1)
		go func(name, address string, hostCfg *config.HostConfig) {
			defer wg.Done()
			containers, err := fetchContainers(cmd.Context(), address)
			if err != nil {
				results <- hostStacks{host: name, err: fmt.Errorf("connect or list: %w", err)}
				return
			}
			stacks := discovery.GroupByStack(name, containers, hostCfg.Stacks)
			results <- hostStacks{host: name, stacks: stacks}
		}(name, h.SSHAddress(cfg.Settings.Username), h)
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

	ui.PrintStackTable(cmd.OutOrStdout(), allStacks)
	return nil
}
