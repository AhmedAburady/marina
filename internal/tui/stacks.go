package tui

import (
	"context"
	"fmt"
	"image/color"
	"sort"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/AhmedAburady/marina/internal/actions"
	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/discovery"
	internalssh "github.com/AhmedAburady/marina/internal/ssh"
)

// CLI-matching semantic colours for stack rows. Values lifted verbatim
// from internal/ui/table.go so `marina stacks` and the Stacks screen tint
// stopped / degraded stacks identically.
var (
	cStackStopped  = lipgloss.Color("183") // muted mauve / pink
	cStackDegraded = lipgloss.Color("214") // amber / orange
)

// stackRowColor returns the fg override for one row: stopped, degraded,
// or nil (use default). Mirrors the switch in PrintStackTable's StyleFunc.
func stackRowColor(r stackRow) color.Color {
	switch {
	case r.total == 0 || r.running == 0:
		return cStackStopped
	case r.running < r.total:
		return cStackDegraded
	}
	return nil
}

// stackRow is one Compose project across all hosts.
type stackRow struct {
	host    string
	name    string
	dir     string
	running int
	total   int
	sshCfg  internalssh.Config
}

// stacksMode switches between the plain list and inline dialogs (add, purge,
// unregister). Only one dialog is ever active; the list is paused while a
// dialog owns the keys.
type stacksMode int

const (
	stacksModeList    stacksMode = iota
	stacksModeAdd                // register a new stack on the current host
	stacksModeConfirm            // generic y/n confirm (purge or unregister)
)

// stacksScreen aggregates every running Compose stack (and config-registered
// stacks that are fully stopped) across all hosts.
type stacksScreen struct {
	ctx     context.Context
	cfg     *config.Config
	rows    []stackRow // unfiltered
	visible []stackRow // post-filter — cursor + actions index into this
	cursor  int
	loading bool
	err     error
	pending map[string]bool
	errors  map[string]string
	spinner spinner.Model
	mode    stacksMode
	prompt  *confirmPrompt
	form    *inlineForm
	filter  filterBar
	notice  string
	sb      scrollBody
}

func newStacksScreen(ctx context.Context, cfg *config.Config) *stacksScreen {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = sSpinner
	return &stacksScreen{
		ctx:     ctx,
		cfg:     cfg,
		loading: true,
		pending: make(map[string]bool),
		errors:  make(map[string]string),
		spinner: sp,
		sb:      newScrollBody(),
		filter:  newFilterBar(),
	}
}

func (s *stacksScreen) Title() string { return "Stacks" }

func (s *stacksScreen) Init() tea.Cmd {
	return tea.Batch(
		FetchAllHostsCmd(s.ctx, s.cfg, s.cfg.Hosts),
		s.spinner.Tick,
	)
}

func (s *stacksScreen) Help() string {
	switch s.mode {
	case stacksModeAdd:
		return "tab move · enter save · esc cancel"
	case stacksModeConfirm:
		return "←/→ select · enter confirm · esc cancel"
	}
	if s.filter.Active() {
		return "type to filter · enter apply · esc clear"
	}
	return "↑/↓ move · / filter · s start · x stop · r restart · p pull · u update · a add · d remove · P purge · R refresh · esc back"
}

