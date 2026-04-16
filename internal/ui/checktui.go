package ui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ── Public types ─────────────────────────────────────────────────────────────

type UpdateCandidate struct {
	Host        string
	Stack       string
	Container   string
	ContainerID string
	Image       string
	ImageRef    string // resolved image reference from inspect
	Digest      string // local digest from inspect (empty = locally built)
	Dir         string
}

type UpdateCheckResult struct {
	UpdateCandidate
	Status    string
	HasUpdate bool
	Error     error
}

// ── Styles ──────────────────────────────────────────────────────────────────

var (
	cAccent = lipgloss.Color("#7D56F4")
	cTeal   = lipgloss.Color("#38BDF8")
	cGreen  = lipgloss.Color("#50fa7b")
	cYellow = lipgloss.Color("#f1fa8c")
	cDim    = lipgloss.Color("#626262")
	cFg     = lipgloss.Color("#f8f8f2")

	sWindow  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(1, 2)
	sTitle   = lipgloss.NewStyle().Bold(true).Foreground(cTeal)
	sSub     = lipgloss.NewStyle().Foreground(cDim)
	sHost    = lipgloss.NewStyle().Bold(true).Foreground(cGreen).PaddingLeft(2)
	sHeader  = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sDim     = lipgloss.NewStyle().Foreground(cDim)
	sRow     = lipgloss.NewStyle().Foreground(cFg)
	sCursor  = lipgloss.NewStyle().Foreground(cYellow).Background(cAccent).Bold(true)
	sCount   = lipgloss.NewStyle().Foreground(cYellow).Bold(true)
	sHelp    = lipgloss.NewStyle().Foreground(cDim)
	sSpinner = lipgloss.NewStyle().Foreground(cAccent)
	sCheck   = lipgloss.NewStyle().Foreground(cGreen).Bold(true)
)

// ── Model ───────────────────────────────────────────────────────────────────

type checkPhase int

const (
	phaseLoading checkPhase = iota
	phaseResults
)

type checkDoneMsg struct{ result UpdateCheckResult }

type checkModel struct {
	ctx       context.Context
	phase     checkPhase
	width     int
	height    int
	cancelled bool
	logger    *slog.Logger
	logFile   *os.File

	spinner    spinner.Model
	candidates []UpdateCandidate
	checkFn    func(context.Context, UpdateCandidate) UpdateCheckResult
	checked    int
	total      int
	results    []UpdateCheckResult

	vp       *viewport.Model
	cursor   int
	selected map[int]bool
	showAll  bool
}

func initLogger() (*slog.Logger, *os.File) {
	home, err := os.UserHomeDir()
	if err != nil {
		return slog.New(slog.DiscardHandler), nil
	}
	dir := filepath.Join(home, ".config", "marina")
	_ = os.MkdirAll(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, "marina.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return slog.New(slog.DiscardHandler), nil
	}
	return slog.New(slog.NewTextHandler(f, nil)), f
}

func newCheckModel(ctx context.Context, candidates []UpdateCandidate, checkFn func(context.Context, UpdateCandidate) UpdateCheckResult) checkModel {
	s := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	s.Style = sSpinner
	logger, logFile := initLogger()
	logger.Info("check started", "containers", len(candidates))
	vp := viewport.New()
	return checkModel{
		ctx: ctx, phase: phaseLoading, spinner: s,
		candidates: candidates, total: len(candidates), checkFn: checkFn,
		selected: make(map[int]bool), logger: logger, logFile: logFile, vp: &vp,
	}
}

func (m checkModel) winW() int { return max(min(m.width-4, 110), 40) }

// colWidths returns STACK (40%), CONTAINER (40%), SELECTED (20%) widths.
func (m checkModel) colWidths() (int, int, int) {
	avail := m.winW() - 6 - 2 // border/pad, leading indent
	stackW := avail * 40 / 100
	containerW := avail * 40 / 100
	selectedW := avail - stackW - containerW
	return max(stackW, 6), max(containerW, 6), max(selectedW, 4)
}

