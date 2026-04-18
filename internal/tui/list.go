package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/AhmedAburady/marina/internal/actions"
	"github.com/AhmedAburady/marina/internal/config"
)

// ── List rendering ─────────────────────────────────────────────────────────
//
// Every list screen (Hosts, Containers, Stacks, Updates) speaks the same
// visual language: a muted column-header row, responsive columns with
// rune-aware truncation (never wrap in the TUI), zebra-striped data rows,
// a full-width accent bar for the cursor row, and optional per-host
// section labels. All of that lives here — screens call these helpers and
// don't repeat any layout code.

// rowPadW is the horizontal indent on each side of every row. Small, since
// we want columns to use as much width as possible without grid chrome.
const rowPadW = 2

// innerWidth is the column area inside one row after left + right pad.
// Screens MUST feed this to shareWidths; otherwise rows overflow and
// panelLine truncates with a trailing "…".
func innerWidth(width int) int { return max0(width - 2*rowPadW) }

// pad returns a whitespace string of width n chars. Small helper that keeps
// call sites readable.
func pad(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(" ", n)
}

// ── Row building blocks ────────────────────────────────────────────────────

// listHeader renders the muted column-titles row. No bg, no bold, no
// divider — the header is a subtle anchor. Columns are truncated to their
// target widths so the header always matches the data rows below.
func listHeader(width int, labels []string, widths []int) string {
	return panelLine(sListHeader, width, pad(rowPadW)+buildColumns(labels, widths))
}

// listRow renders one data row with responsive column widths. Cells are
// rune-truncated to their target widths — the TUI never wraps. Zebra
// striping is applied automatically based on the row's parity; the caller
// passes the logical row number (not the visible line) so striping stays
// consistent as content scrolls.
func listRow(width int, cells []string, widths []int, rowIdx int, selected bool) string {
	return listRowColored(width, cells, widths, rowIdx, selected, nil)
}

// listRowColored is listRow with an optional foreground-colour override.
// Pass nil to get the default row foreground (zebra + white). Used by the
// Stacks screen to tint stopped / degraded rows in their CLI-matching
// muted mauve / amber shades while keeping the zebra bg intact.
func listRowColored(width int, cells []string, widths []int, rowIdx int, selected bool, fg color.Color) string {
	body := pad(rowPadW) + buildColumns(cells, widths)
	if selected {
		return panelLine(sListRowSelected, width, body)
	}
	// Zebra background: alternates panel / black without any fg work.
	bg := cBg
	if rowIdx%2 == 0 {
		bg = cPanel
	}
	style := lipgloss.NewStyle().Background(bg)
	if fg == nil {
		style = style.Foreground(cFg)
	} else {
		style = style.Foreground(fg)
	}
	return panelLine(style, width, body)
}

// hostSection renders a full-width pink bar with the host (or "host —
// stack") label, used above each host's table in Containers / Stacks /
// Updates. The bar itself carries the visual weight, so no leading
// chevron/arrow — just the padded label on the vibrant fill.
func hostSection(width int, host string) string {
	return panelLine(sHostHeader, width, pad(rowPadW)+host)
}

// spacer returns a blank row on the pure-black app background. Use between
// sections to give the eye breathing room.
func spacer(width int) string {
	return panelLine(sPanel, width, "")
}

// notice / errorNote / pendingNote / bodyLine / mutedLine render a single
// indented prose line in the relevant semantic colour. `text` MUST be
// plain (no ANSI); panelLine handles truncation.

func notice(width int, text string) string {
	return panelLine(sSuccess, width, pad(rowPadW)+text)
}

func errorNote(width int, text string) string {
	return panelLine(sError, width, pad(rowPadW)+text)
}

func pendingNote(width int, spinner string, text string) string {
	return panelLine(sPending, width, pad(rowPadW)+spinner+" "+text)
}

func bodyLine(width int, text string) string {
	return panelLine(sBody, width, pad(rowPadW)+text)
}

func mutedLine(width int, text string) string {
	return panelLine(sMuted, width, pad(rowPadW)+text)
}

// summaryLine — used by Updates for the "N update(s) available · showing
// all" strip above the list. Same indent as data rows so everything lines
// up vertically.
//
// Both the gap and the meta span carry sSuccess's cBg fill explicitly so
// the row stays seamless — a bare "    " between the plain bold text and
// sMuted's inner render would terminate sSuccess's fill mid-row (see
// docs/Styling.md §A1).
func summaryLine(width int, bold, meta string) string {
	gap := sSuccess.Render("    ")
	return panelLine(sSuccess, width, pad(rowPadW)+bold+gap+sMuted.Render(meta))
}

// ── Grouped list assembly ─────────────────────────────────────────────────