func (s *stacksScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	// Modal routes take key precedence so background data still streams in.
	switch s.mode {
	case stacksModeAdd:
		if s.form != nil {
			if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
				submit, cancel, cmd := s.form.Update(keyMsg)
				switch {
				case cancel:
					s.mode = stacksModeList
					s.form = nil
				case submit:
					if err := s.commitRegister(); err != nil {
						s.form.err = err.Error()
						return s, cmd
					}
					s.mode = stacksModeList
					s.form = nil
				}
				return s, cmd
			}
		}
	case stacksModeConfirm:
		if s.prompt != nil {
			done, cmd := s.prompt.Update(msg)
			if done {
				s.prompt = nil
				s.mode = stacksModeList
			}
			if _, isKey := msg.(tea.KeyPressMsg); isKey {
				return s, cmd
			}
		}
	}

	// Filter bar takes all key input while editing so list shortcuts like
	// `s` / `x` / `p` don't fire during filter typing.
	if handled, cmd := s.filter.Update(msg); handled {
		s.rebuildVisible()
		return s, cmd
	}

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "left":
			if s.filter.HasQuery() {
				s.filter.Clear()
				s.rebuildVisible()
				return s, nil
			}
			return s, popCmd()
		case "up", "k":
			if s.cursor > 0 {
				s.cursor--
			}
		case "down", "j":
			if s.cursor < len(s.visible)-1 {
				s.cursor++
			}
		case "/":
			return s, s.filter.Activate()
		case "R":
			s.loading = true
			return s, tea.Batch(
				FetchAllHostsCmd(s.ctx, s.cfg, s.cfg.Hosts),
				s.spinner.Tick,
			)
		case "s":
			return s, s.runCompose("up -d", "compose.up")
		case "x":
			return s, s.runCompose("stop", "compose.stop")
		case "r":
			return s, s.runCompose("restart", "compose.restart")
		case "p":
			return s, s.runCompose("pull", "compose.pull")
		case "u":
			return s, s.runUpdate()
		case "a":
			s.openAddForm()
		case "d":
			s.openUnregisterPrompt()
		case "P":
			s.openPurgePrompt()
		}

	case HostsFetchedMsg:
		s.loading = false
		s.err = nil
		s.buildRows(msg.Results)

	case ActionResultMsg:
		delete(s.pending, msg.Target)
		if msg.Err != nil {
			s.errors[msg.Target] = firstLineOf(msg.Err)
		} else {
			delete(s.errors, msg.Target)
			return s, FetchAllHostsCmd(s.ctx, s.cfg, s.cfg.Hosts)
		}

	case SequenceResultsMsg:
		// Multi-step actions (update, purge). Unpack per-step results.
		for _, r := range msg.Results {
			delete(s.pending, r.Target)
			if r.Err != nil {
				s.errors[r.Target] = firstLineOf(r.Err)
			}
		}
		// After a successful full sequence, drop any prior error + refresh.
		if lastOk(msg.Results) {
			last := msg.Results[len(msg.Results)-1]
			delete(s.errors, last.Target)
			// If the last step was image prune for a purge, also tidy the
			// config entry locally.
			if last.Kind == "image.prune" {
				s.removePurgedFromConfig(last.Target)
			}
			return s, FetchAllHostsCmd(s.ctx, s.cfg, s.cfg.Hosts)
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		if !s.loading && len(s.pending) == 0 {
			return s, nil
		}
		return s, cmd

	}
	return s, nil
}

func (s *stacksScreen) View(width, height int) string {
	// Like the Hosts screen, we always render the list as the background.
	// The dashboard composites the active modal on top via ModalProvider.
	if s.loading {
		return panelLines(width, height, []string{
			spacer(width),
			pendingNote(width, s.spinner.View(), "loading stacks…"),
		})
	}
	if s.err != nil {
		return panelLines(width, height, []string{
			spacer(width),
			errorNote(width, firstLineOf(s.err)),
		})
	}
	if len(s.rows) == 0 {
		return panelLines(width, height, []string{
			spacer(width),
			mutedLine(width, "No stacks found on any host."),
		})
	}

	// Fixed top / bottom rows; everything else scrolls.
	var top []string
	top = append(top, spacer(width))
	if bar := s.filter.View(width, len(s.visible)); bar != "" {
		top = append(top, bar)
		top = append(top, spacer(width))
	}

	var bottom []string
	if len(s.pending) > 0 {
		bottom = append(bottom, spacer(width))
		bottom = append(bottom, pendingNote(width, s.spinner.View(),
			fmt.Sprintf("%d action(s) in flight…", len(s.pending))))
	}
	if r := s.currentRow(); r != nil {
		if msg, ok := s.errors[keyOf(r.host, r.name)]; ok {
			bottom = append(bottom, spacer(width))
			bottom = append(bottom, errorNote(width, "last error: "+msg))
		}
	}

	// CLI parity: STACK, DIR, STATUS — same columns, same colour rules.
	// Pending/error indicator is prefixed to STATUS; row colour mirrors
	// internal/ui/table.go (stopped → muted mauve, degraded → amber).
	inner := innerWidth(width)
	widths := shareWidths(inner,
		[]int{3, 5, 2},
		[]int{14, 24, 12},
	)
	all := make([][]string, 0, len(s.visible))
	colors := make([]color.Color, 0, len(s.visible))
	for _, r := range s.visible {
		k := keyOf(r.host, r.name)
		pend, bad := rowHas(s.pending, s.errors, k)
		state := stackStateText(r)
		if m := markFor(pend, bad); m != "" {
			state = m + " " + state
		}
		all = append(all, []string{r.host, r.name, shortPath(r.dir), state})
		colors = append(colors, stackRowColor(r))
	}
	groups := groupRows(all, 0)
	// Re-attach the per-row colour slice to each group — groupRows strips
	// the group column but doesn't know about RowColors. Walk the groups
	// in order and slice `colors` to match each one's rows.
	cursor := 0
	for gi := range groups {
		n := len(groups[gi].Rows)
		groups[gi].RowColors = colors[cursor : cursor+n]
		cursor += n
	}
	content, cursorLine, headerLine := groupedLines(width,
		[]string{"STACK", "DIR", "STATUS"},
		widths, groups, s.cursor)

	bodyHeight := max0(height - len(top) - len(bottom))
	body := viewportBody(&s.sb, width, bodyHeight, content, cursorLine, headerLine)
	lines := append(append(top, body...), bottom...)
	return panelLines(width, height, lines)
}