func (m checkModel) filteredItems() []UpdateCheckResult {
	if m.showAll {
		return m.results
	}
	out := make([]UpdateCheckResult, 0)
	for _, r := range m.results {
		if r.HasUpdate {
			out = append(out, r)
		}
	}
	return out
}

func (m checkModel) selectedResults() []UpdateCheckResult {
	items := m.filteredItems()
	out := make([]UpdateCheckResult, 0)
	for i, item := range items {
		if m.selected[i] {
			out = append(out, item)
		}
	}
	return out
}

// ── tea.Model ───────────────────────────────────────────────────────────────

func (m checkModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick}
	for _, c := range m.candidates {
		c := c
		cmds = append(cmds, func() tea.Msg { return checkDoneMsg{result: m.checkFn(m.ctx, c)} })
	}
	return tea.Batch(cmds...)
}

func (m checkModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.vp.SetWidth(m.winW() - 6)
		m.vp.SetHeight(max(m.height-20, 3))
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case checkDoneMsg:
		m.checked++
		m.results = append(m.results, msg.result)
		if msg.result.Error != nil {
			m.logger.Warn("check failed", "host", msg.result.Host, "container", msg.result.Container, "error", msg.result.Error)
		} else if msg.result.HasUpdate {
			m.logger.Info("update available", "host", msg.result.Host, "container", msg.result.Container)
		}

		if m.checked == m.total {
			sort.Slice(m.results, func(i, j int) bool {
				a, b := m.results[i], m.results[j]
				if a.Host != b.Host {
					return a.Host < b.Host
				}
				if a.Stack != b.Stack {
					return a.Stack < b.Stack
				}
				return a.Container < b.Container
			})
			m.phase = phaseResults
			for i, item := range m.filteredItems() {
				if item.HasUpdate {
					m.selected[i] = true
				}
			}
		}
		return m, nil

	case tea.KeyPressMsg:
		if m.phase == phaseLoading {
			if msg.String() == "q" || msg.String() == "ctrl+c" {
				m.cancelled = true
				return m, tea.Quit
			}
			return m, nil
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func (m checkModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	items := m.filteredItems()
	n := len(items)

	switch msg.String() {
	case "q", "esc", "ctrl+c":
		m.cancelled = true
		return m, tea.Quit
	case "enter":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.scrollTo()
		}
	case "down", "j":
		if m.cursor < n-1 {
			m.cursor++
			m.scrollTo()
		}
	case " ", "space":
		if n > 0 {
			m.selected[m.cursor] = !m.selected[m.cursor]
		}
	case "a":
		all := true
		for i := range items {
			if !m.selected[i] {
				all = false
				break
			}
		}
		if all {
			m.selected = make(map[int]bool)
		} else {
			for i := range items {
				m.selected[i] = true
			}
		}
	case "t":
		m.showAll = !m.showAll
		m.cursor = 0
		m.vp.SetYOffset(0)
		m.selected = make(map[int]bool)
		for i, item := range m.filteredItems() {
			if item.HasUpdate {
				m.selected[i] = true
			}
		}
	}
	return m, nil
}

func (m *checkModel) scrollTo() {
	items := m.filteredItems()
	line := 0
	lastHost := ""
	for i, item := range items {
		if item.Host != lastHost {
			if lastHost != "" {
				line++
			}
			line += 3
			lastHost = item.Host
		}
		if i == m.cursor {
			break
		}
		line++
	}
	m.vp.EnsureVisible(line, 0, 0)
}

func (m checkModel) View() tea.View {
	var s string
	if m.phase == phaseLoading {
		s = m.viewLoading()
	} else {
		s = m.viewResults()
	}
	v := tea.NewView(s)
	v.AltScreen = true
	return v
}

// ── Loading ─────────────────────────────────────────────────────────────────

func (m checkModel) viewLoading() string {
	w := m.winW()
	bw := max(w-10, 10)

	var b strings.Builder
	b.WriteString(sTitle.Render("Marina") + "\n")
	b.WriteString(sSub.Render("Checking for Updates") + "\n\n")

	b.WriteString(m.spinner.View() + " " + sCount.Render(fmt.Sprintf("Checking containers (%d/%d)...", m.checked, m.total)) + "\n")

	filled := 0
	if m.total > 0 {
		filled = m.checked * bw / m.total
	}
	b.WriteString("  " + lipgloss.NewStyle().Foreground(cGreen).Render(strings.Repeat("█", filled)) + sDim.Render(strings.Repeat("░", bw-filled)) + "\n\n")
	b.WriteString(sHelp.Render("  q quit"))

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, sWindow.Width(w).Render(b.String()))
}

