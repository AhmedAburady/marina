package tui

import (
	"context"
	"fmt"
	"image/color"
	"slices"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/AhmedAburady/marina/internal/actions"
	"github.com/AhmedAburady/marina/internal/config"
	"github.com/AhmedAburady/marina/internal/strutil"
)

// hostTestResultMsg carries the outcome of one SSH probe.
type hostTestResultMsg struct {
	host      string
	ok        bool
	latency   time.Duration
	err       error
	untrusted bool
}

// hostTrustResultMsg carries the outcome of one TrustHost attempt. A
// successful trust is always followed by a test probe so the user sees the
// row transition from "untrusted" to "ok" in one action.
type hostTrustResultMsg struct {
	host string
	err  error
}

// hostTestState holds the transient test status for one row. `pending` is
// true while the probe is in flight; `done` + `ok`/`err` carry the result.
type hostTestState struct {
	pending   bool
	done      bool
	ok        bool
	latency   time.Duration
	err       error
	untrusted bool
}

// hostsMode toggles between the plain list and the inline add / confirm
// dialogs so all three renders can share the same parent screen.
type hostsMode int

const (
	hostsModeList hostsMode = iota
	hostsModeAdd
	hostsModeEdit
	hostsModeConfirmDelete
	hostsModeConfirmTrust
)

// hostsScreen is the "Hosts" destination: list, test, add, remove.
type hostsScreen struct {
	ctx      context.Context
	cfg      *config.Config
	rows     []actions.HostRow // unfiltered
	visible  []actions.HostRow // post-filter — cursor + actions index into this
	cursor   int
	states   map[string]*hostTestState
	pending  int
	spinner  spinner.Model
	mode     hostsMode
	form     *inlineForm
	editName string // host name captured when the edit form was opened
	prompt   *confirmPrompt
	filter   filterBar
	notice   string
	sb       scrollBody
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
	case hostsModeAdd, hostsModeEdit:
		return "tab move · space toggle · enter save · esc cancel"
	case hostsModeConfirmDelete, hostsModeConfirmTrust:
		return "←/→ select · enter confirm · esc cancel"
	}
	if s.filter.Active() {
		return "type to filter · enter apply · esc clear"
	}
	return "↑/↓ move · / filter · t test · u trust · x disable · a add · e edit · d delete · R refresh · esc back"
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
				name, err := s.commitAdd()
				if err != nil {
					s.form.err = err.Error()
					return s, cmd
				}
				s.form = nil
				// If the host already has a key in known_hosts (user added
				// it manually before, or it was added for another marina
				// host with the same address), skip the trust prompt and go
				// straight to the list. `u` is still available for manual
				// re-trust attempts.
				if trusted, _ := actions.IsHostTrusted(s.ctx, s.cfg, name); trusted {
					s.mode = hostsModeList
					return s, cmd
				}
				s.openTrustPrompt(name)
				return s, cmd
			}
			return s, cmd
		}
	case hostsModeEdit:
		if s.form != nil {
			submit, cancel, cmd := s.form.Update(msg)
			switch {
			case cancel:
				s.mode = hostsModeList
				s.form = nil
				s.editName = ""
			case submit:
				if err := s.commitEdit(); err != nil {
					s.form.err = err.Error()
					return s, cmd
				}
				s.mode = hostsModeList
				s.form = nil
				s.editName = ""
			}
			return s, cmd
		}
	case hostsModeConfirmDelete, hostsModeConfirmTrust:
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
		case "e":
			s.openEditForm()
		case "u":
			return s, s.trustCurrent()
		case "d":
			s.openDeletePrompt()
		case "x":
			s.toggleDisabled()
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
		st.untrusted = msg.untrusted

	case hostTrustResultMsg:
		if msg.err != nil {
			s.notice = fmt.Sprintf("trust %q: %s", msg.host, strutil.FirstLine(msg.err.Error(), 60))
			return s, nil
		}
		s.notice = fmt.Sprintf("Trusted %q", msg.host)
		// Clear any stale untrusted state and chain an immediate test probe
		// so the row transitions from "untrusted — press u" to the real
		// connection status without requiring another keypress.
		delete(s.states, msg.host)
		s.states[msg.host] = &hostTestState{pending: true}
		s.pending++
		return s, tea.Batch(hostTestCmd(s.ctx, s.cfg, msg.host), s.spinner.Tick)

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

