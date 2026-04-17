package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"charm.land/huh/v2/spinner"
	"charm.land/lipgloss/v2"
	"github.com/AhmedAburady/marina/internal/config"
	notifyPkg "github.com/AhmedAburady/marina/internal/notify"
	"github.com/AhmedAburady/marina/internal/registry"
	internalssh "github.com/AhmedAburady/marina/internal/ssh"
	"github.com/AhmedAburady/marina/internal/ui"
	"github.com/spf13/cobra"
)

// updateAvailableStyle highlights rows that have an update ready.
var updateAvailableStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)

func newCheckCmd(gf *GlobalFlags) *cobra.Command {
	var notify bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check for available image updates",
		Example: `  marina check
  marina check -H myhost
  marina check --all
  marina check --notify`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheckCmd(cmd, gf, notify)
		},
	}
	cmd.Flags().BoolVar(&notify, "notify", false, "Send notification with results (requires Gotify config)")
	return cmd
}

func newUpdateCmd(gf *GlobalFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Apply available image updates",
		Example: `  marina update --all --yes`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdateApply(cmd, gf, yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm updates without prompting (required)")
	return cmd
}

// runCheckCmd resolves hosts, probes registries through the shared
// registry.BuildChecker, and renders the bordered update table. `--notify`
// sends a Gotify summary instead. The interactive selector lives in the
// dashboard's Updates screen; this cobra command is strictly non-interactive.
func runCheckCmd(cmd *cobra.Command, gf *GlobalFlags, notify bool) error {
	cfg, err := config.Load(gf.Config)
	if err != nil {
		return err
	}
	if len(cfg.Hosts) == 0 {
		cmd.Println("No hosts configured. Add one with: marina hosts add <name> <address>")
		return nil
	}
	targets, err := resolveTargets(cfg, gf)
	if err != nil {
		return err
	}

	var results []registry.Result
	var collectErr error
	spinErr := spinner.New().
		Type(spinner.MiniDot).
		Title("Checking for updates...").
		Action(func() { results, collectErr = runChecks(cmd.Context(), cfg, targets) }).
		Run()
	if spinErr != nil {
		return spinErr
	}
	if collectErr != nil {
		return collectErr
	}

	if notify {
		return sendNotifySummary(cmd, cfg, results)
	}

	if len(results) == 0 {
		cmd.Println("No containers found.")
		return nil
	}

	// Group rows by host so we can render one bordered table per host —
	// matches the visual cadence of `marina ps` / `marina stacks`.
	sort.Slice(results, func(i, j int) bool {
		if results[i].Host != results[j].Host {
			return results[i].Host < results[j].Host
		}
		if results[i].Stack != results[j].Stack {
			return results[i].Stack < results[j].Stack
		}
		return results[i].Container < results[j].Container
	})

	byHost := make(map[string][]registry.Result)
	for _, r := range results {
		byHost[r.Host] = append(byHost[r.Host], r)
	}
	hosts := make([]string, 0, len(byHost))
	for h := range byHost {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)

	w := cmd.OutOrStdout()
	for _, h := range hosts {
		printUpdateTable(w, h, byHost[h])
	}
	return nil
}

// runUpdateApply requires --yes, gathers check results once, then pulls and
// recreates any stack whose images have updates.
func runUpdateApply(cmd *cobra.Command, gf *GlobalFlags, yes bool) error {
	if !yes {
		return fmt.Errorf("--yes flag is required to apply updates (use: marina update --yes)")
	}
	cfg, err := config.Load(gf.Config)
	if err != nil {
		return err
	}
	if len(cfg.Hosts) == 0 {
		cmd.Println("No hosts configured. Add one with: marina hosts add <name> <address>")
		return nil
	}
	targets, err := resolveTargets(cfg, gf)
	if err != nil {
		return err
	}

	var results []registry.Result
	var collectErr error
	spinErr := spinner.New().
		Type(spinner.MiniDot).
		Title("Checking for updates...").
		Action(func() { results, collectErr = runChecks(cmd.Context(), cfg, targets) }).
		Run()
	if spinErr != nil {
		return spinErr
	}
	if collectErr != nil {
		return collectErr
	}

	// Group updatable results by (host, stack) and pick a dir (first non-empty).
	type stackKey struct{ Host, Stack string }
	stackDirs := make(map[stackKey]string)
	var keys []stackKey
	for _, r := range results {
		if !r.HasUpdate {
			continue
		}
		k := stackKey{Host: r.Host, Stack: r.Stack}
		if _, seen := stackDirs[k]; !seen {
			keys = append(keys, k)
			stackDirs[k] = r.Dir
		} else if stackDirs[k] == "" && r.Dir != "" {
			stackDirs[k] = r.Dir
		}
	}
	if len(keys) == 0 {
		cmd.Println("All images are up-to-date.")
		return nil
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Host != keys[j].Host {
			return keys[i].Host < keys[j].Host
		}
		return keys[i].Stack < keys[j].Stack
	})

	w := cmd.OutOrStdout()
	for _, k := range keys {
		dir := stackDirs[k]
		hostCfg := cfg.Hosts[k.Host]
		hc := &hostContext{
			cfg:  cfg,
			host: hostCfg,
			name: k.Host,
			sshCfg: internalssh.Config{
				Address: hostCfg.SSHAddress(cfg.Settings.Username),
				KeyPath: hostCfg.ResolvedSSHKey(cfg.Settings.SSHKey),
			},
		}
		title := fmt.Sprintf("Updating stack %q on %q...", k.Stack, k.Host)
		doneMsg := fmt.Sprintf("Updated stack %q on %q.", k.Stack, k.Host)
		if dir != "" {
			command := fmt.Sprintf("cd %s && docker compose pull && docker compose up -d", dir)
			if err := execWithSpinner(cmd.Context(), w, hc, title, command, doneMsg); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s/%s: %v\n", k.Host, k.Stack, err)
			}
		} else {
			commandFmt := "cd %s && docker compose pull && docker compose up -d"
			if err := execStackWithSpinner(cmd.Context(), w, hc, k.Stack, title, commandFmt, doneMsg); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s/%s: %v\n", k.Host, k.Stack, err)
			}
		}
	}

	// Optional image prune after all stacks are updated — once per unique host.
	if cfg.Settings.PruneAfterUpdate {
		pruned := make(map[string]bool)
		for _, k := range keys {
			if pruned[k.Host] {
				continue
			}
			pruned[k.Host] = true
			hostCfg := cfg.Hosts[k.Host]
			hc := &hostContext{
				cfg:  cfg,
				host: hostCfg,
				name: k.Host,
				sshCfg: internalssh.Config{
					Address: hostCfg.SSHAddress(cfg.Settings.Username),
					KeyPath: hostCfg.ResolvedSSHKey(cfg.Settings.SSHKey),
				},
			}
			title := fmt.Sprintf("Pruning old images on %q...", k.Host)
			doneMsg := fmt.Sprintf("Pruned old images on %q.", k.Host)
			if err := execWithSpinner(cmd.Context(), w, hc, title, "docker image prune -f", doneMsg); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: prune on %s: %v\n", k.Host, err)
			}
		}
	}
	return nil
}

