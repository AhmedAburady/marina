package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"

	"charm.land/huh/v2/spinner"
	"charm.land/lipgloss/v2"
	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/docker"
	"github.com/docker/docker/api/types/container"
	notifyPkg "github.com/AhmedAburady/marina/internal/notify"
	"github.com/AhmedAburady/marina/internal/registry"
	"github.com/AhmedAburady/marina/internal/ui"
	internalssh "github.com/AhmedAburady/marina/internal/ssh"
	"github.com/spf13/cobra"
)

// updateInfo holds the check result for a single container.
type updateInfo struct {
	Host      string
	Stack     string
	Container string
	Image     string
	Result    registry.CheckResult
	Dir       string // working dir from container labels, used by apply
}

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

// runUpdatesCheck resolves hosts, builds candidates, then either runs the TUI
// or falls back to notify/plain modes.
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

	// ── Gather candidates (fast: just list containers) ────────────────────────
	candidates, err := gatherCandidates(cmd.Context(), cfg, targets)
	if err != nil {
		return err
	}

	if len(candidates) == 0 {
		cmd.Println("No containers found.")
		return nil
	}

	// Persistent file cache — skips registry calls for recently-checked images.
	// Errors are soft: a missing or corrupt cache just starts fresh.
	regCache, _ := registry.LoadCache("")

	// In-memory dedup — prevents redundant concurrent checks when multiple
	// containers share the same image (e.g. three containers all on postgres:14).
	type cacheEntry struct {
		result registry.CheckResult
		done   chan struct{}
	}
	checkCache := &sync.Map{}

	// checkFn only hits the registry — Docker inspect was already done in gatherCandidates.
	checkFn := func(ctx context.Context, c ui.UpdateCandidate) ui.UpdateCheckResult {
		if c.Digest == "" {
			return ui.UpdateCheckResult{
				UpdateCandidate: c,
				Status:          "check failed",
				Error:           fmt.Errorf("no registry digest (locally built image)"),
			}
		}

		key := c.ImageRef + "|" + c.Digest

		// 1. Check the persistent file cache first (avoids any network call).
		if status, ok := regCache.Lookup(c.ImageRef, c.Digest); ok {
			return ui.UpdateCheckResult{
				UpdateCandidate: c,
				Status:          status.String(),
				HasUpdate:       status == registry.UpdateAvailable,
			}
		}

		// 2. Dedup concurrent registry checks for the same image.
		entry := &cacheEntry{done: make(chan struct{})}
		if existing, loaded := checkCache.LoadOrStore(key, entry); loaded {
			// Another goroutine is already checking this image — wait for it.
			e := existing.(*cacheEntry)
			<-e.done
			return ui.UpdateCheckResult{
				UpdateCandidate: c,
				Status:          e.result.Status.String(),
				HasUpdate:       e.result.Status == registry.UpdateAvailable,
				Error:           e.result.Error,
			}
		}

		// 3. We're the first goroutine for this key — hit the registry.
		cr := registry.CheckUpdate(ctx, c.ImageRef, c.Digest)
		entry.result = cr
		close(entry.done) // unblock any goroutines waiting on step 2

		// 4. Persist successful results so the next `marina check` run skips them.
		if cr.Status != registry.CheckFailed {
			regCache.Store(c.ImageRef, c.Digest, cr.Status)
		}

		return ui.UpdateCheckResult{
			UpdateCandidate: c,
			Status:          cr.Status.String(),
			HasUpdate:       cr.Status == registry.UpdateAvailable,
			Error:           cr.Error,
		}
	}

	// ── --notify mode: sequential checks + Gotify ────────────────────────────
	if notify {
		var allResults []updateInfo
		var collectErr error

		spinErr := spinner.New().
			Type(spinner.MiniDot).
			Title("Checking for updates...").
			Action(func() {
				allResults, collectErr = gatherUpdateInfo(cmd.Context(), cfg, targets)
			}).
			Run()
		if spinErr != nil {
			return spinErr
		}
		if collectErr != nil {
			return collectErr
		}

		var available int
		for _, info := range allResults {
			if info.Result.Status == registry.UpdateAvailable {
				available++
			}
		}
		if available == 0 {
			cmd.Println("All images up to date — no notification sent.")
			return nil
		}

		var msg strings.Builder
		for _, info := range allResults {
			if info.Result.Status == registry.UpdateAvailable {
				fmt.Fprintf(&msg, "• %s/%s: %s\n", info.Host, info.Container, info.Image)
			}
		}

		gotifyCfg := notifyPkg.GotifyConfig{
			URL:      cfg.Notify.Gotify.URL,
			Token:    cfg.Notify.Gotify.Token,
			Priority: cfg.Notify.Gotify.Priority,
		}
		title := fmt.Sprintf("Marina: %d update(s) available", available)
		if err := notifyPkg.SendGotify(cmd.Context(), gotifyCfg, title, msg.String()); err != nil {
			return fmt.Errorf("send notification: %w", err)
		}
		cmd.Printf("Notification sent: %d update(s) available\n", available)
		return nil
	}

	// ── --plain mode: sequential checks + plain table ────────────────────────
	if gf.Plain {
		var allResults []updateInfo
		var collectErr error

		spinErr := spinner.New().
			Type(spinner.MiniDot).
			Title("Checking for updates...").
			Action(func() {
				allResults, collectErr = gatherUpdateInfo(cmd.Context(), cfg, targets)
			}).
			Run()
		if spinErr != nil {
			return spinErr
		}
		if collectErr != nil {
			return collectErr
		}

		sort.Slice(allResults, func(i, j int) bool {
			if allResults[i].Host != allResults[j].Host {
				return allResults[i].Host < allResults[j].Host
			}
			if allResults[i].Stack != allResults[j].Stack {
				return allResults[i].Stack < allResults[j].Stack
			}
			return allResults[i].Container < allResults[j].Container
		})

		var hostOrder []string
		grouped := make(map[string][]updateInfo)
		for _, r := range allResults {
			if _, seen := grouped[r.Host]; !seen {
				hostOrder = append(hostOrder, r.Host)
			}
			grouped[r.Host] = append(grouped[r.Host], r)
		}

		w := cmd.OutOrStdout()
		for i, host := range hostOrder {
			if i > 0 {
				fmt.Fprintln(w)
			}
			printUpdateTablePlain(w, host, grouped[host])
		}
		return nil
	}

	// ── Interactive TUI mode ──────────────────────────────────────────────────
	selected, err := ui.RunCheckTUI(cmd.Context(), candidates, checkFn)
	// Persist the cache regardless of how the TUI exited (cancel, error, or selection).
	_ = registry.SaveCache(regCache, "")
	if err != nil {
		return err
	}
	if selected == nil {
		cmd.Println("Cancelled.")
		return nil
	}
	if len(selected) == 0 {
		cmd.Println("No updates selected.")
		return nil
	}

	// Group selected items by host + stack.
	type stackKey struct {
		Host  string
		Stack string
	}
	// Prefer the Dir from the candidate (populated from container labels).
	stackDirs := make(map[stackKey]string)
	seen := make(map[stackKey]bool)
	var keys []stackKey
	for _, item := range selected {
		k := stackKey{Host: item.Host, Stack: item.Stack}
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
		if stackDirs[k] == "" && item.Dir != "" {
			stackDirs[k] = item.Dir
		}
	}

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Host != keys[j].Host {
			return keys[i].Host < keys[j].Host
		}
		return keys[i].Stack < keys[j].Stack
	})

	w := cmd.OutOrStdout()
	for _, k := range keys {
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
		commandFmt := "cd %s && docker compose pull && docker compose up -d"

		if dir := stackDirs[k]; dir != "" {
			command := fmt.Sprintf("cd %s && docker compose pull && docker compose up -d", dir)
			if err := execWithSpinner(cmd.Context(), w, hc, title, command, doneMsg); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s/%s: %v\n", k.Host, k.Stack, err)
			}
		} else {
			if err := execStackWithSpinner(cmd.Context(), w, hc, k.Stack, title, commandFmt, doneMsg); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s/%s: %v\n", k.Host, k.Stack, err)
			}
		}
	}

	return nil
}

