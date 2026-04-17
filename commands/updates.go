package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"
	"charm.land/lipgloss/v2"
	"github.com/AhmedAburady/marina/internal/actions"
	"github.com/AhmedAburady/marina/internal/config"
	notifyPkg "github.com/AhmedAburady/marina/internal/notify"
	"github.com/AhmedAburady/marina/internal/registry"
	internalssh "github.com/AhmedAburady/marina/internal/ssh"
	"github.com/AhmedAburady/marina/internal/ui"
	"github.com/spf13/cobra"
)

// stackKey identifies a compose stack on a host. Shared by runUpdateApply
// and its helpers so the parallel-per-host path doesn't need its own type.
type stackKey struct{ Host, Stack string }

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
	var yes, stream bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Apply available image updates",
		Example: `  marina update --all --yes
  marina update -H myhost -s mystack --stream`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpdateApply(cmd, gf, yes, stream)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the interactive confirmation prompt")
	cmd.Flags().BoolVar(&stream, "stream", false, "Stream docker compose output live instead of running behind a spinner")
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
	targets, err := resolveTargets(gf, cfg)
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

// runUpdateApply gathers check results once, confirms with the user (unless
// --yes bypasses), then runs `docker compose pull && up -d` for each stack
// whose images have updates. Output defaults to behind a spinner; pass
// `--stream` to see live docker compose progress instead.
func runUpdateApply(cmd *cobra.Command, gf *GlobalFlags, yes, stream bool) error {
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
	w := cmd.OutOrStdout()
	ew := cmd.ErrOrStderr()

	if len(keys) == 0 {
		cmd.Println("All images are up-to-date.")
		// Fall through to the prune block below — `prune_after_update: true`
		// must be honoured even on no-op runs so accumulated dangling images
		// from past updates get swept up.
	}

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Host != keys[j].Host {
			return keys[i].Host < keys[j].Host
		}
		return keys[i].Stack < keys[j].Stack
	})

	// Interactive confirmation when --yes is not passed and there are updates
	// to apply. Skipped entirely on a no-op run since there's nothing to
	// confirm.
	if len(keys) > 0 && !yes {
		labels := make([]string, 0, len(keys))
		for _, k := range keys {
			labels = append(labels, fmt.Sprintf("  %s/%s", k.Host, k.Stack))
		}
		title := fmt.Sprintf("Apply %d update(s)?", len(keys))
		desc := strings.Join(labels, "\n")

		var confirmed bool
		formErr := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title(title).
					Description(desc).
					Value(&confirmed),
			),
		).Run()
		if formErr != nil {
			return formErr
		}
		if !confirmed {
			cmd.Println("Aborted.")
			return nil
		}
	}

	// Group by host. Same-host stacks MUST stay sequential — MaxConnsPerHost:1
	// on the docker transport forces all SSH traffic through one pipe, so
	// firing two `compose pull` commands concurrently against one host just
	// serializes through the same bottleneck with uglier output. Different
	// hosts have independent SSH pipes and can run simultaneously.
	hostGroups := make(map[string][]stackKey)
	for _, k := range keys {
		hostGroups[k.Host] = append(hostGroups[k.Host], k)
	}

	// Only enter the apply block when there's something to apply. A no-op
	// run with prune_after_update: true falls straight through to the prune
	// block below, skipping the "Applying 0 update(s)…" noise.
	switch {
	case len(hostGroups) == 0:
		// nothing to apply — already printed "All images are up-to-date."
	case len(hostGroups) == 1:
		// Single-host fast path: keep the existing spinner / stream behaviour
		// verbatim since there's nothing to parallelise.
		for _, k := range keys {
			runStackUpdate(cmd.Context(), w, ew, cfg, k, stackDirs[k], stream)
		}
	default:
		// Multi-host: one goroutine per host, each chewing through its own
		// sequential queue. Spinners don't compose across goroutines (they
		// fight for the cursor), so the multi-host path reports per-stack
		// status via one-line messages guarded by a shared mutex.
		var mu sync.Mutex
		report := func(out io.Writer, line string) {
			mu.Lock()
			defer mu.Unlock()
			fmt.Fprintln(out, line)
		}

		report(w, fmt.Sprintf("Applying %d update(s) across %d host(s)...", len(keys), len(hostGroups)))
		var wg sync.WaitGroup
		for host, hks := range hostGroups {
			wg.Add(1)
			go func(host string, hks []stackKey) {
				defer wg.Done()
				for _, k := range hks {
					report(w, fmt.Sprintf("  [%s] %s: updating…", host, k.Stack))
					if err := runStackUpdateQuiet(cmd.Context(), cfg, k, stackDirs[k]); err != nil {
						report(ew, fmt.Sprintf("  [%s] %s: FAILED — %v", host, k.Stack, firstLine(err)))
						continue
					}
					report(w, fmt.Sprintf("  [%s] %s: ✓ updated", host, k.Stack))
				}
			}(host, hks)
		}
		wg.Wait()
		report(w, "Done.")
	}

	// Post-apply image prune — gated on `prune_after_update`. Runs against
	// every targeted host, not just the ones where an update actually
	// landed, so dangling images left over from earlier runs still get
	// swept up on a no-op cycle. Same parallelism rules as the apply loop.
	if cfg.Settings.PruneAfterUpdate {
		pruneHosts := make([]string, 0, len(targets))
		for host := range targets {
			pruneHosts = append(pruneHosts, host)
		}
		sort.Strings(pruneHosts)

		if len(pruneHosts) == 1 {
			runHostPrune(cmd.Context(), w, ew, cfg, pruneHosts[0], stream)
		} else if len(pruneHosts) > 1 {
			var mu sync.Mutex
			report := func(out io.Writer, line string) {
				mu.Lock()
				defer mu.Unlock()
				fmt.Fprintln(out, line)
			}
			report(w, fmt.Sprintf("Pruning old images on %d host(s)...", len(pruneHosts)))
			var wg sync.WaitGroup
			for _, host := range pruneHosts {
				wg.Add(1)
				go func(host string) {
					defer wg.Done()
					report(w, fmt.Sprintf("  [%s] pruning…", host))
					if err := runHostPruneQuiet(cmd.Context(), cfg, host); err != nil {
						report(ew, fmt.Sprintf("  [%s] prune FAILED — %v", host, firstLine(err)))
						return
					}
					report(w, fmt.Sprintf("  [%s] ✓ pruned", host))
				}(host)
			}
			wg.Wait()
			report(w, "Done.")
		}
	}
	return nil
}

