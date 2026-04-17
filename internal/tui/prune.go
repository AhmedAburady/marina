package tui

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/AhmedAburady/marina/internal/actions"
	"github.com/AhmedAburady/marina/internal/config"
	internalssh "github.com/AhmedAburady/marina/internal/ssh"
	"github.com/AhmedAburady/marina/internal/strutil"
)

// ── Scope ──────────────────────────────────────────────────────────────────
//
// Scope values map 1:1 to `marina prune`'s CLI flags. Ordering is the same
// as the flag docs so the toggle row reads in the order users already know.

type pruneScope int

const (
	pruneScopeSystem     pruneScope = iota // docker system prune -f (default)
	pruneScopeImagesOnly                   // --images-only  → docker image prune -f
	pruneScopeImagesAll                    // --images-all   → docker image prune -af
	pruneScopeVolumes                      // --volumes      → docker volume prune -f
)

func (p pruneScope) toOptions() actions.PruneOptions {
	switch p {
	case pruneScopeImagesOnly:
		return actions.PruneOptions{ImagesOnly: true}
	case pruneScopeImagesAll:
		return actions.PruneOptions{ImagesAll: true}
	case pruneScopeVolumes:
		return actions.PruneOptions{Volumes: true}
	default:
		return actions.PruneOptions{}
	}
}

// Label maps each scope to the short toggle-row label + the longer summary
// used in the confirm modal. Kept alongside the enum so a new scope only
// needs changes in one place.
var pruneScopeLabels = []struct {
	short string
	long  string
}{
	{"system", "stopped containers, unused networks, dangling images, build cache"},
	{"images-only", "dangling (untagged) images"},
	{"images-all", "ALL unused images (dangling + tagged)"},
	{"volumes", "unused volumes"},
}

// ── Result parsing ─────────────────────────────────────────────────────────

// pruneResult captures a single host's outcome after the remote prune
// finishes. Reclaimed is parsed out of docker's "Total reclaimed space:"
// trailer so the STATUS column can show a meaningful one-liner instead of
// dumping stdout.
type pruneResult struct {
	output    string
	reclaimed string
	err       error
}

var reclaimedRe = regexp.MustCompile(`(?i)Total reclaimed space:\s*(.+)`)