// runUpdateApply requires --yes, checks for updates, then pulls and recreates
// any stack that has at least one container with an available update.
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

	// Gather update information first.
	var allResults []updateInfo
	var collectErr error

	spinErr := spinner.New().
		Type(spinner.MiniDot).
		Title("Checking for updates...").
		Action(func() {
			allResults, collectErr = gatherUpdateInfo(cmd.Context(), cfg, targets)
		}).
		Run()
	if spinErr != nil {
		return spinErr
	}
	if collectErr != nil {
		return collectErr
	}

	// Filter to containers with updates available.
	var needsUpdate []updateInfo
	for _, r := range allResults {
		if r.Result.Status == registry.UpdateAvailable {
			needsUpdate = append(needsUpdate, r)
		}
	}

	if len(needsUpdate) == 0 {
		cmd.Println("All images are up-to-date.")
		return nil
	}

	// Group by host + stack, collecting the working dir.
	type stackKey struct {
		Host  string
		Stack string
	}
	stackDirs := make(map[stackKey]string)
	for _, r := range needsUpdate {
		k := stackKey{Host: r.Host, Stack: r.Stack}
		if _, seen := stackDirs[k]; !seen {
			stackDirs[k] = r.Dir
		}
	}

	// Stable ordering.
	keys := make([]stackKey, 0, len(stackDirs))
	for k := range stackDirs {
		keys = append(keys, k)
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

// ── candidate gathering ───────────────────────────────────────────────────────

// gatherCandidates fans out to all target hosts and collects lightweight
// UpdateCandidate entries (no registry check yet — just container list).
func gatherCandidates(ctx context.Context, cfg *config.Config, targets map[string]*config.HostConfig) ([]ui.UpdateCandidate, error) {
	type hostResult struct {
		host   string
		items  []ui.UpdateCandidate
		err    error
	}

	ch := make(chan hostResult, len(targets))
	var wg sync.WaitGroup

	for name, h := range targets {
		wg.Add(1)
		go func(hostName, address, sshKey string) {
			defer wg.Done()
			items, err := listCandidatesFromHost(ctx, hostName, address, sshKey)
			ch <- hostResult{host: hostName, items: items, err: err}
		}(name, h.SSHAddress(cfg.Settings.Username), h.ResolvedSSHKey(cfg.Settings.SSHKey))
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var all []ui.UpdateCandidate
	var firstErr error
	for r := range ch {
		if r.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("host %q: %w", r.host, r.err)
			}
			continue
		}
		all = append(all, r.items...)
	}

	return all, firstErr
}

