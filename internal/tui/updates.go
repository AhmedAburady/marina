package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/registry"
	internalssh "github.com/AhmedAburady/marina/internal/ssh"
)

type updatesPhase int

const (
	phaseUpdatesLoading updatesPhase = iota
	phaseUpdatesBuilding
	phaseUpdatesResults
	phaseUpdatesApplying
	phaseUpdatesDone
)

// checkerReadyMsg is emitted by the BuildChecker async command once the
// per-host candidate list and shared check function are ready.
type checkerReadyMsg struct {
	candidates []registry.Candidate
	check      registry.CheckFn
	err        error
}

// updateResultMsg is delivered once per registry check (one per candidate).
type updateResultMsg struct{ result registry.Result }

// updatesScreen walks the user through: probe registries for updates → tick
// off which ones to apply → fire pull+up for each selected stack.
type updatesScreen struct {
	ctx   context.Context
	cfg   *config.Config
	phase updatesPhase
	err   error

	candidates []registry.Candidate
	check      registry.CheckFn

	checked int
	total   int
	results []registry.Result

	cursor   int
	selected map[int]bool
	showAll  bool

	appliedOk   int
	appliedFail int

	// pruning tracks the post-apply `docker image prune -f` phase triggered
	// by cfg.Settings.PruneAfterUpdate. When true we're waiting for the
	// prune commands (one per unique host in the apply set) to report back
	// before rerunning the registry check.
	pruning      bool
	prunePending int

	spinner spinner.Model
	sb      scrollBody
	filter  filterBar
}

func newUpdatesScreen(ctx context.Context, cfg *config.Config) *updatesScreen {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = sSpinner
	return &updatesScreen{
		ctx:      ctx,
		cfg:      cfg,
		phase:    phaseUpdatesLoading,
		selected: make(map[int]bool),
		spinner:  sp,
		sb:       newScrollBody(),
		filter:   newFilterBar(),
	}
}

func (s *updatesScreen) Title() string { return "Updates" }

func (s *updatesScreen) Init() tea.Cmd {
	return tea.Batch(
		s.spinner.Tick,
		s.buildCheckerCmd(),
	)
}

func (s *updatesScreen) Help() string {
	switch s.phase {
	case phaseUpdatesLoading, phaseUpdatesBuilding:
		return "checking image registries…  esc back"
	case phaseUpdatesResults:
		if s.filter.Active() {
			return "type to filter · enter apply · esc clear"
		}
		return "↑/↓ move · / filter · space select · a toggle all · t show-all · enter apply · esc back"
	case phaseUpdatesApplying:
		// The apply phase renders a centered spinner overlay — keep the
		// status bar minimal so the message isn't duplicated.
		return "esc back"
	case phaseUpdatesDone:
		return "enter check again · esc back"
	}
	return "esc back"
}

// Modal implements ModalProvider. During the apply phase it floats a small
// centered spinner dialog over the results view so the background list
// stays visible as context. Other phases have no modal.
func (s *updatesScreen) Modal() (string, bool) {
	if s.phase == phaseUpdatesApplying {
		return renderSpinnerModal(s.spinner.View(), "Applying updates…"), true
	}
	return "", false
}