func parseReclaimed(out string) string {
	m := reclaimedRe.FindStringSubmatch(out)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// ── Messages ───────────────────────────────────────────────────────────────

type pruneDoneMsg struct {
	host   string
	output string
	err    error
}

// pruneExecCmd runs the prune on one host through the shared actions
// package. Every apply path (CLI + TUI) eventually funnels into
// actions.Prune so we can't drift: changing the command in one place moves
// both callers together.
func pruneExecCmd(ctx context.Context, sshCfg internalssh.Config, host string, opts actions.PruneOptions) tea.Cmd {
	return func() tea.Msg {
		Log().Info("prune.start", "host", host, "cmd", actions.PruneCommand(opts))
		out, err := actions.Prune(ctx, sshCfg, opts)
		if err != nil {
			Log().Warn("prune.fail", "host", host, "err", shortenErr(err, 200), "out", strutil.FirstLine(out, 200))
		} else {
			Log().Info("prune.ok", "host", host, "out", strutil.FirstLine(out, 200))
		}
		return pruneDoneMsg{host: host, output: out, err: err}
	}
}

// ── Screen ─────────────────────────────────────────────────────────────────

type pruneMode int

const (
	pruneModeList pruneMode = iota
	pruneModeConfirm
)

type pruneScreen struct {
	ctx context.Context
	cfg *config.Config

	hosts    []string        // sorted host names from config (unfiltered)
	visible  []string        // post-filter view — cursor indexes into this
	cursor   int             // current host in visible
	selected map[string]bool // host names opted in for pruning

	scope pruneScope

	pending map[string]bool        // hosts mid-flight
	results map[string]pruneResult // completed results (ok or error)

	mode    pruneMode
	prompt  *confirmPrompt
	spinner spinner.Model
	sb      scrollBody
	filter  filterBar
	notice  string
}

func newPruneScreen(ctx context.Context, cfg *config.Config) *pruneScreen {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = sSpinner

	names := slices.Sorted(maps.Keys(cfg.Hosts))

	// Default: every host selected. Matches --all in the CLI and keeps the
	// common "clean everything" flow to one keystroke.
	selected := make(map[string]bool, len(names))
	for _, n := range names {
		selected[n] = true
	}

	s := &pruneScreen{
		ctx:      ctx,
		cfg:      cfg,
		hosts:    names,
		selected: selected,
		pending:  make(map[string]bool),
		results:  make(map[string]pruneResult),
		spinner:  sp,
		sb:       newScrollBody(),
		filter:   newFilterBar(),
	}
	s.rebuildVisible()
	return s
}

func (s *pruneScreen) Title() string { return "Prune" }
func (s *pruneScreen) Init() tea.Cmd { return nil }

func (s *pruneScreen) Help() string {
	if s.mode == pruneModeConfirm {
		return "←/→ select · enter confirm · esc cancel"
	}
	if s.filter.Active() {
		return "type to filter · enter apply · esc clear"
	}
	return "↑/↓ move · / filter · space toggle · a all · 1-4 scope · enter run · esc back"
}

func (s *pruneScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if s.mode == pruneModeConfirm && s.prompt != nil {
		done, cmd := s.prompt.Update(msg)
		if done {
			s.prompt = nil
			s.mode = pruneModeList
		}
		if _, isKey := msg.(tea.KeyPressMsg); isKey {
			return s, cmd
		}
	}

	// Filter bar owns keys while editing.
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
		case "space", " ":
			if h := s.currentHost(); h != "" {
				s.selected[h] = !s.selected[h]
			}
		case "a":
			s.toggleAll()
		case "1":
			s.scope = pruneScopeSystem
		case "2":
			s.scope = pruneScopeImagesOnly
		case "3":
			s.scope = pruneScopeImagesAll
		case "4":
			s.scope = pruneScopeVolumes
		case "R":
			s.results = make(map[string]pruneResult)
			s.notice = ""
		case "enter":
			s.openConfirm()
		}

	case pruneDoneMsg:
		delete(s.pending, msg.host)
		s.results[msg.host] = pruneResult{
			output:    msg.output,
			reclaimed: parseReclaimed(msg.output),
			err:       msg.err,
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		if len(s.pending) == 0 {
			return s, nil
		}
		return s, cmd
	}
	return s, nil
}

func (s *pruneScreen) View(width, height int) string {
	if len(s.hosts) == 0 {
		return panelLines(width, height, []string{
			spacer(width),
			mutedLine(width, "No hosts configured."),
			mutedLine(width, "Add one with: marina hosts add <name> <address>"),
		})
	}

	var top []string
	top = append(top, spacer(width))
	top = append(top, s.scopeRow(width))
	top = append(top, spacer(width))
	if bar := s.filter.View(width, len(s.visible)); bar != "" {
		top = append(top, bar)
		top = append(top, spacer(width))
	}
	if s.notice != "" {
		top = append(top, notice(width, s.notice))
		top = append(top, spacer(width))
	}

	var bottom []string
	if len(s.pending) > 0 {
		bottom = append(bottom, spacer(width))
		bottom = append(bottom, pendingNote(width, s.spinner.View(),
			fmt.Sprintf("pruning %d host(s)…", len(s.pending))))
	}

	// Columns: SEL, HOST, STATUS. SEL is a two-char cell (✓ or blank) so
	// the list double-renders as the multi-select picker and the result
	// summary; STATUS shows idle / pending / reclaimed / error.
	inner := innerWidth(width)
	widths := shareWidths(inner,
		[]int{1, 3, 4},
		[]int{3, 12, 16},
	)
	rows := make([][]string, 0, len(s.visible))
	for _, h := range s.visible {
		mark := "  "
		if s.selected[h] {
			mark = "✓ "
		}
		rows = append(rows, []string{mark, h, s.hostStatusCell(h)})
	}
	content, cursorLine, headerLine := flatLines(width,
		[]string{"SEL", "HOST", "STATUS"},
		widths, rows, s.cursor)

	bodyHeight := max0(height - len(top) - len(bottom))
	body := viewportBody(&s.sb, width, bodyHeight, content, cursorLine, headerLine)
	lines := append(append(top, body...), bottom...)
	return panelLines(width, height, lines)
}

// Modal implements ModalProvider. Only the confirm dialog floats on top of
// the list — the scope row and host picker are always on the background.
func (s *pruneScreen) Modal() (string, bool) {
	if s.mode == pruneModeConfirm && s.prompt != nil {
		return s.prompt.View(), true
	}
	return "", false
}

