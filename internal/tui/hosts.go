package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/AhmedAburady/marina/internal/actions"
	"github.com/AhmedAburady/marina/internal/config"
)

// hostTestResultMsg carries the outcome of one SSH probe.
type hostTestResultMsg struct {
	host    string
	ok      bool
	latency time.Duration
	err     error
}

// hostTestState holds the transient test status for one row. `pending` is
// true while the probe is in flight; `done` + `ok`/`err` carry the result.
type hostTestState struct {
	pending bool
	done    bool
	ok      bool
	latency time.Duration
	err     error
}

// hostsMode toggles between the plain list and the inline add / confirm
// dialogs so all three renders can share the same parent screen.
type hostsMode int

const (
	hostsModeList hostsMode = iota
	hostsModeAdd
	hostsModeConfirmDelete
)

// hostsScreen is the "Hosts" destination: list, test, add, remove.
type hostsScreen struct {
	ctx     context.Context
	cfg     *config.Config
	rows    []actions.HostRow // unfiltered
	visible []actions.HostRow // post-filter — cursor + actions index into this
	cursor  int
	states  map[string]*hostTestState
	pending int
	spinner spinner.Model
	mode    hostsMode
	form    *inlineForm
	prompt  *confirmPrompt
	filter  filterBar
	notice  string
	sb      scrollBody
}

func newHostsScreen(ctx context.Context, cfg *config.Config) *hostsScreen {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = sSpinner
	s := &hostsScreen{
		ctx:     ctx,
		cfg:     cfg,
		states:  make(map[string]*hostTestState),
		spinner: sp,
		sb:      newScrollBody(),
		filter:  newFilterBar(),
	}
	s.refresh()
	return s
}

func (s *hostsScreen) Title() string         { return "Hosts" }
func (s *hostsScreen) Init() tea.Cmd         { return nil }

func (s *hostsScreen) Help() string {
	switch s.mode {
	case hostsModeAdd:
		return "tab move · enter save · esc cancel"
	case hostsModeConfirmDelete:
		return "←/→ select · enter confirm · esc cancel"
	}
	if s.filter.Active() {
		return "type to filter · enter apply · esc clear"
	}
	return "↑/↓ move · / filter · t test · a add · d delete · R refresh · esc back"
}

func (s *hostsScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	// Modal path: form or confirm owns the keys.
	switch s.mode {
	case hostsModeAdd:
		if s.form != nil {
			submit, cancel, cmd := s.form.Update(msg)
			switch {
			case cancel:
				s.mode = hostsModeList
				s.form = nil
			case submit:
				if err := s.commitAdd(); err != nil {
					s.form.err = err.Error()
					return s, cmd
				}
				s.mode = hostsModeList
				s.form = nil
			}
			return s, cmd
		}
	case hostsModeConfirmDelete:
		if s.prompt != nil {
			done, cmd := s.prompt.Update(msg)
			if done {
				s.prompt = nil
				s.mode = hostsModeList
			}
			if _, isKey := msg.(tea.KeyPressMsg); isKey {
				return s, cmd
			}
		}
		return s, nil
	}

	// Filter bar owns keys while editing.
	if handled, cmd := s.filter.Update(msg); handled {
		s.rebuildVisible()
		return s, cmd
	}

	// List-mode key handling.
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
		case "t":
			return s, s.startTests()
		case "R":
			s.refresh()
		case "a":
			s.openAddForm()
		case "d":
			s.openDeletePrompt()
		}

	case hostTestResultMsg:
		st, ok := s.states[msg.host]
		if !ok {
			st = &hostTestState{}
			s.states[msg.host] = st
		}
		if st.pending {
			s.pending--
		}
		st.pending = false
		st.done = true
		st.ok = msg.ok
		st.latency = msg.latency
		st.err = msg.err

	case spinner.TickMsg:
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		if s.pending == 0 {
			return s, nil
		}
		return s, cmd
	}
	return s, nil
}