func (s *updatesScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	// Filter bar takes keys while editing on the results phase; other phases
	// ignore it because there's nothing to filter against.
	if s.phase == phaseUpdatesResults {
		if handled, cmd := s.filter.Update(msg); handled {
			s.cursor = 0
			return s, cmd
		}
	}

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.String() == "esc" || msg.String() == "left" {
			if s.phase == phaseUpdatesResults && s.filter.HasQuery() {
				s.filter.Clear()
				s.cursor = 0
				return s, nil
			}
			return s, popCmd()
		}
		return s.handleKey(msg)

	case checkerReadyMsg:
		if msg.err != nil {
			s.err = msg.err
			s.phase = phaseUpdatesDone
			return s, nil
		}
		s.candidates = msg.candidates
		s.check = msg.check
		s.total = len(msg.candidates)
		s.phase = phaseUpdatesBuilding
		if s.total == 0 {
			s.phase = phaseUpdatesResults
			return s, nil
		}
		cmds := make([]tea.Cmd, 0, len(msg.candidates))
		for _, c := range msg.candidates {
			cmds = append(cmds, s.checkOneCmd(c))
		}
		return s, tea.Batch(cmds...)

	case updateResultMsg:
		s.checked++
		s.results = append(s.results, msg.result)
		if s.checked >= s.total {
			s.finishChecks()
		}

	case SequenceResultsMsg:
		// Apply-phase results: count successes / failures, then — once every
		// stack has reported — either fire the post-apply prune pass (when
		// PruneAfterUpdate is on) or jump straight to the recheck.
		for _, r := range msg.Results {
			if r.Err == nil {
				s.appliedOk++
			} else {
				s.appliedFail++
			}
		}

		if s.pruning {
			// Prune phase: one SequenceResultsMsg arrives per host. When
			// every host has reported, we're truly done — recheck.
			if s.appliedOk+s.appliedFail >= s.prunePending {
				return s, s.finishAndRecheck()
			}
			return s, nil
		}

		if s.appliedOk+s.appliedFail >= s.pendingApply() {
			if s.cfg.Settings.PruneAfterUpdate {
				return s, s.startPostApplyPrune()
			}
			return s, s.finishAndRecheck()
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		if !s.spinning() {
			return s, nil
		}
		return s, cmd

	}
	return s, nil
}

func (s *updatesScreen) handleKey(msg tea.KeyPressMsg) (Screen, tea.Cmd) {
	switch s.phase {
	case phaseUpdatesResults:
		items := s.filteredItems()
		switch msg.String() {
		case "up", "k":
			if s.cursor > 0 {
				s.cursor--
			}
		case "down", "j":
			if s.cursor < len(items)-1 {
				s.cursor++
			}
		case "space", " ":
			if len(items) == 0 {
				return s, nil
			}
			idx := s.absoluteIndex(s.cursor)
			s.selected[idx] = !s.selected[idx]
		case "a":
			s.toggleAll()
		case "t":
			s.showAll = !s.showAll
			s.cursor = 0
		case "/":
			return s, s.filter.Activate()
		case "enter":
			return s, s.startApply()
		}
	case phaseUpdatesDone:
		switch msg.String() {
		case "enter":
			// Re-run the whole check.
			s.phase = phaseUpdatesLoading
			s.results = nil
			s.selected = make(map[int]bool)
			s.cursor = 0
			s.checked = 0
			s.appliedOk = 0
			s.appliedFail = 0
			return s, tea.Batch(s.spinner.Tick, s.buildCheckerCmd())
		}
	}
	return s, nil
}

func (s *updatesScreen) View(width, height int) string {
	switch s.phase {
	case phaseUpdatesLoading:
		return panelLines(width, height, []string{
			spacer(width),
			pendingNote(width, s.spinner.View(), "discovering containers…"),
		})
	case phaseUpdatesBuilding:
		return s.viewProgress(width, height)
	case phaseUpdatesResults:
		return s.viewResults(width, height)
	case phaseUpdatesApplying:
		// Render the results list as the background; the dashboard will
		// composite the spinner modal returned by Modal() dead centre on
		// top. Avoids the blank-screen + duplicated "applying updates…"
		// status bar the previous full-screen placeholder produced.
		return s.viewResults(width, height)
	case phaseUpdatesDone:
		return s.viewDone(width, height)
	}
	return panelLines(width, height, []string{spacer(width)})
}

func (s *updatesScreen) viewProgress(width, height int) string {
	frac := 0.0
	if s.total > 0 {
		frac = float64(s.checked) / float64(s.total)
	}
	bar := renderBar(width-8, frac)
	return panelLines(width, height, []string{
		spacer(width),
		pendingNote(width, s.spinner.View(), fmt.Sprintf("checking %d of %d", s.checked, s.total)),
		spacer(width),
		bodyLine(width, bar),
	})
}