// Modal implements ModalProvider. Returns the active add/confirm dialog so
// the dashboard composites it over the stacks list.
func (s *stacksScreen) Modal() (string, bool) {
	switch s.mode {
	case stacksModeAdd:
		if s.form == nil {
			return "", false
		}
		return s.form.Modal(), true
	case stacksModeConfirm:
		if s.prompt == nil {
			return "", false
		}
		return s.prompt.View(), true
	}
	return "", false
}

// ── Internals ───────────────────────────────────────────────────────────────

func (s *stacksScreen) buildRows(results map[string]HostFetchResult) {
	// Sort hosts alphabetically, but PRESERVE the per-host ordering that
	// discovery.GroupByStack returns (running stacks first, stopped last).
	// A top-level sort by name would break that invariant and we'd lose
	// CLI parity — stopped stacks would interleave with running ones.
	hosts := make([]string, 0, len(results))
	for h := range results {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)

	var out []stackRow
	for _, host := range hosts {
		res := results[host]
		if res.Err != nil {
			out = append(out, stackRow{host: host, name: "(unreachable)"})
			continue
		}
		hostCfg := s.cfg.Hosts[host]
		if hostCfg == nil {
			continue
		}
		sshCfg := internalssh.Config{
			Address: hostCfg.SSHAddress(s.cfg.Settings.Username),
			KeyPath: hostCfg.ResolvedSSHKey(s.cfg.Settings.SSHKey),
		}
		for _, st := range discovery.GroupByStack(host, res.Containers, hostCfg.Stacks) {
			out = append(out, stackRow{
				host:    host,
				name:    st.Name,
				dir:     st.Dir,
				running: st.Running,
				total:   st.Total,
				sshCfg:  sshCfg,
			})
		}
	}
	s.rows = out
	s.rebuildVisible()
}

