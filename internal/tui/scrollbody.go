package tui

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
)

// ── Scrollable list body ──────────────────────────────────────────────────
//
// Every list screen (Hosts, Containers, Stacks, Updates) composes the same
// structure: one or more table groups stacked vertically, potentially
// taller than the terminal. This file owns the viewport that makes the
// whole thing scroll with the cursor — so cursor-following behaviour lives
// in exactly one place instead of being reinvented per screen.

// scrollBody is a viewport-backed scrollable area. Screens embed one per
// list, feed it the full pre-rendered content + the cursor's line number,
// and get back a fixed-height view that keeps the cursor on screen.
type scrollBody struct {
	vp    viewport.Model
	ready bool
}

// newScrollBody constructs a scrollBody. The viewport is created lazily
// on the first View call — nothing to do here besides allocate.
func newScrollBody() scrollBody { return scrollBody{} }

// View assembles and renders the scrollable area.
//
//	width, height   — outer dimensions allowed for the body
//	lines           — pre-rendered content rows (each already sized to
//	                  width via panelLine / tableLines)
//	cursorLine      — 0-based index of the line to keep on screen
//	headerLine      — 0-based index of the cursor's group/column header,
//	                  or -1 when there's no header to pin to. When the
//	                  header-to-cursor span fits in the viewport, the Y
//	                  offset is pinned to headerLine so the "▸ hostname"
//	                  label stays visible as the user walks into the
//	                  group. Once the cursor descends past the window,
//	                  the header scrolls off naturally.
//
// Returns a single string with exactly `height` newline-separated rows.
func (s *scrollBody) View(width, height int, lines []string, cursorLine, headerLine int) string {
	if !s.ready {
		s.vp = viewport.New()
		s.ready = true
	}
	s.vp.SetWidth(width)
	s.vp.SetHeight(max0(height))
	s.vp.SetContent(strings.Join(lines, "\n"))

	// Compute the Y offset manually every render. bubbles v2's
	// viewport.EnsureVisible only scrolls when the line is OUTSIDE the
	// current window, which means walking the cursor up from far below
	// never repositions the viewport. Two rules apply in order:
	//
	//   1. If a headerLine is set and the distance from header to cursor
	//      fits in the window, pin the viewport to the header — the user
	//      always sees the group label while browsing its contents.
	//   2. Otherwise, standard cursor-follow with a 1-row edge margin.
	if cursorLine >= 0 && height > 0 {
		y := s.vp.YOffset()

		if headerLine >= 0 && headerLine <= cursorLine && cursorLine-headerLine < height {
			y = headerLine
		} else {
			margin := 1
			top, bot := y+margin, y+height-1-margin
			switch {
			case cursorLine < top:
				y = cursorLine - margin
			case cursorLine > bot:
				y = cursorLine - height + 1 + margin
			}
		}

		if y < 0 {
			y = 0
		}
		if maxY := len(lines) - height; maxY > 0 && y > maxY {
			y = maxY
		}
		s.vp.SetYOffset(y)
	}

	body := s.vp.View()
	// EnsureVisible guarantees the cursor stays visible, but the viewport
	// may still output fewer than `height` lines if content is short. Pad
	// with blank panel rows so the caller's fixed-height slot stays stable.
	got := strings.Count(body, "\n") + 1
	if got < height {
		blank := lipgloss.NewStyle().Background(cBg).Width(width).Render("")
		body += "\n" + strings.Repeat(blank+"\n", height-got-1) + blank
	}
	return body
}