// runStackUpdate is the single-host path: spinner or streaming per --stream.
// Delegates to actions.ApplyStackUpdate for two-step pull+up semantics that
// match the TUI's SequenceCmds(pull, up) flow.
func runStackUpdate(ctx context.Context, w, ew io.Writer, cfg *config.Config, k stackKey, dir string, stream bool) {
	hostCfg := cfg.Hosts[k.Host]
	sshCfg := internalssh.Config{
		Address: hostCfg.SSHAddress(cfg.Settings.Username),
		KeyPath: hostCfg.ResolvedSSHKey(cfg.Settings.SSHKey),
	}
	hc := &hostContext{cfg: cfg, host: hostCfg, name: k.Host, sshCfg: sshCfg}

	if dir == "" {
		resolved, err := findStackDir(ctx, hc, k.Stack)
		if err != nil {
			fmt.Fprintf(ew, "warning: %s/%s: %v\n", k.Host, k.Stack, err)
			return
		}
		dir = resolved
	}

	title := fmt.Sprintf("Updating stack %q on %q...", k.Stack, k.Host)
	doneMsg := fmt.Sprintf("Updated stack %q on %q.", k.Stack, k.Host)

	if stream {
		fmt.Fprintf(w, "\n── %s ──\n", title)
		if err := actions.ApplyStackUpdate(ctx, sshCfg, dir, w); err != nil {
			fmt.Fprintf(ew, "warning: %s/%s: %v\n", k.Host, k.Stack, err)
			return
		}
		fmt.Fprintln(w, doneMsg)
	} else {
		var buf bytes.Buffer
		var applyErr error
		spinErr := spinner.New().
			Type(spinner.MiniDot).
			Title(title).
			Action(func() { applyErr = actions.ApplyStackUpdate(ctx, sshCfg, dir, &buf) }).
			Run()
		if spinErr != nil {
			fmt.Fprintf(ew, "warning: %s/%s: %v\n", k.Host, k.Stack, spinErr)
			return
		}
		if applyErr != nil {
			fmt.Fprintf(ew, "warning: %s/%s: %v\n", k.Host, k.Stack, applyErr)
			return
		}
		fmt.Fprintln(w)
		if buf.Len() > 0 {
			fmt.Fprint(w, buf.String())
		}
		fmt.Fprintln(w, doneMsg)
	}
}

