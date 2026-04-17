// Package tui implements Marina's full-screen dashboard: a screen-stack
// navigation app built on Bubble Tea v2 + lipgloss. Visual rules:
//
//   - App background is pure black EVERYWHERE — title bar, status bar,
//     lists, tables, menu, the body area around every panel.
//   - Darker grey (cAccent*) is used ONLY for accent surfaces: the modal
//     panel, text-input fields inside the modal. Never for the body.
//   - Selected rows render as three-line cards with vertical padding so the
//     selection reads as a generous highlight, not a thin bar.
//   - Zero separator lines. Zero borders. Breathing room via blank rows.
package tui

import "charm.land/lipgloss/v2"

// ── Palette ─────────────────────────────────────────────────────────────────

var (
	// Surfaces — pure neutral greys. NO blue tint; R == G == B for every
	// shade so the palette reads as strict grayscale.
	cBg       = lipgloss.Color("#000000") // app background
	cPanel    = lipgloss.Color("#1a1a1a") // zebra alt row / section header
	cOverlay  = lipgloss.Color("#262626") // modal surface — lighter than zebra
	cInput    = lipgloss.Color("#0a0a0a") // text-input fill inside modals
	cSelected = lipgloss.Color("#7D56F4") // selected row highlight (brand accent, intentional purple)

	// Text — also pure neutral greys.
	cFg    = lipgloss.Color("#e5e5e5")
	cDim   = lipgloss.Color("#8a8a8a")
	cMuted = lipgloss.Color("#5a5a5a")
	cWhite = lipgloss.Color("#ffffff")

	// Accents (semantic)
	cAccent = lipgloss.Color("#7D56F4")
	cTeal   = lipgloss.Color("#38BDF8")
	cGreen  = lipgloss.Color("#50fa7b")
	cYellow = lipgloss.Color("#f1fa8c")
	cRed    = lipgloss.Color("#ff6b6b")
	cOrange = lipgloss.Color("#ffa657")
)

// ── Chrome strips ───────────────────────────────────────────────────────────
//
// Title and status bars are plain ANSI text on the black app background.

var (
	// Both bars sit on pure black. No more grey chrome — headers render
	// as dim text on black. The app surface is black end-to-end.
	sTitleBar  = lipgloss.NewStyle().Background(cBg).Foreground(cTeal).Bold(true)
	sStatusBar = lipgloss.NewStyle().Background(cBg).Foreground(cDim)
)

// ── Body (tables, lists, menus) — all on pure black ────────────────────────

// sPanel is the single-line body style. It lives on the app bg; every row in
// a screen goes through panelLine(sPanel, width, "") for empty fill so there
// are no colour tears even as blank rows flow between content.
var sPanel = lipgloss.NewStyle().Background(cBg).Foreground(cFg)

var (
	// Column headers — bright saturated cyan, bold. Strong visual anchor
	// above the data rows; reads unambiguously as "labels" on black.
	sListHeader = lipgloss.NewStyle().Background(cBg).Foreground(cTeal).Bold(true)

	// Data rows alternate between cBg (pure black) and cPanel (subtle
	// grey) — the "zebra" pattern that keeps long tables readable without
	// grid lines.
	sListRow    = lipgloss.NewStyle().Background(cBg).Foreground(cFg)
	sListRowAlt = lipgloss.NewStyle().Background(cPanel).Foreground(cFg)

	// Selected row — full-width accent bar. White bold text on the purple
	// accent background makes the selection immediately visible against
	// either zebra shade behind it.
	sListRowSelected = lipgloss.NewStyle().
				Background(cSelected).
				Foreground(cWhite).
				Bold(true)

	sHostHeader = lipgloss.NewStyle().Background(cBg).Foreground(cGreen).Bold(true)

	// Body text variants — all on cBg so adjacent rows concatenate seamlessly.
	sMuted   = lipgloss.NewStyle().Background(cBg).Foreground(cDim)
	sBody    = lipgloss.NewStyle().Background(cBg).Foreground(cFg)
	sSuccess = lipgloss.NewStyle().Background(cBg).Foreground(cGreen).Bold(true)
	sWarning = lipgloss.NewStyle().Background(cBg).Foreground(cYellow)
	sError   = lipgloss.NewStyle().Background(cBg).Foreground(cRed)
	sPending = lipgloss.NewStyle().Background(cBg).Foreground(cOrange)
	sSpinner = lipgloss.NewStyle().Background(cBg).Foreground(cAccent)
)

// ── Home menu cards ─────────────────────────────────────────────────────────

var (
	sHomeCard = lipgloss.NewStyle().
			Background(cBg).
			Foreground(cFg).
			Bold(true)

	sHomeCardSelected = lipgloss.NewStyle().
				Background(cSelected).
				Foreground(cWhite).
				Bold(true)
)
