package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Screen is the contract for every view in the dashboard's navigation stack.
// The dashboard owns the program loop, the window size, and global keys; each
// Screen is a self-contained sub-model for one destination (Home, Hosts,
// Containers, Stacks, Updates, or a detail page).
//
// Navigation is stack-based: Home is pushed first; a section screen pushes
// itself on Enter; Esc pops back. Push/Pop are signalled by returning
// PushScreenMsg / PopScreenMsg from Update.
//
// View receives the inner drawing area (width, height) after the dashboard
// has already reserved the top title bar and bottom status bar. The screen
// MUST return a string of exactly height newline-separated lines, each
// occupying the full width when rendered — the responsive-layout guarantee
// depends on every screen honouring this contract.
type Screen interface {
	Title() string                        // breadcrumb label ("Hosts", "Containers > webapp")
	Init() tea.Cmd                        // initial command (fetches, ticks, ...)
	Update(msg tea.Msg) (Screen, tea.Cmd) // consumes messages, returns (possibly new) screen
	View(width, height int) string        // renders inside the body panel
	Help() string                         // one-line status-bar hint
}

// ProgressReporter is an optional screen mix-in. Screens that return active=true
// from Progress surface work state to the dashboard, which forwards it to the
// terminal chrome (dock icon, Windows Terminal taskbar) via v2's native
// progress bar. Screens with no long-running work need not implement this.
type ProgressReporter interface {
	Progress() (fraction float64, active bool)
}

// PushScreenMsg asks the dashboard to push a new screen onto the stack. The
// new screen's Init is invoked immediately after push.
type PushScreenMsg struct{ Screen Screen }

// PopScreenMsg asks the dashboard to pop the top screen. It's a no-op when
// the stack has only the root Home screen.
type PopScreenMsg struct{}

// pushCmd builds a tea.Cmd that emits PushScreenMsg. Use this from a screen's
// Update when the user selects a submenu entry or a row detail.
func pushCmd(s Screen) tea.Cmd {
	return func() tea.Msg { return PushScreenMsg{Screen: s} }
}

// popCmd builds a tea.Cmd that emits PopScreenMsg. Use this from a screen's
// Update for the Esc / back binding.
func popCmd() tea.Cmd {
	return func() tea.Msg { return PopScreenMsg{} }
}

// ── Layout helpers — EVERY screen must use these to stay aligned ───────────
//
// The app is three stacked full-width strips:
//
//   line 0            title bar          (width chars)  background = cBar
//   lines 1..h-2      body panel         (width chars)  background = cPanel
//   line  h-1         status bar         (width chars)  background = cBar
//
// Screens paint into the body panel. They get (width, height-2) and must
// return exactly height-2 lines, each exactly `width` characters wide once
// rendered. The helpers below enforce both invariants.

// panelLine renders a raw string as a single full-width panel row. Content is
// truncated rune-wise when too long and padded with spaces when too short;
// the whole line is then wrapped in the panel background so adjacent cells
// bleed together seamlessly. Pass a style (sListRow, sListRowSelected,
// sSectionHeader, etc.) — all panel styles share the same background so the
// row transitions never leak the terminal default.
func panelLine(style lipgloss.Style, width int, content string) string {
	return style.Width(width).Render(truncateToWidth(content, width))
}

// panelLines stacks `lines` inside a width×height rectangle. Every line is
// forced to full width; any shortfall is padded with blank bg-filled rows at
// the bottom. Every dashboard screen returns its View by calling this.
func panelLines(width, height int, lines []string) string {
	out := make([]string, 0, height)
	for i := range height {
		if i < len(lines) {
			out = append(out, lines[i])
		} else {
			out = append(out, panelLine(sPanel, width, ""))
		}
	}
	return strings.Join(out, "\n")
}

// truncateToWidth trims `s` so its display width is at most `n`. It first
// consults lipgloss.Width (ANSI-aware) and short-circuits when content
// already fits — this preserves pre-styled inputs like the gradient banner
// that pass through panelLine unchanged. When content does overflow we fall
// back to a rune-based truncation with an ellipsis; that path assumes plain
// text, which is the case for every non-banner caller in this package.
func truncateToWidth(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// padRight left-aligns `s` to exactly `width` characters (rune-aware).
// Shorter inputs are padded with spaces; longer inputs are truncated with an
// ellipsis. Use this for column cells inside a panel row.
func padRight(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = truncateToWidth(s, width)
	r := []rune(s)
	if len(r) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(r))
}

// buildColumns returns a single row string from parallel `cells` and `widths`
// arrays, separated by a single space. Lengths must match; any mismatch is
// treated as a programming error and surfaced as a visible "<?>" so it can't
// silently desync the layout.
func buildColumns(cells []string, widths []int) string {
	if len(cells) != len(widths) {
		return "<column/width length mismatch>"
	}
	parts := make([]string, len(cells))
	for i, c := range cells {
		parts[i] = padRight(c, widths[i])
	}
	return strings.Join(parts, " ")
}

// centerText returns `s` padded on the left so it sits centred inside a
// `width`-wide row. Handles ANSI-styled input (via lipgloss.Width) so pre-
// coloured content — gradient banner lines, accented labels — centres
// correctly at any terminal size. Oversized input is truncated.
func centerText(s string, width int) string {
	if width <= 0 {
		return ""
	}
	w := lipgloss.Width(s)
	if w >= width {
		return truncateToWidth(s, width)
	}
	pad := (width - w) / 2
	return strings.Repeat(" ", pad) + s
}

// viewWindow returns the [offset, end) slice of `total` rows that fit in
// `maxRows`, keeping `cursor` inside the window. Callers use it to implement
// cursor-following viewport scrolling in list screens without importing
// bubbles/viewport (we don't need its richer API here — just a stable
// windowing math that every list agrees on).
func viewWindow(total, cursor, maxRows int) (offset, end int) {
	if maxRows <= 0 || total <= 0 {
		return 0, 0
	}
	if total <= maxRows {
		return 0, total
	}
	// Keep cursor 1 row above the bottom when scrolling down; no special
	// handling needed when scrolling up since offset won't go below zero.
	offset = max(cursor-maxRows+2, 0)
	if offset+maxRows > total {
		offset = total - maxRows
	}
	return offset, offset + maxRows
}

// shareWidths splits `total` into len(weights) columns proportional to weights,
// clamping each to its floor. Returns the same number of widths as weights.
// Use this so tables scale smoothly as the terminal resizes.
func shareWidths(total int, weights, floors []int) []int {
	if len(weights) != len(floors) {
		return nil
	}
	// Reserve floors first.
	floorSum := 0
	for _, f := range floors {
		floorSum += f
	}
	// Single-space gutters between columns are owned by the caller (they
	// appear via buildColumns' strings.Join).
	gutters := len(weights) - 1
	avail := max(total-floorSum-gutters, 0)
	weightSum := 0
	for _, w := range weights {
		weightSum += w
	}
	widths := make([]int, len(weights))
	spent := 0
	for i, w := range weights {
		extra := 0
		if weightSum > 0 {
			extra = avail * w / weightSum
		}
		if i == len(weights)-1 {
			// Absorb any rounding drift into the last column.
			extra = max(avail-spent, 0)
		}
		widths[i] = floors[i] + extra
		spent += extra
	}
	return widths
}