// runStackUpdateQuiet is the multi-host goroutine worker: no spinner (they
// don't compose) and no streaming — returns an error the caller reports via
// the shared status-line mutex. Delegates to actions.ApplyStackUpdate.
func runStackUpdateQuiet(ctx context.Context, cfg *config.Config, k stackKey, dir string) error {
	hostCfg := cfg.Hosts[k.Host]
	sshCfg := internalssh.Config{
		Address: hostCfg.SSHAddress(cfg.Settings.Username),
		KeyPath: hostCfg.ResolvedSSHKey(cfg.Settings.SSHKey),
	}
	hc := &hostContext{cfg: cfg, host: hostCfg, name: k.Host, sshCfg: sshCfg}
	if dir == "" {
		resolved, err := findStackDir(ctx, hc, k.Stack)
		if err != nil {
			return err
		}
		dir = resolved
	}
	return actions.ApplyStackUpdate(ctx, sshCfg, dir, io.Discard)
}

// runHostPrune / runHostPruneQuiet mirror the stack helpers for the
// post-apply image prune pass. Delegate to actions.PruneHost.
func runHostPrune(ctx context.Context, w, ew io.Writer, cfg *config.Config, host string, stream bool) {
	hostCfg := cfg.Hosts[host]
	sshCfg := internalssh.Config{
		Address: hostCfg.SSHAddress(cfg.Settings.Username),
		KeyPath: hostCfg.ResolvedSSHKey(cfg.Settings.SSHKey),
	}
	title := fmt.Sprintf("Pruning old images on %q...", host)
	doneMsg := fmt.Sprintf("Pruned old images on %q.", host)
	if stream {
		fmt.Fprintf(w, "\n── %s ──\n", title)
		if err := actions.PruneHost(ctx, sshCfg, w); err != nil {
			fmt.Fprintf(ew, "warning: prune on %s: %v\n", host, err)
		}
		fmt.Fprintln(w, doneMsg)
	} else {
		var buf bytes.Buffer
		var pruneErr error
		spinErr := spinner.New().
			Type(spinner.MiniDot).
			Title(title).
			Action(func() { pruneErr = actions.PruneHost(ctx, sshCfg, &buf) }).
			Run()
		if spinErr != nil {
			fmt.Fprintf(ew, "warning: prune on %s: %v\n", host, spinErr)
			return
		}
		if pruneErr != nil {
			fmt.Fprintf(ew, "warning: prune on %s: %v\n", host, pruneErr)
			return
		}
		fmt.Fprintln(w)
		if buf.Len() > 0 {
			fmt.Fprint(w, buf.String())
		}
		fmt.Fprintln(w, doneMsg)
	}
}

func runHostPruneQuiet(ctx context.Context, cfg *config.Config, host string) error {
	hostCfg := cfg.Hosts[host]
	sshCfg := internalssh.Config{
		Address: hostCfg.SSHAddress(cfg.Settings.Username),
		KeyPath: hostCfg.ResolvedSSHKey(cfg.Settings.SSHKey),
	}
	return actions.PruneHost(ctx, sshCfg, io.Discard)
}

// firstLine trims an error message to its first line, capped at 80 runes,
// so one-liner status reports stay tidy.
func firstLine(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:79] + "…"
	}
	return s
}

// ── Check orchestration ──────────────────────────────────────────────────────

// runChecks delegates to actions.RunChecks — the single shared implementation
// used by both this CLI command and the TUI Updates screen.
func runChecks(
	ctx context.Context,
	cfg *config.Config,
	targets map[string]*config.HostConfig,
) ([]registry.Result, error) {
	return actions.RunChecks(ctx, cfg, targets)
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