// listCandidatesFromHost connects to one host and returns all containers as
// UpdateCandidate entries, without performing any registry checks.
func listCandidatesFromHost(ctx context.Context, hostName, address, sshKey string) ([]ui.UpdateCandidate, error) {
	dc, err := docker.NewClient(ctx, address, sshKey)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer dc.Close()

	containers, err := dc.ListContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	out := make([]ui.UpdateCandidate, 0, len(containers))
	for _, c := range containers {
		// Inspect each container sequentially (SSH can't handle concurrent requests)
		imageRef, digest, _ := dc.InspectContainer(ctx, c.ID)
		if imageRef == "" {
			imageRef = c.Image
		}
		out = append(out, ui.UpdateCandidate{
			Host:        hostName,
			Stack:       stackLabel(c.Labels),
			Container:   containerDisplayName(c.Names, c.ID),
			ContainerID: c.ID,
			Image:       c.Image,
			ImageRef:    imageRef,
			Digest:      digest,
			Dir:         c.Labels["com.docker.compose.project.working_dir"],
		})
	}
	return out, nil
}

// ── legacy helpers (used by --notify / --plain and runUpdateApply) ────────────

// hostUpdateResult holds all updateInfo rows collected from a single host.
type hostUpdateResult struct {
	host    string
	results []updateInfo
	err     error
}

// gatherUpdateInfo fans out to all target hosts in parallel, inspects every
// container, and checks each image against its registry.
func gatherUpdateInfo(ctx context.Context, cfg *config.Config, targets map[string]*config.HostConfig) ([]updateInfo, error) {
	ch := make(chan hostUpdateResult, len(targets))
	var wg sync.WaitGroup

	for name, h := range targets {
		wg.Add(1)
		go func(hostName, address, sshKey string) {
			defer wg.Done()
			rows, err := checkHostUpdates(ctx, hostName, address, sshKey)
			ch <- hostUpdateResult{host: hostName, results: rows, err: err}
		}(name, h.SSHAddress(cfg.Settings.Username), h.ResolvedSSHKey(cfg.Settings.SSHKey))
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var all []updateInfo
	var firstErr error
	for r := range ch {
		if r.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("host %q: %w", r.host, r.err)
			}
			continue
		}
		all = append(all, r.results...)
	}

	return all, firstErr
}