// Modal implements ModalProvider. When an add-host, edit-host, or
// delete-confirm flow is open, the dashboard overlays this string over
// the list view.
func (s *hostsScreen) Modal() (string, bool) {
	switch s.mode {
	case hostsModeAdd, hostsModeEdit:
		if s.form == nil {
			return "", false
		}
		return s.form.Modal(), true
	case hostsModeConfirmDelete, hostsModeConfirmTrust:
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
	colors := make([]color.Color, 0, len(s.visible))
	for _, r := range s.visible {
		all = append(all, []string{r.Name, r.User, r.Address, shortPath(r.Key), s.statusCell(r)})
		if r.Disabled {
			colors = append(colors, cStackStopped)
		} else {
			colors = append(colors, nil)
		}
	}
	content, cursorLine, headerLine := flatLinesColored(width,
		[]string{"NAME", "USER", "ADDRESS", "KEY", "STATUS"},
		widths, all, colors, s.cursor)

	bodyHeight := max0(height - len(top) - len(bottom))
	body := viewportBody(&s.sb, width, bodyHeight, content, cursorLine, headerLine)
	lines := append(append(top, body...), bottom...)
	return panelLines(width, height, lines)
}

func (s *hostsScreen) statusCell(r actions.HostRow) string {
	if r.Disabled {
		return "disabled"
	}
	st, ok := s.states[r.Name]
	if !ok || (!st.pending && !st.done) {
		return "–"
	}
	if st.pending {
		return "…"
	}
	if st.ok {
		return fmt.Sprintf("ok %s", st.latency.Round(time.Millisecond))
	}
	if st.untrusted {
		return "untrusted — press u"
	}
	return "unreachable"
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

// commitAdd persists the add-form values as a new host entry. Returns the
// host name so the caller can chain follow-up commands (e.g. auto-trust).
func (s *hostsScreen) commitAdd() (string, error) {
	name := s.form.Value(0)
	address := s.form.Value(1)
	user := s.form.Value(2)
	key := s.form.Value(3)
	if err := actions.AddHost(s.cfg, "", name, actions.JoinUserAddress(user, address), key); err != nil {
		return "", err
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
	return name, nil
}

// openEditForm opens the edit-host dialog pre-filled with the current row's
// values. Name is intentionally omitted from the form — renaming would
// require re-keying the config map, which is out of scope here. Enabled /
// Disabled is exposed as a pill toggle so the user can change state from
// the same dialog that edits address / user / key.
func (s *hostsScreen) openEditForm() {
	r := s.currentRow()
	if r == nil {
		return
	}
	h, ok := s.cfg.Hosts[r.Name]
	if !ok {
		return
	}
	// Reconstruct the display-friendly address the user typed when adding —
	// join user back in so the edit view looks like the input they'd supply
	// via `marina hosts add`.
	rawAddr := h.Address
	if h.User != "" {
		rawAddr = h.User + "@" + h.Address
	}
	s.editName = r.Name
	s.form = newInlineForm(fmt.Sprintf("Edit host %q", r.Name), []formField{
		newTextFieldWithValue("address", "host or IP", rawAddr, true),
		newTextFieldWithValue("user", "blank for global user", h.User, false),
		newTextFieldWithValue("ssh key", "blank for global key", h.SSHKey, false),
		newToggleField("status", "Enabled", "Disabled", !h.Disabled),
	})
	s.mode = hostsModeEdit
}

// commitEdit applies the form values through actions.UpdateHost. The host
// name was captured at openEditForm time, so cursor movement while the
// dialog is open doesn't affect which host gets updated.
func (s *hostsScreen) commitEdit() error {
	name := s.editName
	address := s.form.Value(0)
	user := s.form.Value(1)
	key := s.form.Value(2)
	enabled := s.form.BoolValue(3)

	if err := actions.UpdateHost(s.cfg, "", name, actions.JoinUserAddress(user, address), key, !enabled); err != nil {
		return err
	}
	s.notice = fmt.Sprintf("Updated host %q", name)
	s.refresh()
	for i, r := range s.visible {
		if r.Name == name {
			s.cursor = i
			break
		}
	}
	return nil
}

// openTrustPrompt stages a post-add confirm modal asking the user whether
// to trust the newly added host's SSH key. Mirrors the CLI `marina hosts
// add` prompt so both flows behave identically. Confirm pill is the
// default focus — trusting after an explicit add is the typical path.
func (s *hostsScreen) openTrustPrompt(name string) {
	h, ok := s.cfg.Hosts[name]
	if !ok {
		return
	}
	addr := h.Address
	s.prompt = &confirmPrompt{
		title: fmt.Sprintf("Trust SSH host key for %q?", name),
		details: []string{
			fmt.Sprintf("Adds %s to ~/.ssh/known_hosts so marina can", addr),
			"connect without the interactive first-time prompt.",
			"Decline to leave the host untrusted — you can still",
			"press `u` on the row later to trust it.",
		},
		confirmLabel: "Trust",
		focus:        1, // additive action → default to Confirm
		onYes: func() tea.Cmd {
			return hostTrustCmd(s.ctx, s.cfg, name)
		},
	}
	s.mode = hostsModeConfirmTrust
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
				s.notice = "error: " + strutil.FirstLine(err.Error(), 40)
			} else if len(removed) > 0 {
				s.notice = fmt.Sprintf("Removed host %q", captured.Name)
			}
			s.refresh() // rebuilds visible + clamps cursor
			return nil
		},
	}
	s.mode = hostsModeConfirmDelete
}

