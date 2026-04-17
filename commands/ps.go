package commands

import (
	"fmt"
	"sort"
	"time"

	"github.com/AhmedAburady/marina/internal/actions"
	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/ui"
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

func runPs(cmd *cobra.Command, gf *GlobalFlags) error {
	cfg, err := config.Load(gf.Config)
	if err != nil {
		return err
	}
	if len(cfg.Hosts) == 0 {
		cmd.Println("No hosts configured. Add one with: marina hosts add <name> <address>")
		return nil
	}

	// Resolve target host set: -H, --all, or interactive selector.
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

	// Single fan-out call — same implementation the TUI uses.
	results := actions.FetchAllHosts(cmd.Context(), cfg, targets)

	var hasResults bool
	// Stable host ordering.
	names := make([]string, 0, len(results))
	for n := range results {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		r := results[name]
		if r.Err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: host %q: %v\n", name, r.Err)
			continue
		}
		label := name
		if r.FromCache {
			label += cachedIndicator(r.CachedAt)
		}
		ui.PrintContainerTable(cmd.OutOrStdout(), label, r.Containers)
		hasResults = true
	}

	if !hasResults {
		cmd.Println("No container data retrieved.")
	}
	return nil
}

// cachedIndicator returns a suffix like " (cached from 2h ago)" for a row
// that fell back to the state snapshot.
func cachedIndicator(updatedAt time.Time) string {
	d := time.Since(updatedAt)
	var age string
	switch {
	case d < time.Minute:
		age = "just now"
	case d < time.Hour:
		age = fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		age = fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		age = fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return fmt.Sprintf(" (cached from %s)", age)
}
