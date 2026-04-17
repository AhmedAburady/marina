package commands

import (
	"fmt"
	"sort"
	"time"

	"charm.land/huh/v2/spinner"

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

	targets, err := resolveTargets(gf, cfg)
	if err != nil {
		return err
	}

	var results map[string]actions.HostFetchResult
	spinErr := spinner.New().
		Type(spinner.MiniDot).
		Title(fmt.Sprintf("Listing containers on %d host(s)...", len(targets))).
		Action(func() { results = actions.FetchAllHosts(cmd.Context(), cfg, targets) }).
		Run()
	if spinErr != nil {
		return spinErr
	}

	names := make([]string, 0, len(results))
	for n := range results {
		names = append(names, n)
	}
	sort.Strings(names)

	w := cmd.OutOrStdout()
	var printed int
	for _, name := range names {
		r := results[name]
		if r.Err != nil {
			continue // unreachable hosts surface only in `marina hosts`
		}
		label := name
		if r.FromCache {
			label += cachedIndicator(r.CachedAt)
		}
		ui.PrintContainerTable(w, label, r.Containers)
		printed++
	}

	if printed == 0 {
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