func (s *hostsScreen) View(width, height int) string {
	// The list is always rendered as the background; the modal (when any)
	// is composited on top by the dashboard via ModalProvider. This keeps
	// the dialog small and lets the user still see the list context behind.
	return s.viewList(width, height)
}

// Modal implements ModalProvider. When an add-host or delete-confirm flow
// is open, the dashboard overlays this string over the list view.
func (s *hostsScreen) Modal() (string, bool) {
	switch s.mode {
	case hostsModeAdd:
		if s.form == nil {
			return "", false
		}
		return s.form.Modal(), true
	case hostsModeConfirmDelete:
		if s.prompt == nil {
			return "", false
		}
		return s.prompt.View(), true
	}
	return "", false
}

// viewList renders the Hosts table. Every row / header / spacer / notice
// goes through the helpers in list.go — this function owns the data + key
// bindings, not the layout.
func (s *hostsScreen) viewList(width, height int) string {
	if len(s.rows) == 0 {
		return panelLines(width, height, []string{
			spacer(width),
			mutedLine(width, "No hosts configured."),
			mutedLine(width, "Press `a` to add one."),
		})
	}

	var top []string
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
	if s.pending > 0 {
		bottom = append(bottom, spacer(width))
		bottom = append(bottom, pendingNote(width, s.spinner.View(),
			fmt.Sprintf("testing %d host(s)…", s.pending)))
	}

	// NAME, USER, ADDRESS, KEY, STATUS. STATUS shows the per-row test
	// probe state — idle (–), in-flight (testing…), ok (latency), or the
	// connection error — so users see results in the row itself instead
	// of a single opaque summary line.
	inner := innerWidth(width)
	widths := shareWidths(inner,
		[]int{2, 2, 3, 3, 2},
		[]int{10, 10, 14, 16, 12},
	)
	all := make([][]string, 0, len(s.visible))
	for _, r := range s.visible {
		all = append(all, []string{r.Name, r.User, r.Address, shortPath(r.Key), s.statusCell(r.Name)})
	}
	content, cursorLine, headerLine := flatLines(width,
		[]string{"NAME", "USER", "ADDRESS", "KEY", "STATUS"},
		widths, all, s.cursor)

	bodyHeight := max0(height - len(top) - len(bottom))
	body := viewportBody(&s.sb, width, bodyHeight, content, cursorLine, headerLine)
	lines := append(append(top, body...), bottom...)
	return panelLines(width, height, lines)
}

// statusCell returns the STATUS column value for a host. Empty/idle hosts
// render as an en-dash; in-flight probes show "testing…"; successful
// probes show the round-trip latency; failures surface the first line of
// the error, truncated for table width.
func (s *hostsScreen) statusCell(name string) string {
	st, ok := s.states[name]
	if !ok || (!st.pending && !st.done) {
		return "–"
	}
	if st.pending {
		return "testing…"
	}
	if st.ok {
		return fmt.Sprintf("ok %s", st.latency.Round(time.Millisecond))
	}
	return firstLineOf(st.err)
}

// ── Actions ─────────────────────────────────────────────────────────────────

// openAddForm creates the inline add-host form. Four fields: name +
// address (required), user + ssh key (optional). user and address are
// split so each has its own labeled input — the address field carries
// only the host[:port], and user is joined back before the actions layer
// sees it.
func (s *hostsScreen) openAddForm() {
	s.form = newInlineForm("Add host", []formField{
		newFormField("name", "short identifier", true),
		newFormField("address", "host or IP", true),
		newFormField("user", "blank for global user", false),
		newFormField("ssh key", "blank for global key", false),
	})
	s.mode = hostsModeAdd
}

