package tui

import (
	"context"
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/docker/docker/api/types/container"

	"github.com/AhmedAburady/marina/internal/config"
	internalssh "github.com/AhmedAburady/marina/internal/ssh"
)

// containerRow is one container line in the unified list.
type containerRow struct {
	host   string
	stack  string
	name   string
	id     string
	image  string
	state  string // machine-readable docker state — drives row colour
	status string // human-readable "Up 2 minutes" / "Exited (0) 3 hours ago"
	ports  string // pre-formatted "host->guest/proto" list
	sshCfg internalssh.Config
}

// containerStateColor returns the foreground override for a container row
// based on its docker state. `running` gets no override so it inherits the
// default light-grey; anything else is tinted so the user can scan a wall
// of containers and spot problems at a glance.
func containerStateColor(state string) color.Color {
	switch state {
	case "exited", "dead":
		return cRed
	case "restarting":
		return cYellow
	case "paused", "created":
		return cDim
	}
	return nil
}

// containersMode routes key handling between the list and the remove-confirm
// dialog. Only one dialog is ever active; the list is paused while it owns
// the keyboard.
type containersMode int

const (
	containersModeList containersMode = iota
	containersModeConfirmRemove
)

// containersScreen lists every container across every configured host, with
// start/stop/restart/remove actions on the highlighted row.
type containersScreen struct {
	ctx     context.Context
	cfg     *config.Config
	rows    []containerRow // unfiltered full list
	visible []containerRow // post-filter view — cursor + actions index into THIS
	cursor  int            // position inside visible
	loading bool
	err     error
	pending map[string]bool // container ID → action in-flight
	errors  map[string]string
	spinner spinner.Model
	sb      scrollBody // viewport-backed scroll for long lists
	mode    containersMode
	prompt  *confirmPrompt
	filter  filterBar
}

