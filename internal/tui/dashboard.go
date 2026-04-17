package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/AhmedAburady/marina/internal/config"
)

// dashboard is the top-level Bubble Tea v2 model. It owns the screen stack,
// the terminal size, and global keys (ctrl+c quits). Everything else is
// delegated to the top-of-stack screen.
type dashboard struct {
	ctx    context.Context
	cfg    *config.Config
	stack  []Screen
	width  int
	height int
}

func newDashboard(ctx context.Context, cfg *config.Config) *dashboard {
	return &dashboard{
		ctx:   ctx,
		cfg:   cfg,
		stack: []Screen{newHomeScreen(ctx, cfg)},
	}
}

// top returns the screen currently on top of the navigation stack.
func (m *dashboard) top() Screen { return m.stack[len(m.stack)-1] }

// ── tea.Model ───────────────────────────────────────────────────────────────

func (m *dashboard) Init() tea.Cmd {
	return m.top().Init()
}

func (m *dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case PushScreenMsg:
		m.stack = append(m.stack, msg.Screen)
		return m, msg.Screen.Init()

	case PopScreenMsg:
		if len(m.stack) > 1 {
			m.stack = m.stack[:len(m.stack)-1]
		}
		return m, nil

	case tea.KeyPressMsg:
		// ctrl+c is the only universal kill. Everything else — including `q`
		// and `esc` — is the top screen's to interpret, because it means
		// different things on different screens (quit on Home, back elsewhere).
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

	// Mouse events are intentionally dropped. Marina is keyboard-only so
	// the terminal can keep its native text selection / copy behaviour.
	case tea.MouseMsg:
		return m, nil
	}

	// Delegate to the top screen. Screens may return (possibly a new screen
	// instance) + a command; we always replace the top entry in place.
	updated, cmd := m.top().Update(msg)
	m.stack[len(m.stack)-1] = updated
	return m, cmd
}

func (m *dashboard) View() tea.View {
	w := max(m.width, 20)
	h := max(m.height, 5)

	title := m.renderTitleBar(w)
	status := m.renderStatusBar(w)

	bodyHeight := max(h-2, 1)
	body := m.top().View(w, bodyHeight)

	// Guardrails: if a screen returns a malformed body, normalise it so
	// misalignment is impossible. Screens that use panelLines (they all do)
	// already produce exact-height output; this is just defence-in-depth.
	body = normaliseBody(body, w, bodyHeight)

	content := title + "\n" + body + "\n" + status

	// Modal overlay: when the active screen has an open dialog, composite it
	// over the normal view at the screen centre using lipgloss v2's native
	// Layer + Canvas primitives. The background layer is the stacked
	// title/body/status output; the modal sits on top with z=1.
	if mp, ok := m.top().(ModalProvider); ok {
		if modal, active := mp.Modal(); active {
			content = overlayModal(content, modal, w, h)
		}
	}

	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "Marina"
	v.Cursor = nil
	v.KeyboardEnhancements.ReportEventTypes = true

	// Surface tab-level progress to the terminal chrome (dock icon, Windows
	// Terminal taskbar) whenever the active screen is reporting work.
	if pr, ok := m.top().(ProgressReporter); ok {
		if frac, active := pr.Progress(); active {
			pct := int(frac * 100)
			if pct < 0 {
				pct = 0
			} else if pct > 100 {
				pct = 100
			}
			v.ProgressBar = tea.NewProgressBar(tea.ProgressBarDefault, pct)
		}
	}
	return v
}

// ── Chrome rendering ────────────────────────────────────────────────────────

// renderTitleBar paints the top strip. The whole app surface is pure
// black; this strip is rendered but intentionally left blank so the
// terminal gets one row of padding above the content without introducing
// any non-black colour strip.
func (m *dashboard) renderTitleBar(w int) string {
	return sPanel.Width(w).Render("") // sPanel has cBg (pure black)
}

// renderStatusBar paints the bottom strip with the active screen's help on
// the left and the global hints (ctrl+c, esc) on the right. A 2-char side
// margin keeps the text from touching the terminal edges.
func (m *dashboard) renderStatusBar(w int) string {
	help := m.top().Help()
	global := "ctrl+c quit"
	if len(m.stack) > 1 {
		global = "esc back · ctrl+c quit"
	}
	// Inner width after the 2-char left/right margins.
	inner := max(w-4, 0)
	hw, gw := lipglossWidth(help), lipglossWidth(global)
	if hw+gw+2 > inner {
		// Tight terminal — keep help only, truncated to fit.
		return sStatusBar.Width(w).Render("  " + truncateToWidth(help, inner) + "  ")
	}
	pad := max(inner-hw-gw, 0)
	return sStatusBar.Width(w).Render("  " + help + strings.Repeat(" ", pad) + global + "  ")
}

// normaliseBody enforces the (width, height) invariant so a misbehaving
// screen can never bleed into the title/status strips. Lines longer than `w`
// are truncated; missing lines are filled with blank panel rows.
func normaliseBody(body string, w, h int) string {
	lines := strings.Split(body, "\n")
	out := make([]string, h)
	for i := range h {
		if i < len(lines) {
			line := lines[i]
			lw := lipglossWidth(line)
			switch {
			case lw == w:
				out[i] = line
			case lw > w:
				// Over-wide: re-render at exact width. Strips embedded styles
				// but keeps us aligned — screens should never hit this path.
				out[i] = panelLine(sPanel, w, lipgloss.NewStyle().Render(line))
			default:
				// Under-wide: extend with a blank-filled panel row for the gap.
				filler := panelLine(sPanel, w-lw, "")
				out[i] = line + filler
			}
		} else {
			out[i] = panelLine(sPanel, w, "")
		}
	}
	return strings.Join(out, "\n")
}

// lipglossWidth measures the display width of an already-styled string,
// ignoring ANSI escapes. Thin wrapper so the helper's intent is explicit.
func lipglossWidth(s string) int { return lipgloss.Width(s) }
