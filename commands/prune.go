package commands

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/huh/v2"
	"github.com/AhmedAburady/marina/internal/actions"
	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/strutil"
	"github.com/spf13/cobra"
)

func newPruneCmd(gf *GlobalFlags) *cobra.Command {
	var imagesOnly, imagesAll, volumes, force bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove unused Docker resources on remote hosts",
		Example: `  marina prune -H myhost
  marina prune -H myhost --images-only
  marina prune -H myhost --images-all
  marina prune -H myhost --volumes -y`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPrune(cmd, gf, imagesOnly, imagesAll, volumes, force)
		},
	}

	cmd.Flags().BoolVar(&imagesOnly, "images-only", false, "Prune dangling (untagged) images only")
	cmd.Flags().BoolVar(&imagesAll, "images-all", false, "Prune all unused images (dangling + tagged)")
	cmd.Flags().BoolVar(&volumes, "volumes", false, "Prune unused volumes")
	cmd.Flags().BoolVarP(&force, "force", "y", false, "Skip confirmation prompt")
	return cmd
}

func runPrune(cmd *cobra.Command, gf *GlobalFlags, imagesOnly, imagesAll, volumes, force bool) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	pruneOpts := actions.PruneOptions{
		ImagesOnly: imagesOnly,
		ImagesAll:  imagesAll,
		Volumes:    volumes,
	}
	dockerCmd := actions.PruneCommand(pruneOpts)

	// ── Resolve target hosts ──────────────────────────────────────────────────
	cfg, err := config.Load(gf.Config)
	if err != nil {
		return err
	}
	if len(cfg.Hosts) == 0 {
		cmd.Println("No hosts configured. Add one with: marina hosts add <name> <address>")
		return nil
	}

	hostMap, err := resolveTargets(gf, cfg)
	if err != nil {
		return err
	}

	// Build sorted slice of hostContexts for deterministic output and
	// sequential execution (prune is destructive — no fan-out).
	hostNames := make([]string, 0, len(hostMap))
	for name := range hostMap {
		hostNames = append(hostNames, name)
	}
	sort.Strings(hostNames)

	targets := make([]*hostContext, 0, len(hostNames))
	for _, name := range hostNames {
		targets = append(targets, newHostContext(cfg, name, hostMap[name]))
	}

	// ── Confirmation prompt (unless --force / -y) ─────────────────────────────
	if !force {
		names := make([]string, 0, len(targets))
		for _, hc := range targets {
			names = append(names, hc.name)
		}
		what := "stopped containers, unused networks, dangling images, build cache"
		if imagesAll {
			what = "all unused images (dangling + tagged)"
		} else if imagesOnly {
			what = "dangling images"
		} else if volumes {
			what = "unused volumes"
		}
		title := fmt.Sprintf("Prune %s on %s?", what, strings.Join(names, ", "))

		var confirmed bool
		err := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title(title).
					Value(&confirmed),
			),
		).Run()
		if err != nil {
			return err
		}
		if !confirmed {
			cmd.Println("Aborted.")
			return nil
		}
	}

	// ── Run prune sequentially (destructive operation) ────────────────────────
	var failures []actions.HostStackErr
	for _, hc := range targets {
		err := execWithSpinner(ctx, w, hc,
			fmt.Sprintf("Pruning Docker resources on %s...", hc.name),
			dockerCmd,
			fmt.Sprintf("Pruned Docker resources on %q", hc.name),
		)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: host %q: %s\n", hc.name, strutil.FirstLine(err.Error(), 80))
			failures = append(failures, actions.HostStackErr{Host: hc.name, Err: err})
		}
	}

	return actions.NewApplyErr("prune", failures)
}