// ── Check orchestration (single shared implementation) ───────────────────────

// runChecks gathers update candidates once via registry.BuildChecker, then
// fires the per-candidate registry HTTP probes concurrently. The gather step
// inside BuildChecker already uses docker.NewClient (MaxConnsPerHost: 1), so
// per-host Docker inspects serialize through one SSH pipe automatically —
// we don't add our own throttling there. The HTTP registry calls are plain
// network calls with an in-closure dedup for shared images.
func runChecks(
	ctx context.Context,
	cfg *config.Config,
	targets map[string]*config.HostConfig,
) ([]registry.Result, error) {
	candidates, check, cache, err := registry.BuildChecker(ctx, cfg, targets)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	results := make([]registry.Result, len(candidates))
	var wg sync.WaitGroup
	for i, c := range candidates {
		wg.Add(1)
		go func(idx int, cand registry.Candidate) {
			defer wg.Done()
			results[idx] = check(ctx, cand)
		}(i, c)
	}
	wg.Wait()

	// Persist the registry digest cache so the next run starts warm.
	_ = registry.SaveCache(cache, "")
	return results, nil
}

// sendNotifySummary sends a Gotify ping summarising the run. Fires even
// when every image is current so a scheduled `marina check --notify` run
// produces a proof-of-life heartbeat instead of silent success — without
// it, a broken cron / SSH / registry path looks identical to "all good".
func sendNotifySummary(cmd *cobra.Command, cfg *config.Config, results []registry.Result) error {
	var available int
	var msg strings.Builder
	for _, r := range results {
		if !r.HasUpdate {
			continue
		}
		available++
		stack := r.Stack
		if stack == "" || stack == "-" {
			stack = "(no stack)"
		}
		// host · stack · container → image
		// Picks delimiters Gotify renders cleanly on mobile: middle-dot
		// groups the "where", arrow separates from the "what".
		fmt.Fprintf(&msg, "• %s · %s · %s → %s\n", r.Host, stack, r.Container, r.Image)
	}

	gotifyCfg := notifyPkg.GotifyConfig{
		URL:      cfg.Notify.Gotify.URL,
		Token:    cfg.Notify.Gotify.Token,
		Priority: cfg.Notify.Gotify.Priority,
	}

	var title, body string
	if available == 0 {
		title = "Marina: all up to date"
		body = fmt.Sprintf("Checked %d container(s). No updates available.", len(results))
	} else {
		title = fmt.Sprintf("Marina: %d update(s) available", available)
		body = msg.String()
	}

	if err := notifyPkg.SendGotify(cmd.Context(), gotifyCfg, title, body); err != nil {
		return fmt.Errorf("send notification: %w", err)
	}
	if available == 0 {
		cmd.Printf("Notification sent: all %d container(s) up to date\n", len(results))
	} else {
		cmd.Printf("Notification sent: %d update(s) available\n", available)
	}
	return nil
}

