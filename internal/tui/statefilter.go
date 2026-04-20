package tui

import "charm.land/lipgloss/v2"

// stateFilter is the Running/All pill selector shared by the Containers and
// Stacks list screens. Default is Running — fully-stopped rows are hidden
// until the user flips the toggle to All. Renders as two inline pills on the
// black body bg, using the same style language as the settings pill row.
type stateFilter struct {
	showAll bool
}

// Toggle flips between Running (default) and All.
func (f *stateFilter) Toggle() { f.showAll = !f.showAll }

// ShowAll reports whether stopped rows should be kept in the visible list.
func (f *stateFilter) ShowAll() bool { return f.showAll }

// MatchContainerState returns true for a row that passes the current filter.
// When showing all, everything passes; otherwise only docker state "running".
func (f *stateFilter) MatchContainerState(state string) bool {
	return f.showAll || state == "running"
}

// MatchStackRunning returns true when a stack row passes the current filter.
// When showing all, everything passes; otherwise only stacks with at least
// one running container are kept (stopped / fully-exited stacks drop off).
func (f *stateFilter) MatchStackRunning(running int) bool {
	return f.showAll || running > 0
}

// Pill styles for the state toggle strip. Unlike the settings pills (which
// sit on cOverlay), these live on the panel's black background so they line
// up with the filter bar. Active uses the brand accent; idle uses the subtle
// cPanel zebra shade so the unchosen option still reads as a button.
var (
	sStatePillActive = lipgloss.NewStyle().
				Background(cAccent).
				Foreground(cWhite).
				Bold(true).
				Padding(0, 1)

	sStatePillIdle = lipgloss.NewStyle().
			Background(cPanel).
			Foreground(cDim).
			Padding(0, 1)

	sStateLabel   = lipgloss.NewStyle().Background(cBg).Foreground(cDim)
	sStateGap     = lipgloss.NewStyle().Background(cBg)
	sStateDotRun  = lipgloss.NewStyle().Background(cBg).Foreground(cGreen).Bold(true)
	sStateDotStop = lipgloss.NewStyle().Background(cBg).Foreground(cRed).Bold(true)
	sStateNum     = lipgloss.NewStyle().Background(cBg).Foreground(cWhite).Bold(true)
	sStateUnit    = lipgloss.NewStyle().Background(cBg).Foreground(cDim)
)

// View renders the pill strip as one full-width panel row. Running/stopped
// counts sit right after the pills with a generous gap so they're always
// visible regardless of terminal width. Coloured dots carry the semantics;
// numbers are bold in white for scan-ability.
func (f *stateFilter) View(width int, running, stopped int) string {
	runPill := sStatePillIdle.Render("Running")
	allPill := sStatePillIdle.Render("All")
	if f.showAll {
		allPill = sStatePillActive.Render("All")
	} else {
		runPill = sStatePillActive.Render("Running")
	}

	dotRun := sStateDotRun.Render("●")
	dotStop := sStateDotStop.Render("●")

	stats := dotRun + sStateGap.Render(" ") + sStateNum.Render(itoa(running)) + sStateUnit.Render(" running") +
		sStateGap.Render("   ") +
		dotStop + sStateGap.Render(" ") + sStateNum.Render(itoa(stopped)) + sStateUnit.Render(" stopped")

	content := "  " + sStateLabel.Render("Show: ") + runPill + sStateGap.Render(" ") + allPill + sStateGap.Render("    ") + stats
	return panelLine(sPanel, width, content)
}
