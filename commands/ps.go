package commands

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/docker"
	"github.com/AhmedAburady/marina/internal/state"
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

	// Collect results; partial failures fall back to cached state, then warn.
	var hasResults bool
	for r := range results {
		if r.err != nil {
			// Attempt to load last-known state from cache.
			store, loadErr := state.Load("")
			if loadErr == nil {
				if snap, ok := store.Hosts[r.host]; ok {
					indicator := cachedIndicator(snap.UpdatedAt)
					containers := convertFromStateContainers(snap.Containers)
					if gf.Plain {
						ui.PrintContainerTablePlain(cmd.OutOrStdout(), r.host+indicator, containers)
					} else {
						ui.PrintContainerTable(cmd.OutOrStdout(), r.host+indicator, containers)
					}
					hasResults = true
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

// cachedIndicator returns a human-readable suffix indicating when the cache
// was last updated, e.g. " (cached from 2h ago)".
func cachedIndicator(updatedAt time.Time) string {
	d := time.Since(updatedAt)
	var age string
	switch {
	case d < time.Minute:
		age = "just now"
	case d < time.Hour:
		mins := int(d.Minutes())
		age = fmt.Sprintf("%dm ago", mins)
	case d < 24*time.Hour:
		hours := int(d.Hours())
		age = fmt.Sprintf("%dh ago", hours)
	default:
		days := int(d.Hours() / 24)
		age = fmt.Sprintf("%dd ago", days)
	}
	return fmt.Sprintf(" (cached from %s)", age)
}

// convertToStateContainers maps Docker container summaries to cache-friendly state types.
func convertToStateContainers(containers []container.Summary) []state.ContainerState {
	result := make([]state.ContainerState, len(containers))
	for i, c := range containers {
		var ports []state.PortState
		for _, p := range c.Ports {
			ports = append(ports, state.PortState{
				PublicPort:  p.PublicPort,
				PrivatePort: p.PrivatePort,
				Type:        p.Type,
			})
		}
		result[i] = state.ContainerState{
			ID:     c.ID,
			Names:  c.Names,
			Image:  c.Image,
			State:  c.State,
			Status: c.Status,
			Labels: c.Labels,
			Ports:  ports,
		}
	}
	return result
}

// convertFromStateContainers maps cached state types back to Docker container summaries.
func convertFromStateContainers(states []state.ContainerState) []container.Summary {
	result := make([]container.Summary, len(states))
	for i, s := range states {
		var ports []container.Port
		for _, p := range s.Ports {
			ports = append(ports, container.Port{
				PublicPort:  p.PublicPort,
				PrivatePort: p.PrivatePort,
				Type:        p.Type,
			})
		}
		result[i] = container.Summary{
			ID:     s.ID,
			Names:  s.Names,
			Image:  s.Image,
			State:  s.State,
			Status: s.Status,
			Labels: s.Labels,
			Ports:  ports,
		}
	}
	return result
}