// ── Layout helpers ─────────────────────────────────────────────────────────

// scopeRow renders the four-option toggle. The active option is highlighted
// with the accent pill; the others render as dim labels. Keys 1..4 jump
// directly, so the row doubles as a legend.
func (s *pruneScreen) scopeRow(width int) string {
	var parts []string
	for i, lbl := range pruneScopeLabels {
		idx := fmt.Sprintf("%d", i+1)
		text := idx + " " + lbl.short
		parts = append(parts, renderPill(text, pruneScope(i) == s.scope))
	}
	row := strings.Join(parts, "  ")
	return panelLine(sPanel, width, row)
}

// hostStatusCell returns the right-hand STATUS cell for one host row.
// Priority (high → low): in-flight spinner → last result (error or freed) →
// idle dash.
func (s *pruneScreen) hostStatusCell(h string) string {
	if s.pending[h] {
		return "pruning…"
	}
	if r, ok := s.results[h]; ok {
		if r.err != nil {
			return "error: " + strutil.FirstLine(r.err.Error(), 40)
		}
		if r.reclaimed != "" {
			return "freed " + r.reclaimed
		}
		return "done"
	}
	return "–"
}

func (s *pruneScreen) currentHost() string {
	if s.cursor < 0 || s.cursor >= len(s.visible) {
		return ""
	}
	return s.visible[s.cursor]
}

// rebuildVisible applies the current filter to the host list. The selection
// state is keyed on host name, not index, so it survives filter toggles
// without extra bookkeeping.
func (s *pruneScreen) rebuildVisible() {
	s.visible = s.visible[:0]
	for _, h := range s.hosts {
		if s.filter.Match(h) {
			s.visible = append(s.visible, h)
		}
	}
	if s.cursor >= len(s.visible) {
		s.cursor = max0(len(s.visible) - 1)
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

func (s *pruneScreen) toggleAll() {
	allOn := true
	for _, h := range s.hosts {
		if !s.selected[h] {
			allOn = false
			break
		}
	}
	for _, h := range s.hosts {
		if allOn {
			delete(s.selected, h)
		} else {
			s.selected[h] = true
		}
	}
}

// ── Confirm + apply ────────────────────────────────────────────────────────

// openConfirm stages the pre-run confirmation. Title mirrors the CLI's
// `--force` prompt so users see the exact resources scheduled for removal
// and which hosts are affected; the confirm label is always "Prune" so
// there is no ambiguity about the destructive step.
func (s *pruneScreen) openConfirm() {
	selected := s.selectedHosts()
	if len(selected) == 0 {
		s.notice = "No hosts selected."
		return
	}
	lbl := pruneScopeLabels[s.scope]
	details := []string{
		"This will remove " + lbl.long + " on:",
		"  " + strings.Join(selected, ", "),
		"",
		"Command: " + actions.PruneCommand(s.scope.toOptions()),
	}
	s.prompt = &confirmPrompt{
		title:        "Prune Docker resources?",
		details:      details,
		confirmLabel: "Prune",
		focus:        0, // default to Cancel on destructive ops
		onYes:        s.buildApply(selected),
	}
	s.mode = pruneModeConfirm
}

func (s *pruneScreen) buildApply(selected []string) func() tea.Cmd {
	opts := s.scope.toOptions()
	return func() tea.Cmd {
		s.results = make(map[string]pruneResult)
		cmds := make([]tea.Cmd, 0, len(selected)+1)
		for _, host := range selected {
			hc := s.cfg.Hosts[host]
			if hc == nil {
				continue
			}
			sshCfg := internalssh.Config{
				Address: hc.SSHAddress(s.cfg.Settings.Username),
				KeyPath: hc.ResolvedSSHKey(s.cfg.Settings.SSHKey),
			}
			s.pending[host] = true
			cmds = append(cmds, pruneExecCmd(s.ctx, sshCfg, host, opts))
		}
		if len(cmds) == 0 {
			return nil
		}
		cmds = append(cmds, s.spinner.Tick)
		return tea.Batch(cmds...)
	}
}

func (s *pruneScreen) selectedHosts() []string {
	out := make([]string, 0, len(s.hosts))
	for _, h := range s.hosts {
		if s.selected[h] {
			out = append(out, h)
		}
	}
	return out
}
