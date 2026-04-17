package commands

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"
	"github.com/AhmedAburady/marina/internal/actions"
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
			cfg, err := config.Load(gf.Config)
			if err != nil {
				return err
			}
			name, dir := args[0], args[1]
			if err := actions.RegisterStack(cfg, gf.Config, gf.Host, name, dir); err != nil {
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
			removed, notFound, err := actions.UnregisterStacks(cfg, gf.Config, gf.Host, args...)
			if err != nil {
				return err
			}
			for _, n := range removed {
				cmd.Printf("Removed stack %q from host %q\n", n, gf.Host)
			}
			if len(notFound) > 0 {
				return fmt.Errorf("stack(s) not found on host %q: %s", gf.Host, strings.Join(notFound, ", "))
			}
			return nil
		},
	}
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

	targets, err := resolveTargets(gf, cfg)
	if err != nil {
		return err
	}

	// Single shared fan-out. Cache-fallback handled inside actions.
	results := actions.FetchAllHosts(cmd.Context(), cfg, targets)

	var all []discovery.Stack
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
		all = append(all, discovery.GroupByStack(label, r.Containers, cfg.Hosts[name].Stacks)...)
	}

	if len(all) == 0 {
		cmd.Println("No stacks found.")
		return nil
	}
	ui.PrintStackTable(cmd.OutOrStdout(), all)
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

			cfg, err := config.Load(gf.Config)
			if err != nil {
				return err
			}

			plan, err := actions.PurgePlan(cmd.Context(), cfg, gf.Config, gf.Host, gf.Stack)
			if err != nil {
				return err
			}

			// Surface the working directory to the user via the confirm message.
			dir, _ := actions.FindStackDir(cmd.Context(), cfg, gf.Host, gf.Stack)
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
					huh.NewConfirm().Title(warning).Value(&confirmed),
				),
			).Run(); err != nil {
				return err
			}
			if !confirmed {
				cmd.Println("Aborted.")
				return nil
			}

			// Run each step with its own spinner.
			for _, step := range plan {
				title, done := purgeStepTitles(step.Kind, gf.Stack, gf.Host, dir)
				var stepErr error
				spinErr := spinner.New().
					Type(spinner.MiniDot).
					Title(title).
					Action(func() { stepErr = step.Run() }).
					Run()
				if spinErr != nil {
					return spinErr
				}
				if stepErr != nil {
					return fmt.Errorf("%s failed: %w", step.Kind, stepErr)
				}
				cmd.Println(done)
			}
			cmd.Printf("\nStack %q purged from %s\n", gf.Stack, gf.Host)
			return nil
		},
	}
}

// purgeStepTitles returns (spinner title, completion message) for a purge
// step. Kept out-of-band so the CLI can narrate progress without the actions
// package taking on any rendering duties.
func purgeStepTitles(kind, stack, host, dir string) (string, string) {
	switch kind {
	case "compose.down":
		return fmt.Sprintf("Stopping stack %s on %s...", stack, host),
			fmt.Sprintf("Stack %q stopped and containers removed", stack)
	case "dir.rm":
		return fmt.Sprintf("Deleting %s on %s...", dir, host),
			fmt.Sprintf("Directory %s deleted", dir)
	case "image.prune":
		return fmt.Sprintf("Pruning dangling images on %s...", host),
			fmt.Sprintf("Pruned images on %s", host)
	case "config.save":
		return fmt.Sprintf("Updating config..."),
			fmt.Sprintf("Removed %q from config", stack)
	}
	return kind + "...", kind + " done"
}