// groupedLines returns the full vertical stack for a multi-host list.
//
// Returns:
//
//	lines        rendered rows, ready to feed into scrollBody.View
//	cursorLine   0-based line of the cursor row (-1 if none)
//	headerLine   0-based line where the cursor's GROUP header begins
//	             (the "▸ hostname" row). scrollBody uses this to pin the
//	             viewport so the header stays visible as long as the
//	             cursor's group fits in the window.
func groupedLines(
	width int,
	headers []string,
	widths []int,
	groups []tableGroup,
	globalCursor int,
) (lines []string, cursorLine, headerLine int) {
	cursorLine, headerLine = -1, -1
	for _, g := range groups {
		groupStart := len(lines)
		lines = append(lines, hostSection(width, g.Label))
		lines = append(lines, spacer(width))
		lines = append(lines, listHeader(width, headers, widths))
		lines = append(lines, spacer(width))

		tableStart := len(lines)
		for i, row := range g.Rows {
			selected := globalCursor == g.FirstRow+i
			var fg color.Color
			if i < len(g.RowColors) {
				fg = g.RowColors[i]
			}
			lines = append(lines, listRowColored(width, row, widths, i, selected, fg))
			if selected {
				cursorLine = tableStart + i
				headerLine = groupStart
			}
		}
		lines = append(lines, spacer(width))
	}
	return
}

// flatLines is the ungrouped counterpart to groupedLines — a single
// header + rows + spacer stack. Used by the Hosts screen (no grouping).
// Returns headerLine=0 so scrollBody can pin the column-header row at
// the top of the viewport when the cursor is near it.
func flatLines(
	width int,
	headers []string,
	widths []int,
	rows [][]string,
	cursor int,
) (lines []string, cursorLine, headerLine int) {
	return flatLinesColored(width, headers, widths, rows, nil, cursor)
}

// flatLinesColored is flatLines with an optional per-row fg override. Rows
// whose index is out of range in colors fall back to the default row style.
func flatLinesColored(
	width int,
	headers []string,
	widths []int,
	rows [][]string,
	colors []color.Color,
	cursor int,
) (lines []string, cursorLine, headerLine int) {
	headerLine = 0
	lines = append(lines, listHeader(width, headers, widths))
	lines = append(lines, spacer(width))

	tableStart := len(lines)
	cursorLine = -1
	for i, row := range rows {
		selected := cursor == i
		var fg color.Color
		if i < len(colors) {
			fg = colors[i]
		}
		lines = append(lines, listRowColored(width, row, widths, i, selected, fg))
		if selected {
			cursorLine = tableStart + i
		}
	}
	return
}

// viewportBody is the shared adapter that drops a scrollable body (with
// cursor-follow) into a screen's fixed-height View. Screens render their
// fixed header/notice rows, then hand the remaining height + the grouped
// content + the cursor line + the cursor's group-header line to this
// helper. headerLine lets scrollBody pin the viewport so the group label
// stays visible while the cursor's group fits in the window.
func viewportBody(
	sb *scrollBody,
	width, height int,
	content []string,
	cursorLine, headerLine int,
) []string {
	if height <= 0 {
		return nil
	}
	rendered := sb.View(width, height, content, cursorLine, headerLine)
	return strings.Split(rendered, "\n")
}

// scopedHosts returns the hosts map a fan-out screen should fetch from.
// An empty hostFilter means "every enabled host" (the default home-menu
// entry point). A non-empty hostFilter narrows the set to that single
// host — used when a list screen is launched scoped from the Hosts
// screen (press `c` for Containers or `s` for Stacks on a row).
//
// When the filter names a host that doesn't exist or is disabled, the
// returned map is empty and the screen will render its empty state.
func scopedHosts(cfg *config.Config, hostFilter string) map[string]*config.HostConfig {
	all := actions.EnabledHosts(cfg)
	if hostFilter == "" {
		return all
	}
	if h, ok := all[hostFilter]; ok {
		return map[string]*config.HostConfig{hostFilter: h}
	}
	return map[string]*config.HostConfig{}
}

// ── Action-state helpers ──────────────────────────────────────────────────

// rowHas reports whether a row key currently has a pending action and/or
// a recorded error. Small helper so callers don't repeat the double-lookup.
func rowHas(pending map[string]bool, errors map[string]string, key string) (bool, bool) {
	_, e := errors[key]
	return pending[key], e
}

// markFor returns the single-character trailing indicator for a row based
// on its pending / error state. Empty when neither applies.
func markFor(pending bool, hasError bool) string {
	switch {
	case pending:
		return "•"
	case hasError:
		return "!"
	}
	return ""
}

// keyOf builds the canonical "host/name" row key used by the Stacks and
// Updates screens when tracking pending actions + errors.
func keyOf(host, name string) string { return host + "/" + name }

// ensure the lipgloss import is retained even if no style is used directly
// inline in this file (future helpers may want to declare inline styles).
var _ = lipgloss.NewStyle