// checkHostUpdates connects to a single host, lists all containers, inspects
// each one, and calls the registry checker.
func checkHostUpdates(ctx context.Context, hostName, address, sshKey string) ([]updateInfo, error) {
	dc, err := docker.NewClient(ctx, address, sshKey)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer dc.Close()

	containers, err := dc.ListContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	type indexedResult struct {
		idx  int
		info updateInfo
	}

	results := make(chan indexedResult, len(containers))
	var wg sync.WaitGroup

	for i, c := range containers {
		wg.Add(1)
		go func(idx int, c container.Summary) {
			defer wg.Done()

			imageRef, digest, inspectErr := dc.InspectContainer(ctx, c.ID)
			if inspectErr != nil {
				results <- indexedResult{idx, updateInfo{
					Host:      hostName,
					Stack:     stackLabel(c.Labels),
					Container: containerDisplayName(c.Names, c.ID),
					Image:     c.Image,
					Result: registry.CheckResult{
						Image:  c.Image,
						Status: registry.CheckFailed,
						Error:  inspectErr,
					},
				}}
				return
			}

			var result registry.CheckResult
			if digest == "" {
				result = registry.CheckResult{
					Image:  imageRef,
					Status: registry.CheckFailed,
					Error:  fmt.Errorf("no registry digest (locally built image)"),
				}
			} else {
				result = registry.CheckUpdate(ctx, imageRef, digest)
			}

			results <- indexedResult{idx, updateInfo{
				Host:      hostName,
				Stack:     stackLabel(c.Labels),
				Container: containerDisplayName(c.Names, c.ID),
				Image:     imageRef,
				Dir:       c.Labels["com.docker.compose.project.working_dir"],
				Result:    result,
			}}
		}(i, c)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	indexed := make([]indexedResult, 0, len(containers))
	for r := range results {
		indexed = append(indexed, r)
	}
	sort.Slice(indexed, func(a, b int) bool { return indexed[a].idx < indexed[b].idx })

	rows := make([]updateInfo, len(indexed))
	for i, r := range indexed {
		rows[i] = r.info
	}
	return rows, nil
}

// ── display helpers ───────────────────────────────────────────────────────────

// stackLabel returns the compose project name from container labels, or "-".
func stackLabel(labels map[string]string) string {
	if s := labels["com.docker.compose.project"]; s != "" {
		return s
	}
	return "-"
}

// containerDisplayName returns the primary container name with the leading
// slash stripped, or a short ID when no name is available.
func containerDisplayName(names []string, id string) string {
	if len(names) > 0 {
		return strings.TrimPrefix(names[0], "/")
	}
	if len(id) >= 12 {
		return id[:12]
	}
	return id
}

// statusText returns the human-readable status cell value for a CheckResult.
func statusText(r registry.CheckResult) string {
	switch r.Status {
	case registry.UpdateAvailable:
		return r.Status.String()
	case registry.UpToDate:
		return r.Status.String()
	default:
		if r.Error != nil {
			return "check failed: " + r.Error.Error()
		}
		return r.Status.String()
	}
}

// printUpdateTable renders a styled lipgloss table for a single host.
func printUpdateTable(w interface{ Write([]byte) (int, error) }, host string, rows []updateInfo) {
	hostHeaderStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	fmt.Fprintf(w, "%s\n", hostHeaderStyle.Render("▸ "+host))

	t := ui.StyledTable("STACK", "CONTAINER", "IMAGE", "STATUS")
	for _, r := range rows {
		status := statusText(r.Result)
		if r.Result.Status == registry.UpdateAvailable {
			status = updateAvailableStyle.Render(status)
		}
		t.Row(r.Stack, r.Container, r.Image, status)
	}
	fmt.Fprintf(w, "%s\n", t.String())
}

// printUpdateTablePlain renders a plain tabwriter table for a single host.
func printUpdateTablePlain(w interface{ Write([]byte) (int, error) }, host string, rows []updateInfo) {
	fmt.Fprintf(w, "[%s]\n", host)
	tw := tabwriter.NewWriter(w, 0, 4, 3, ' ', 0)
	fmt.Fprintln(tw, "STACK\tCONTAINER\tIMAGE\tSTATUS")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Stack, r.Container, r.Image, statusText(r.Result))
	}
	tw.Flush()
	fmt.Fprintln(w, "")
}
