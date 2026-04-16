package commands

import (
	"context"
	"fmt"
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

	// Build the target host map: all hosts or just the one specified by -H.
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
	results := make(chan hostContainers, len(targets))
	var wg sync.WaitGroup

	for name, h := range targets {
		wg.Add(1)
		go func(name, address string) {
			defer wg.Done()
			containers, err := fetchContainers(cmd.Context(), address)
			results <- hostContainers{host: name, containers: containers, err: err}
		}(name, h.SSHAddress(cfg.Settings.Username))
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
		ui.PrintContainerTable(cmd.OutOrStdout(), r.host, r.containers)
		hasResults = true
	}

	if !hasResults {
		cmd.Println("No container data retrieved.")
	}
	return nil
}

func fetchContainers(ctx context.Context, address string) ([]container.Summary, error) {
	c, err := docker.NewClient(ctx, address)
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