// resolveTargets builds the target host map following the -H / --all /
// interactive selector pattern shared across marina commands.
func resolveTargets(cfg *config.Config, gf *GlobalFlags) (map[string]*config.HostConfig, error) {
	targets := cfg.Hosts
	if gf.Host != "" {
		h, ok := cfg.Hosts[gf.Host]
		if !ok {
			return nil, fmt.Errorf("host %q not found in config", gf.Host)
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
			return nil, err
		}
		if selected != "" {
			targets = map[string]*config.HostConfig{selected: cfg.Hosts[selected]}
		}
	}
	return targets, nil
}

// ── Display helpers ──────────────────────────────────────────────────────────

// statusText returns the human-readable status cell value for a Result.
// Status strings come from registry.CheckStatus.String(); Error patterns are
// used to surface rate-limit / local-build / connection failures distinctly.
func statusText(r registry.Result) string {
	if r.HasUpdate {
		return "update available"
	}
	if r.Error == nil {
		return "up-to-date"
	}
	msg := r.Error.Error()
	switch {
	case strings.Contains(msg, "TOOMANYREQUESTS"):
		return "rate limited"
	case strings.Contains(msg, "no registry digest"):
		return "local build"
	case strings.Contains(msg, "inspect container"),
		strings.Contains(msg, "error during connect"),
		strings.Contains(msg, "Connection reset"):
		return "connection error"
	}
	if len(msg) > 40 {
		msg = msg[:40] + "…"
	}
	return "check failed: " + msg
}

// printUpdateTable renders a styled lipgloss table for a single host.
func printUpdateTable(w interface{ Write([]byte) (int, error) }, host string, rows []registry.Result) {
	hostHeaderStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	fmt.Fprintf(w, "%s\n", hostHeaderStyle.Render("▸ "+host))
	t := ui.StyledTable("STACK", "CONTAINER", "IMAGE", "STATUS")
	for _, r := range rows {
		status := statusText(r)
		if r.HasUpdate {
			status = updateAvailableStyle.Render(status)
		}
		stack := r.Stack
		if stack == "" {
			stack = "-"
		}
		t.Row(stack, r.Container, r.Image, status)
	}
	fmt.Fprintf(w, "%s\n", t.String())
}
