package commands

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/docker"
	"github.com/AhmedAburady/marina/internal/ui"
	"github.com/docker/docker/api/types/container"
	"github.com/spf13/cobra"
)

func newPsCmd(gf *GlobalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "List running containers across all hosts",
		Long:  "List all running containers across configured hosts. Use -H to target a single host.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPs(cmd, gf)
		},
	}
}

type hostContainers struct {
	host       string
	containers []container.Summary
	err        error
}

func runPs(cmd *cobra.Command, gf *GlobalFlags) error {
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
	results := make(chan hostContainers, len(targets))
	var wg sync.WaitGroup

	for name, h := range targets {
		wg.Add(1)
		go func(name, address, sshKey string) {
			defer wg.Done()
			containers, err := fetchContainers(cmd.Context(), address, sshKey)
			results <- hostContainers{host: name, containers: containers, err: err}
		}(name, h.SSHAddress(cfg.Settings.Username), h.ResolvedSSHKey(cfg.Settings.SSHKey))
	}

	// Close the channel once all goroutines are done.
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results; partial failures are logged but do not abort.
	var hasResults bool
	for r := range results {
		if r.err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: host %q: %v\n", r.host, r.err)
			continue
		}
		if gf.Plain {
			ui.PrintContainerTablePlain(cmd.OutOrStdout(), r.host, r.containers)
		} else {
			ui.PrintContainerTable(cmd.OutOrStdout(), r.host, r.containers)
		}
		hasResults = true
	}

	if !hasResults {
		cmd.Println("No container data retrieved.")
	}
	return nil
}

func fetchContainers(ctx context.Context, address, sshKey string) ([]container.Summary, error) {
	c, err := docker.NewClient(ctx, address, sshKey)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer c.Close()

	containers, err := c.ListContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	return containers, nil
}