// ── Results ─────────────────────────────────────────────────────────────────

func (m checkModel) viewResults() string {
	w := m.winW()
	sW, cW, selW := m.colWidths()
	contentWidth := w - 6
	items := m.filteredItems()

	updates, sel := 0, 0
	for _, r := range m.results {
		if r.HasUpdate {
			updates++
		}
	}
	for _, v := range m.selected {
		if v {
			sel++
		}
	}

	// Header
	var b strings.Builder
	b.WriteString(sTitle.Render("Marina") + "\n")
	b.WriteString(sSub.Render("Available Updates") + "\n\n")
	b.WriteString(sCount.Render(fmt.Sprintf("%d update(s) available", updates)) + "  ")
	if m.showAll {
		b.WriteString(sDim.Render("showing all"))
	} else {
		b.WriteString(sDim.Render("showing updates only"))
	}
	b.WriteString("\n")

	// Table content for viewport
	var t strings.Builder
	lastHost := ""

	fmtRow := func(c1, c2, c3 string) string {
		return fmt.Sprintf("  %-*s%-*s%-*s", sW, c1, cW, c2, selW, c3)
	}

	for i, item := range items {
		if item.Host != lastHost {
			if lastHost != "" {
				t.WriteString("\n")
			}
			t.WriteString(sHost.Render("▸ "+item.Host) + "\n")
			t.WriteString(sHeader.Render(fmtRow("STACK", "CONTAINER", "SELECTED")) + "\n")
			t.WriteString(sDim.Render("  "+strings.Repeat("─", contentWidth-2)) + "\n")
			lastHost = item.Host
		}

		stack := item.Stack
		if stack == "" {
			stack = "-"
		}

		sel := "  -"
		if m.selected[i] {
			sel = "  ✓"
		}

		if i == m.cursor {
			t.WriteString(sCursor.Width(contentWidth).Render(fmtRow(truncateStr(stack, sW-1), truncateStr(item.Container, cW-1), sel)) + "\n")
		} else {
			left := fmt.Sprintf("  %-*s%-*s", sW, truncateStr(stack, sW-1), cW, truncateStr(item.Container, cW-1))
			selStr := fmt.Sprintf("%-*s", selW, sel)
			if m.selected[i] {
				selStr = sCheck.Render(selStr)
			}
			if item.HasUpdate {
				t.WriteString(sRow.Render(left) + selStr + "\n")
			} else {
				t.WriteString(sDim.Render(left) + selStr + "\n")
			}
		}
	}

	if len(items) == 0 {
		t.WriteString("\n" + sDim.Render("  All images are up-to-date.") + "\n")
	}

	m.vp.SetContent(t.String())
	b.WriteString(m.vp.View() + "\n")

	// Footer
	b.WriteString("\n" + sCount.Render(fmt.Sprintf("%d selected", sel)) + "\n\n")
	b.WriteString(sHelp.Render("  j/k move   space select   a all   t filter   enter apply   q quit"))

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, sWindow.Width(w).Render(b.String()))
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func truncateStr(s string, n int) string {
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// ── Entry point ─────────────────────────────────────────────────────────────

func RunCheckTUI(ctx context.Context, candidates []UpdateCandidate, checkFn func(context.Context, UpdateCandidate) UpdateCheckResult) ([]UpdateCheckResult, error) {
	m := newCheckModel(ctx, candidates, checkFn)
	p := tea.NewProgram(m)
	result, err := p.Run()
	if m.logFile != nil {
		m.logFile.Close()
	}
	if err != nil {
		return nil, err
	}
	final, ok := result.(checkModel)
	if !ok {
		return nil, fmt.Errorf("unexpected model type")
	}
	if final.cancelled {
		return nil, nil
	}
	return final.selectedResults(), nil
}