func (s *updatesScreen) viewResults(width, height int) string {
	items := s.filteredItems()
	updates := s.countUpdates()

	// Top: summary counter + optional filter bar.
	var top []string
	top = append(top, spacer(width))
	tag := "showing updates only"
	if s.showAll {
		tag = "showing all"
	}
	top = append(top, summaryLine(width, fmt.Sprintf("%d update(s) available", updates), tag))
	if bar := s.filter.View(width, len(items)); bar != "" {
		top = append(top, bar)
	}
	top = append(top, spacer(width))

	if len(items) == 0 {
		top = append(top, mutedLine(width, "All images are up-to-date."))
		return panelLines(width, height, top)
	}

	// Bottom: selection counter.
	selCount := s.countSelected()
	var bottom []string
	bottom = append(bottom, spacer(width))
	bottom = append(bottom, notice(width, fmt.Sprintf("%d selected", selCount)))

	// CLI parity: STACK, CONTAINER, IMAGE, STATUS. Selection is shown as
	// a "✓ " or "  " prefix on the STACK cell — no extra column.
	inner := innerWidth(width)
	widths := shareWidths(inner,
		[]int{2, 3, 4, 3},
		[]int{12, 14, 18, 12},
	)
	all := make([][]string, 0, len(items))
	for i, item := range items {
		absIdx := s.absoluteIndex(i)
		mark := "  "
		if s.selected[absIdx] {
			mark = "✓ "
		}
		all = append(all, []string{item.Host, mark + emptyToDash(item.Stack), item.Container, item.Image, item.Status})
	}
	groups := groupRows(all, 0) // group by HOST
	content, cursorLine, headerLine := groupedLines(width,
		[]string{"STACK", "CONTAINER", "IMAGE", "STATUS"},
		widths, groups, s.cursor)

	bodyHeight := max0(height - len(top) - len(bottom))
	body := viewportBody(&s.sb, width, bodyHeight, content, cursorLine, headerLine)
	lines := append(append(top, body...), bottom...)

	return panelLines(width, height, lines)
}

func (s *updatesScreen) viewDone(width, height int) string {
	var lines []string
	lines = append(lines, spacer(width))
	if s.err != nil {
		lines = append(lines, errorNote(width, firstLineOf(s.err)))
		return panelLines(width, height, lines)
	}
	if s.appliedOk+s.appliedFail == 0 {
		lines = append(lines, notice(width, fmt.Sprintf("Checked %d container(s). All up-to-date.", s.total)))
	} else {
		lines = append(lines, notice(width, fmt.Sprintf("Applied %d update(s).", s.appliedOk)))
		if s.appliedFail > 0 {
			lines = append(lines, errorNote(width, fmt.Sprintf("%d failed.", s.appliedFail)))
		}
	}
	lines = append(lines, spacer(width))
	lines = append(lines, mutedLine(width, "Press enter to check again, or esc to go back."))
	return panelLines(width, height, lines)
}

// ── Internals ───────────────────────────────────────────────────────────────

func (s *updatesScreen) Progress() (float64, bool) {
	switch s.phase {
	case phaseUpdatesBuilding:
		if s.total == 0 {
			return 0, false
		}
		return float64(s.checked) / float64(s.total), true
	case phaseUpdatesApplying:
		return 0, true // indeterminate while applying
	}
	return 0, false
}

func (s *updatesScreen) spinning() bool {
	switch s.phase {
	case phaseUpdatesLoading, phaseUpdatesBuilding, phaseUpdatesApplying:
		return true
	}
	return false
}

func (s *updatesScreen) buildCheckerCmd() tea.Cmd {
	return func() tea.Msg {
		candidates, check, _, err := registry.BuildChecker(s.ctx, s.cfg, s.cfg.Hosts)
		return checkerReadyMsg{candidates: candidates, check: check, err: err}
	}
}

// Note: actions.RunChecks is the CLI/cron entry point for checks with
// errgroup concurrency limiting. The TUI retains its per-candidate fan-out
// (buildCheckerCmd → checkOneCmd) to preserve the real-time progress bar.

func (s *updatesScreen) checkOneCmd(c registry.Candidate) tea.Cmd {
	return func() tea.Msg {
		r := s.check(s.ctx, c)
		locals := make([]string, 0, len(c.Digests))
		for _, d := range c.Digests {
			locals = append(locals, shortDigest(d))
		}
		Log().Info("check.result",
			"host", c.Host, "stack", c.Stack, "container", c.Container,
			"image", c.ImageRef, "platform", c.OS+"/"+c.Architecture,
			"locals", strings.Join(locals, ","),
			"remote", shortDigest(r.RemoteDigest),
			"status", r.Status)
		return updateResultMsg{result: r}
	}
}