// toggleDisabled flips the Disabled flag on the host under the cursor and
// persists immediately. Disabled hosts are skipped by fan-out operations
// (ps, stacks, check, update, dashboards).
func (s *hostsScreen) toggleDisabled() {
	if s.cursor < 0 || s.cursor >= len(s.visible) {
		return
	}
	name := s.visible[s.cursor].Name
	h, ok := s.cfg.Hosts[name]
	if !ok {
		return
	}
	newState := !h.Disabled
	if err := actions.SetHostDisabled(s.cfg, "", name, newState); err != nil {
		s.notice = "error: " + strutil.FirstLine(err.Error(), 40)
		return
	}
	if newState {
		s.notice = fmt.Sprintf("Disabled host %q", name)
		// Drop any test state so a stale pending counter from an in-flight
		// probe can't keep the footer spinner alive. The probe goroutine
		// still runs to completion, but its result is ignored on arrival
		// (state.pending == false → no decrement).
		if st, ok := s.states[name]; ok && st.pending {
			s.pending--
		}
		delete(s.states, name)
	} else {
		s.notice = fmt.Sprintf("Enabled host %q", name)
	}
	s.refresh()
}

func (s *hostsScreen) startTests() tea.Cmd {
	if len(s.rows) == 0 {
		return nil
	}
	cmds := make([]tea.Cmd, 0, len(s.rows)+1)
	for _, r := range s.rows {
		if r.Disabled {
			continue // disabled hosts are never probed
		}
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
		return hostTestResultMsg{
			host:      r.Host,
			ok:        r.OK,
			latency:   r.Latency,
			err:       r.Err,
			untrusted: r.Untrusted,
		}
	}
}

// hostTrustCmd wraps actions.TrustHost in a tea.Cmd. A successful trust is
// always followed (in the Update handler) by an immediate test probe, so
// callers don't need to orchestrate the chain themselves.
func hostTrustCmd(ctx context.Context, cfg *config.Config, host string) tea.Cmd {
	return func() tea.Msg {
		trustCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		err := actions.TrustHost(trustCtx, cfg, host)
		return hostTrustResultMsg{host: host, err: err}
	}
}

// trustCurrent kicks off a TrustHost command for the host under the
// cursor. Returns nil (no-op) when the list is empty.
func (s *hostsScreen) trustCurrent() tea.Cmd {
	r := s.currentRow()
	if r == nil {
		return nil
	}
	return hostTrustCmd(s.ctx, s.cfg, r.Name)
}

// refresh rebuilds the row cache from the in-memory config and reapplies
// the filter so the visible list stays consistent with the underlying data.
// Disabled hosts sink to the bottom (enabled alpha, then disabled alpha).
func (s *hostsScreen) refresh() {
	s.rows = actions.ListHosts(s.cfg)
	slices.SortStableFunc(s.rows, func(a, b actions.HostRow) int {
		if a.Disabled != b.Disabled {
			if a.Disabled {
				return 1
			}
			return -1
		}
		return 0 // ListHosts already alpha-sorted within each bucket
	})
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