// rebuildVisible applies the current filter to the raw stack list.
func (s *stacksScreen) rebuildVisible() {
	s.visible = s.visible[:0]
	for _, r := range s.rows {
		if s.filter.Match(r.host, r.name, r.dir) {
			s.visible = append(s.visible, r)
		}
	}
	if s.cursor >= len(s.visible) {
		s.cursor = max0(len(s.visible) - 1)
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

func (s *stacksScreen) currentRow() *stackRow {
	if s.cursor < 0 || s.cursor >= len(s.visible) {
		return nil
	}
	return &s.visible[s.cursor]
}

func (s *stacksScreen) runCompose(subCmd, kind string) tea.Cmd {
	r := s.currentRow()
	if r == nil || r.dir == "" {
		return nil
	}
	key := r.host + "/" + r.name
	s.pending[key] = true
	delete(s.errors, key)
	return tea.Batch(
		ComposeExecCmd(r.sshCfg, r.dir, subCmd, kind, key),
		s.spinner.Tick,
	)
}

// runUpdate chains pull + up -d. A failed pull short-circuits the up call
// thanks to SequenceCmds' stop-on-error semantics.
func (s *stacksScreen) runUpdate() tea.Cmd {
	r := s.currentRow()
	if r == nil || r.dir == "" {
		return nil
	}
	key := r.host + "/" + r.name
	s.pending[key] = true
	delete(s.errors, key)
	return tea.Batch(
		SequenceCmds(
			ComposeExecCmd(r.sshCfg, r.dir, "pull", "compose.pull", key),
			ComposeExecCmd(r.sshCfg, r.dir, "up -d", "compose.up", key),
		),
		s.spinner.Tick,
	)
}

// openPurgePrompt stages a confirm modal for the destructive purge sequence.
func (s *stacksScreen) openPurgePrompt() {
	r := s.currentRow()
	if r == nil || r.dir == "" {
		return
	}
	s.prompt = &confirmPrompt{
		title: fmt.Sprintf("Purge stack %q on %q?", r.name, r.host),
		details: []string{
			"This will permanently:",
			"  1. Stop and remove all containers (docker compose down)",
			fmt.Sprintf("  2. Delete %s on the remote host", r.dir),
			"  3. Unregister the stack from marina config",
			"",
			"This cannot be undone.",
		},
		confirmLabel: "Purge",
		focus:        0, // default to Cancel — safer for destructive ops
		onYes:        s.buildPurgeCmd(),
	}
	s.mode = stacksModeConfirm
}

// openAddForm opens the inline register-stack form (name + working dir for
// the stack on the CURRENT highlighted row's host). Register-on-different-host
// flows go through the Hosts screen first.
func (s *stacksScreen) openAddForm() {
	// Pick a target host. Prefer the current row's host; fall back to the
	// first configured host; bail if there are none.
	var host string
	if r := s.currentRow(); r != nil {
		host = r.host
	}
	if host == "" {
		for name := range s.cfg.Hosts {
			host = name
			break
		}
	}
	if host == "" {
		s.notice = "No hosts configured — add a host first."
		return
	}
	s.form = newInlineForm(
		fmt.Sprintf("Register stack on %s", host),
		[]formField{
			newFormField("host", host, true),
			newFormField("name", "compose project name", true),
			newFormField("dir", "absolute remote path", true),
		},
	)
	s.form.fields[0].input.SetValue(host)
	s.mode = stacksModeAdd
}

// commitRegister writes the stack entry to config via actions.RegisterStack
// and refreshes the list so the new row shows up immediately.
func (s *stacksScreen) commitRegister() error {
	host := s.form.Value(0)
	name := s.form.Value(1)
	dir := s.form.Value(2)
	if err := actions.RegisterStack(s.cfg, "", host, name, dir); err != nil {
		return err
	}
	s.notice = fmt.Sprintf("Registered stack %q on %q", name, host)
	// Re-fetch so discovery picks up the new entry.
	return nil
}

// openUnregisterPrompt confirms removing the highlighted stack from the
// config (no remote docker action — containers untouched).
func (s *stacksScreen) openUnregisterPrompt() {
	r := s.currentRow()
	if r == nil {
		return
	}
	captured := *r
	s.prompt = &confirmPrompt{
		title: fmt.Sprintf("Unregister stack %q from %q?", captured.name, captured.host),
		details: []string{
			"This removes the stack entry from marina config.",
			"Containers on the remote host are NOT stopped or deleted.",
			"Use P instead to fully purge containers + files + config.",
		},
		confirmLabel: "Unregister",
		focus:        0,
		onYes: func() tea.Cmd {
			removed, _, err := actions.UnregisterStacks(s.cfg, "", captured.host, captured.name)
			if err != nil {
				s.notice = "error: " + firstLineOf(err)
			} else if len(removed) > 0 {
				s.notice = fmt.Sprintf("Unregistered stack %q from %q", captured.name, captured.host)
			}
			return FetchAllHostsCmd(s.ctx, s.cfg, s.cfg.Hosts)
		},
	}
	s.mode = stacksModeConfirm
}

// buildPurgeCmd captures the current row and builds the multi-step purge.
// The closure defers row lookup so the user's focus at confirm-time is used.
func (s *stacksScreen) buildPurgeCmd() func() tea.Cmd {
	r := s.currentRow()
	if r == nil || r.dir == "" {
		return func() tea.Cmd { return nil }
	}
	captured := *r
	key := captured.host + "/" + captured.name
	return func() tea.Cmd {
		s.pending[key] = true
		delete(s.errors, key)
		return tea.Batch(
			SequenceCmds(
				ComposeExecCmd(captured.sshCfg, captured.dir, "down --remove-orphans", "compose.down", key),
				DockerExecCmd(captured.sshCfg, "rm -rf "+shellQuote(captured.dir), "dir.rm", key),
				DockerExecCmd(captured.sshCfg, "docker image prune -f", "image.prune", key),
			),
			s.spinner.Tick,
		)
	}
}

// removePurgedFromConfig strips the just-purged stack from the local config
// file, mirroring the last step of commands/stacks.go:newStacksPurgeCmd.
func (s *stacksScreen) removePurgedFromConfig(key string) {
	host, name := splitKey(key)
	if host == "" || name == "" {
		return
	}
	h, ok := s.cfg.Hosts[host]
	if !ok || h.Stacks == nil {
		return
	}
	if _, ok := h.Stacks[name]; !ok {
		return
	}
	delete(h.Stacks, name)
	_ = config.Save(s.cfg, "") // best-effort; surface as error on next refresh if it failed
}

// ── Helpers ────────────────────────────────────────────────────────────────

func stackStateText(r stackRow) string {
	switch {
	case r.total == 0:
		return "stopped"
	case r.running == r.total:
		return fmt.Sprintf("%d running", r.total)
	case r.running == 0:
		return "stopped"
	default:
		return fmt.Sprintf("%d/%d running", r.running, r.total)
	}
}

// lastOk returns true when the sequence completed all its steps without an
// error on the final one.
func lastOk(results []ActionResultMsg) bool {
	if len(results) == 0 {
		return false
	}
	return results[len(results)-1].Err == nil
}

// splitKey parses "host/stack" back into its parts.
func splitKey(key string) (host, name string) {
	before, after, ok := strings.Cut(key, "/")
	if !ok {
		return "", ""
	}
	return before, after
}

// shellQuote wraps a path in single quotes for safe use in a remote shell.
// Paths with embedded quotes are rejected (returned empty) so we never issue
// a malformed command.
func shellQuote(s string) string {
	if strings.ContainsAny(s, "'\"\\") {
		return ""
	}
	return "'" + s + "'"
}