// shortDigest trims a RepoDigest / manifest digest to its first 16 runes
// after the sha256: prefix so marina.log entries stay readable.
func shortDigest(d string) string {
	if idx := strings.LastIndex(d, "@"); idx >= 0 {
		d = d[idx+1:]
	}
	if len(d) > 23 {
		return d[:23] + "…"
	}
	return d
}

// finishChecks sorts results and auto-selects every row that has an update.
// No persistent cache — every check cycle hits the registry fresh.
func (s *updatesScreen) finishChecks() {
	sort.Slice(s.results, func(i, j int) bool {
		a, b := s.results[i], s.results[j]
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		if a.Stack != b.Stack {
			return a.Stack < b.Stack
		}
		return a.Container < b.Container
	})
	s.selected = make(map[int]bool)
	for i, r := range s.results {
		if r.HasUpdate {
			s.selected[i] = true
		}
	}
	s.phase = phaseUpdatesResults
}

// filteredItems returns the rows visible under the current filter chain.
// Two layers stack: `t` (showAll) — when false, drop rows with no update —
// and the `/` text filter, which looks for substring matches across the
// host / stack / container / image / status cells.
func (s *updatesScreen) filteredItems() []registry.Result {
	out := make([]registry.Result, 0, len(s.results))
	for _, r := range s.results {
		if !s.showAll && !r.HasUpdate {
			continue
		}
		if !s.filter.Match(r.Host, r.Stack, r.Container, r.ImageRef, r.Status) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// absoluteIndex maps a cursor index in the filtered list back to s.results
// so selection state stays stable as filters toggle. Must use the same
// predicates as filteredItems above — keep them in sync.
func (s *updatesScreen) absoluteIndex(visibleIdx int) int {
	seen := 0
	for i, r := range s.results {
		if !s.showAll && !r.HasUpdate {
			continue
		}
		if !s.filter.Match(r.Host, r.Stack, r.Container, r.ImageRef, r.Status) {
			continue
		}
		if seen == visibleIdx {
			return i
		}
		seen++
	}
	return -1
}

func (s *updatesScreen) toggleAll() {
	items := s.filteredItems()
	// If every visible item is selected, clear; otherwise select all visible.
	allOn := true
	for i := range items {
		if !s.selected[s.absoluteIndex(i)] {
			allOn = false
			break
		}
	}
	if allOn {
		for i := range items {
			delete(s.selected, s.absoluteIndex(i))
		}
		return
	}
	for i := range items {
		s.selected[s.absoluteIndex(i)] = true
	}
}

func (s *updatesScreen) countUpdates() int {
	n := 0
	for _, r := range s.results {
		if r.HasUpdate {
			n++
		}
	}
	return n
}

func (s *updatesScreen) countSelected() int {
	n := 0
	for _, v := range s.selected {
		if v {
			n++
		}
	}
	return n
}

// startApply groups selected items by (host, stack), builds a pull+up sequence
// per group, and fires them in a single tea.Batch. s.pendingApply() is used
// later to decide when the apply phase is finished.
func (s *updatesScreen) startApply() tea.Cmd {
	type groupKey struct{ host, stack string }
	groups := make(map[groupKey]string) // → dir
	var order []groupKey

	for i, r := range s.results {
		if !s.selected[i] || !r.HasUpdate {
			continue
		}
		k := groupKey{host: r.Host, stack: r.Stack}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
			groups[k] = r.Dir
		}
	}
	if len(order) == 0 {
		return nil
	}

	s.phase = phaseUpdatesApplying
	s.appliedOk = 0
	s.appliedFail = 0

	Log().Info("apply.start", "stacks", len(order))
	cmds := make([]tea.Cmd, 0, len(order)+1)
	for _, k := range order {
		dir := groups[k]
		hostCfg := s.cfg.Hosts[k.host]
		if hostCfg == nil || dir == "" {
			// Most common silent-failure: container has no
			// compose.project.working_dir label (e.g. stacks started
			// outside compose, or compose v1 before labels). Log the
			// reason so users can see why apply did nothing.
			Log().Warn("apply.skip",
				"host", k.host, "stack", k.stack,
				"reason", "missing dir or host config",
				"dir", dir, "host_present", hostCfg != nil)
			s.appliedFail++
			continue
		}
		sshCfg := internalssh.Config{
			Address: hostCfg.SSHAddress(s.cfg.Settings.Username),
			KeyPath: hostCfg.ResolvedSSHKey(s.cfg.Settings.SSHKey),
		}
		key := k.host + "/" + k.stack
		cmds = append(cmds, SequenceCmds(
			ComposeExecCmd(s.ctx, sshCfg, dir, "pull", "compose.pull", key),
			ComposeExecCmd(s.ctx, sshCfg, dir, "up -d", "compose.up", key),
		))
	}
	cmds = append(cmds, s.spinner.Tick)
	return tea.Batch(cmds...)
}

// startPostApplyPrune fires `docker image prune -f` once per unique host
// involved in the current apply. Honors cfg.Settings.PruneAfterUpdate;
// mirrors what `marina update --all --yes` does at the CLI level so the
// two paths don't drift. Each prune wraps in a single-step SequenceCmds
// so its result lands on the same SequenceResultsMsg path the apply used.
func (s *updatesScreen) startPostApplyPrune() tea.Cmd {
	hosts := s.uniqueAppliedHosts()
	if len(hosts) == 0 {
		return s.finishAndRecheck()
	}

	// Transition to prune phase: reset the apply counter, redirect the
	// SequenceResultsMsg handler via the `pruning` flag.
	s.pruning = true
	s.prunePending = len(hosts)
	s.appliedOk = 0
	s.appliedFail = 0

	Log().Info("apply.prune.start", "hosts", len(hosts))
	cmds := make([]tea.Cmd, 0, len(hosts)+1)
	for _, host := range hosts {
		hostCfg := s.cfg.Hosts[host]
		if hostCfg == nil {
			s.appliedFail++
			continue
		}
		sshCfg := internalssh.Config{
			Address: hostCfg.SSHAddress(s.cfg.Settings.Username),
			KeyPath: hostCfg.ResolvedSSHKey(s.cfg.Settings.SSHKey),
		}
		cmds = append(cmds, SequenceCmds(
			DockerExecCmd(s.ctx, sshCfg, "docker image prune -f", "image.prune", host),
		))
	}
	cmds = append(cmds, s.spinner.Tick)
	return tea.Batch(cmds...)
}

// uniqueAppliedHosts returns the distinct host names involved in the
// applied stack set, derived from the selection. Used to bound the
// post-apply prune to exactly the hosts we just touched.
func (s *updatesScreen) uniqueAppliedHosts() []string {
	seen := make(map[string]bool)
	var out []string
	for i, r := range s.results {
		if !s.selected[i] || !r.HasUpdate {
			continue
		}
		if seen[r.Host] {
			continue
		}
		seen[r.Host] = true
		out = append(out, r.Host)
	}
	return out
}

// finishAndRecheck resets the apply + prune state and kicks off a fresh
// registry check so the list reflects post-update reality.
func (s *updatesScreen) finishAndRecheck() tea.Cmd {
	s.phase = phaseUpdatesLoading
	s.results = nil
	s.selected = make(map[int]bool)
	s.cursor = 0
	s.checked = 0
	s.total = 0
	s.appliedOk = 0
	s.appliedFail = 0
	s.pruning = false
	s.prunePending = 0
	return tea.Batch(s.spinner.Tick, s.buildCheckerCmd())
}

// pendingApply returns how many stacks we tried to update in the current
// apply phase — used to detect completion.
func (s *updatesScreen) pendingApply() int {
	type groupKey struct{ host, stack string }
	seen := make(map[groupKey]bool)
	n := 0
	for i, r := range s.results {
		if !s.selected[i] || !r.HasUpdate {
			continue
		}
		k := groupKey{host: r.Host, stack: r.Stack}
		if seen[k] {
			continue
		}
		seen[k] = true
		n++
	}
	return n
}

// ── Small helpers ──────────────────────────────────────────────────────────

func emptyToDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// renderBar draws a fixed-width ASCII progress bar. We don't use v2's native
// ProgressBar here because it renders in the terminal chrome (dock icon, tab
// badge) — we still want an in-panel visual so users on terminals without
// chrome-progress support see progress too.
func renderBar(width int, frac float64) string {
	if width <= 0 {
		return ""
	}
	if frac < 0 {
		frac = 0
	} else if frac > 1 {
		frac = 1
	}
	filled := min(int(float64(width)*frac), width)
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// config import is retained so we reference it — currently unused here but
// newUpdatesScreen signature uses it for consistency with the other
// constructors.
var _ = config.Load
