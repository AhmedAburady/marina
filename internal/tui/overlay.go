package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// ── Modal overlay ──────────────────────────────────────────────────────────
//
// Screens signal an active modal by implementing ModalProvider. The dashboard
// composites the returned content over the screen's normal view via
// lipgloss.Canvas + Compositor so it floats dead-centre with the list beneath
// still visible.

type ModalProvider interface {
	Modal() (content string, active bool)
}

// overlayModal centres `modal` over `bg` on a width×height canvas. Uses
// lipgloss.Compositor because Canvas.Compose(layer) ignores per-layer X/Y —
// only the Compositor honours absolute positioning.
func overlayModal(bg, modal string, width, height int) string {
	if width <= 0 || height <= 0 {
		return bg
	}
	mW, mH := lipgloss.Width(modal), lipgloss.Height(modal)
	x := max0((width - mW) / 2)
	y := max0((height - mH) / 2)

	base := lipgloss.NewLayer(bg)
	top := lipgloss.NewLayer(modal).X(x).Y(y).Z(1)

	comp := lipgloss.NewCompositor(base, top)
	canvas := lipgloss.NewCanvas(width, height)
	canvas.Compose(comp)
	return canvas.Render()
}

// ── Standard modal box ─────────────────────────────────────────────────────
//
// One lipgloss style — rounded teal border, pure-black interior (matches the
// app background so nothing looks out of place), generous padding. Every
// modal renders into this box and lets its inner components (textinput,
// spinner) render with their default styles.

var modalBox = lipgloss.NewStyle().
	Border(lipgloss.NormalBorder()).
	BorderForeground(cTeal).
	Background(cBg).
	Foreground(cFg).
	Padding(1, 2)

// Inner text styles — foreground-only on top of modalBox's cBg fill.
var (
	sDialogTitle = lipgloss.NewStyle().Foreground(cTeal).Bold(true)
	sDialogLabel = lipgloss.NewStyle().Foreground(cDim)
	sDialogFocus = lipgloss.NewStyle().Foreground(cTeal).Bold(true)
	sDialogErr   = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	sDialogHelp  = lipgloss.NewStyle().Foreground(cDim)
	sDialogDim   = lipgloss.NewStyle().Foreground(cDim)
)

// renderFormModal assembles a standard labeled-input dialog:
//
//	Title
//	                                             <blank>
//	label                                            (bold teal when focused)
//	<input>                                          (textinput default render)
//	                                                 <blank>
//	...
//	error (if any)
//	help
//
// All inputs render with bubbles/v2 textinput's default styling. Focus is
// indicated by the label going bold+teal instead of dim.
func renderFormModal(title string, fields []formField, focus int, errMsg, help string) string {
	const inputWidth = 44

	var rows []string
	rows = append(rows, sDialogTitle.Render(title))
	rows = append(rows, "")

	for i, f := range fields {
		label := f.label
		if f.required {
			label += " *"
		}
		style := sDialogLabel
		if i == focus {
			style = sDialogFocus
		}
		rows = append(rows, style.Render(label))

		ti := f.input
		ti.SetWidth(inputWidth)
		rows = append(rows, ti.View())

		if i < len(fields)-1 {
			rows = append(rows, "")
		}
	}

	if errMsg != "" {
		rows = append(rows, "")
		rows = append(rows, sDialogErr.Render(errMsg))
	}
	if help != "" {
		rows = append(rows, "")
		rows = append(rows, sDialogHelp.Render(help))
	}

	return modalBox.Render(strings.Join(rows, "\n"))
}

// renderConfirmModal renders a confirm dialog with a pill-style button pair
// (Cancel / <confirmLabel>). `focus` selects the current button: 0 = cancel,
// 1 = confirm.
func renderConfirmModal(title string, details []string, confirmLabel string, focus int) string {
	var rows []string
	rows = append(rows, sDialogTitle.Render(title))
	rows = append(rows, "")
	for _, d := range details {
		rows = append(rows, sDialogDim.Render(d))
	}
	rows = append(rows, "")

	btns := renderPill("Cancel", focus == 0) + "  " + renderPill(confirmLabel, focus == 1)
	rows = append(rows, btns)
	rows = append(rows, "")
	rows = append(rows, sDialogHelp.Render("←/→ select  ·  enter confirm  ·  esc cancel"))

	return modalBox.Render(strings.Join(rows, "\n"))
}

// renderSpinnerModal renders a tiny centred "operation in progress" dialog:
// one line with the bubbles spinner and a bold teal label.
func renderSpinnerModal(spinner, title string) string {
	body := spinner + "  " + sDialogTitle.Render(title)
	return modalBox.Render(body)
}

// renderPill renders one button in the confirm dialog. Focused pill is the
// accent purple; idle pill is dim text on the modal's black bg.
func renderPill(label string, focused bool) string {
	if focused {
		return lipgloss.NewStyle().
			Background(cAccent).
			Foreground(cWhite).
			Bold(true).
			Padding(0, 2).
			Render(label)
	}
	return lipgloss.NewStyle().
		Foreground(cDim).
		Padding(0, 2).
		Render(label)
}