func (s *hostsScreen) commitAdd() error {
	name := s.form.Value(0)
	address := s.form.Value(1)
	user := s.form.Value(2)
	key := s.form.Value(3)
	// Join user + address into the "user@host" form ParseAddress expects.
	// When user is blank, ParseAddress accepts the bare host and the global
	// default kicks in at SSH dial time.
	raw := address
	if user != "" {
		raw = user + "@" + address
	}
	if err := actions.AddHost(s.cfg, "", name, raw, key); err != nil {
		return err
	}
	s.notice = fmt.Sprintf("Added host %q", name)
	s.refresh()
	// Move cursor to the newly added row so the user sees the entry appear.
	// Walk the visible slice so the index lands on the row that will render.
	for i, r := range s.visible {
		if r.Name == name {
			s.cursor = i
			break
		}
	}
	return nil
}

// openDeletePrompt stages the remove-host confirm modal. Row is captured at
// open time so the dialog reads consistently even if the user moves the
// cursor while it's open (and so the commit closure matches exactly what
// the user saw when they pressed Confirm).
func (s *hostsScreen) openDeletePrompt() {
	r := s.currentRow()
	if r == nil {
		return
	}
	captured := *r
	s.prompt = &confirmPrompt{
		title: fmt.Sprintf("Remove host %q?", captured.Name),
		details: []string{
			fmt.Sprintf("The host entry (%s) will be deleted", captured.Address),
			"from your marina config.",
			"No remote changes are made.",
		},
		confirmLabel: "Remove",
		focus:        0, // destructive default → Cancel
		onYes: func() tea.Cmd {
			removed, _, err := actions.RemoveHosts(s.cfg, "", captured.Name)
			if err != nil {
				s.notice = "error: " + firstLineOf(err)
			} else if len(removed) > 0 {
				s.notice = fmt.Sprintf("Removed host %q", captured.Name)
			}
			s.refresh() // rebuilds visible + clamps cursor
			return nil
		},
	}
	s.mode = hostsModeConfirmDelete
}

func (s *hostsScreen) startTests() tea.Cmd {
	// Tests every configured host — filter only affects what the user sees,
	// not what gets probed. Keeping tests against `s.rows` means a filtered
	// view still gets accurate connectivity data in the background.
	if len(s.rows) == 0 {
		return nil
	}
	cmds := make([]tea.Cmd, 0, len(s.rows)+1)
	for _, r := range s.rows {
		name := r.Name
		s.states[name] = &hostTestState{pending: true}
		s.pending++
		cmds = append(cmds, hostTestCmd(s.ctx, s.cfg, name))
	}
	if s.pending > 0 {
		cmds = append(cmds, s.spinner.Tick)
	}
	return tea.Batch(cmds...)
}

// hostTestCmd wraps actions.TestHost in a tea.Cmd so the result lands in
// Update as a hostTestResultMsg.
func hostTestCmd(ctx context.Context, cfg *config.Config, host string) tea.Cmd {
	return func() tea.Msg {
		probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		r := actions.TestHost(probeCtx, cfg, host)
		return hostTestResultMsg{host: r.Host, ok: r.OK, latency: r.Latency, err: r.Err}
	}
}

// refresh rebuilds the row cache from the in-memory config and reapplies
// the filter so the visible list stays consistent with the underlying data.
func (s *hostsScreen) refresh() {
	s.rows = actions.ListHosts(s.cfg)
	s.rebuildVisible()
}

// rebuildVisible applies the current filter to the raw host list.
func (s *hostsScreen) rebuildVisible() {
	s.visible = s.visible[:0]
	for _, r := range s.rows {
		if s.filter.Match(r.Name, r.User, r.Address, r.Key) {
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

func (s *hostsScreen) currentRow() *actions.HostRow {
	if s.cursor < 0 || s.cursor >= len(s.visible) {
		return nil
	}
	return &s.visible[s.cursor]
}

// ── shared small helpers ────────────────────────────────────────────────────

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func sumInts(v []int) int {
	total := 0
	for _, x := range v {
		total += x
	}
	return total
}

func firstLineOf(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 40 {
		s = s[:39] + "…"
	}
	return s
}