func newContainersScreen(ctx context.Context, cfg *config.Config) *containersScreen {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = sSpinner
	return &containersScreen{
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

func (s *containersScreen) Title() string         { return "Containers" }

func (s *containersScreen) Init() tea.Cmd {
	return tea.Batch(
		FetchAllHostsCmd(s.ctx, s.cfg, s.cfg.Hosts),
		s.spinner.Tick,
	)
}

func (s *containersScreen) Help() string {
	if s.mode == containersModeConfirmRemove {
		return "←/→ select · enter confirm · esc cancel"
	}
	if s.filter.Active() {
		return "type to filter · enter apply · esc clear"
	}
	return "↑/↓ move · / filter · s start · x stop · r restart · d remove · R refresh · esc back"
}

func (s *containersScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	// Confirm dialog owns the keys while open. Background data still streams
	// in via the message switch below.
	if s.mode == containersModeConfirmRemove && s.prompt != nil {
		done, cmd := s.prompt.Update(msg)
		if done {
			s.prompt = nil
			s.mode = containersModeList
		}
		if _, isKey := msg.(tea.KeyPressMsg); isKey {
			return s, cmd
		}
	}

	// Filter bar takes all key input while editing so that typing a letter
	// like "s" refines the query instead of firing the start action.
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
			return s, s.runAction("docker start", "container.start")
		case "x":
			return s, s.runAction("docker stop", "container.stop")
		case "r":
			return s, s.runAction("docker restart", "container.restart")
		case "d":
			s.openRemovePrompt()
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
			// Refresh container state after a successful action so status
			// reflects reality. Cheap compared to the action itself.
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

func (s *containersScreen) View(width, height int) string {
	if s.loading {
		return panelLines(width, height, []string{
			spacer(width),
			pendingNote(width, s.spinner.View(), "loading containers…"),
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
			mutedLine(width, "No containers found on any host."),
		})
	}

	// Fixed header rows: blank leading spacer + optional filter bar above
	// the list. filterBar.View returns "" when idle, so it only consumes a
	// row when the user is actually filtering.
	var top []string
	top = append(top, spacer(width))
	if bar := s.filter.View(width, len(s.visible)); bar != "" {
		top = append(top, bar)
		top = append(top, spacer(width))
	}

	// Trailing rows: pending spinner + last-error for the current row.
	var bottom []string
	if len(s.pending) > 0 {
		bottom = append(bottom, spacer(width))
		bottom = append(bottom, pendingNote(width, s.spinner.View(),
			fmt.Sprintf("%d action(s) in flight…", len(s.pending))))
	}
	if r := s.currentRow(); r != nil {
		if msg, ok := s.errors[r.id]; ok {
			bottom = append(bottom, spacer(width))
			bottom = append(bottom, errorNote(width, "last error: "+msg))
		}
	}

	// Build the scrollable content: one grouped section per host.
	// Columns: STACK, CONTAINER, IMAGE, STATUS, PORTS — matching
	// `marina ps` exactly. A trailing indicator (•/!) for action state
	// is appended to the STATUS cell so column parity stays intact.
	inner := innerWidth(width)
	widths := shareWidths(inner,
		[]int{2, 3, 4, 3, 3},    // weights
		[]int{10, 14, 16, 10, 8}, // floors
	)
	all := make([][]string, 0, len(s.visible))
	colors := make([]color.Color, 0, len(s.visible))
	for _, r := range s.visible {
		pend, bad := rowHas(s.pending, s.errors, r.id)
		status := r.status
		if m := markFor(pend, bad); m != "" {
			status = m + " " + status
		}
		all = append(all, []string{r.host, r.stack, r.name, r.image, status, r.ports})
		colors = append(colors, containerStateColor(r.state))
	}
	groups := groupRows(all, 0) // strip and group by HOST
	// Re-attach per-row colours to each group — groupRows strips the group
	// column but doesn't know about RowColors. Walk groups in original order
	// and slice `colors` to match each one's row count.
	cursor := 0
	for gi := range groups {
		n := len(groups[gi].Rows)
		groups[gi].RowColors = colors[cursor : cursor+n]
		cursor += n
	}
	content, cursorLine, headerLine := groupedLines(width,
		[]string{"STACK", "CONTAINER", "IMAGE", "STATUS", "PORTS"},
		widths, groups, s.cursor)

	bodyHeight := max0(height - len(top) - len(bottom))
	body := viewportBody(&s.sb, width, bodyHeight, content, cursorLine, headerLine)

	lines := append(append(top, body...), bottom...)
	return panelLines(width, height, lines)
}

// Modal implements ModalProvider. Returns the active remove-confirm dialog
// so the dashboard composites it over the containers list.
func (s *containersScreen) Modal() (string, bool) {
	if s.mode == containersModeConfirmRemove && s.prompt != nil {
		return s.prompt.View(), true
	}
	return "", false
}

// ── Internals ───────────────────────────────────────────────────────────────

// openRemovePrompt stages a confirm modal for a destructive `docker rm -f`
// on the highlighted row. Force is on because stop-then-rm would make this
// a two-step operation from the TUI; a single rm -f matches what users
// intend when they pick "remove" on a running container.
func (s *containersScreen) openRemovePrompt() {
	r := s.currentRow()
	if r == nil || r.id == "" {
		return
	}
	captured := *r
	s.prompt = &confirmPrompt{
		title: fmt.Sprintf("Remove container %q on %q?", captured.name, captured.host),
		details: []string{
			"This will permanently:",
			"  1. Stop the container if it's running",
			"  2. Delete the container (docker rm -f)",
			"",
			"Container state is lost. If the container belongs to a",
			"stack, use Stacks → s (start) to recreate it from compose.",
		},
		confirmLabel: "Remove",
		focus:        0, // default to Cancel on destructive ops
		onYes:        s.buildRemoveCmd(captured),
	}
	s.mode = containersModeConfirmRemove
}

// buildRemoveCmd returns the tea.Cmd that actually fires the remove. Runs
// `docker rm -f <id>` via the shared action path so logging + result
// plumbing behaves exactly like start/stop/restart.
func (s *containersScreen) buildRemoveCmd(r containerRow) func() tea.Cmd {
	return func() tea.Cmd {
		s.pending[r.id] = true
		delete(s.errors, r.id)
		return tea.Batch(
			DockerExecCmd(r.sshCfg, "docker rm -f "+r.id, "container.remove", r.id),
			s.spinner.Tick,
		)
	}
}

func (s *containersScreen) buildRows(results map[string]HostFetchResult) {
	var out []containerRow
	for host, res := range results {
		if res.Err != nil {
			// Keep one placeholder row so the user sees the host error.
			out = append(out, containerRow{
				host:   host,
				stack:  "-",
				name:   "(unreachable)",
				image:  "",
				status: firstLineOf(res.Err),
			})
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
		for _, c := range res.Containers {
			out = append(out, containerRow{
				host:   host,
				stack:  stackName(c),
				name:   displayName(c),
				id:     c.ID,
				image:  c.Image,
				state:  c.State,
				status: c.Status,
				ports:  formatPorts(c.Ports),
				sshCfg: sshCfg,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].host != out[j].host {
			return out[i].host < out[j].host
		}
		if out[i].stack != out[j].stack {
			return out[i].stack < out[j].stack
		}
		return out[i].name < out[j].name
	})
	s.rows = out
	s.rebuildVisible()
}

// rebuildVisible applies the current filter to the raw row list, clamping
// the cursor so actions can't dereference a stale index after the visible
// set shrinks.
func (s *containersScreen) rebuildVisible() {
	s.visible = s.visible[:0]
	for _, r := range s.rows {
		if s.filter.Match(r.host, r.stack, r.name, r.image, r.status, r.ports) {
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

func (s *containersScreen) currentRow() *containerRow {
	if s.cursor < 0 || s.cursor >= len(s.visible) {
		return nil
	}
	return &s.visible[s.cursor]
}

// runAction fires one of the three container control commands against the
// highlighted row and marks it pending until the ActionResultMsg returns.
func (s *containersScreen) runAction(dockerCmd, kind string) tea.Cmd {
	r := s.currentRow()
	if r == nil || r.id == "" {
		return nil
	}
	s.pending[r.id] = true
	delete(s.errors, r.id)
	return tea.Batch(
		DockerExecCmd(r.sshCfg, dockerCmd+" "+r.id, kind, r.id),
		s.spinner.Tick,
	)
}

// formatPorts builds "hostPort->containerPort/proto" strings for each
// exposed port and joins with commas. Only publicly-mapped ports are
// listed; duplicates are collapsed. Mirrors the CLI helper in
// internal/ui/table.go so the TUI and `marina ps` render identical values.
func formatPorts(ports []container.Port) string {
	seen := make(map[string]bool)
	var parts []string
	for _, p := range ports {
		if p.PublicPort == 0 {
			continue
		}
		entry := fmt.Sprintf("%d->%d/%s", p.PublicPort, p.PrivatePort, p.Type)
		if seen[entry] {
			continue
		}
		seen[entry] = true
		parts = append(parts, entry)
	}
	return strings.Join(parts, ", ")
}

// stackName returns the compose project label for a container or "-".
func stackName(c container.Summary) string {
	if v := c.Labels["com.docker.compose.project"]; v != "" {
		return v
	}
	return "-"
}

// displayName returns the user-friendly container name or a short ID.
func displayName(c container.Summary) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	if len(c.ID) >= 12 {
		return c.ID[:12]
	}
	return c.ID
}

// Satisfy unused-import guard on time — rows don't use it but future
// per-row pending timers may.
var _ = time.Second
