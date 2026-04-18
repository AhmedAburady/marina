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
//
// Architecture verified against the v2 upstream recommendation (maintainer
// meowgorithm, bubbletea#1570 / bubbletea#642): Canvas + Layer + Compositor
// is THE pattern for modals in v2 and is what Crush uses in production.

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
// Bright purple surface on black body. No border — the contrast between the
// modal's cAccent fill and the dashboard's cBg body is what makes the popup
// "pop". Default text is black for maximum readability against the purple
// fill. See docs/Styling.md §5.4 (cards) and §A6 (explicit backgrounds).

var modalBox = lipgloss.NewStyle().
	Background(cAccent).
	Foreground(cBg).
	Padding(1, 3)

// cAccentSoft is a light lavender used for the help/muted line inside a
// purple modal — it reads quieter than full black but stays legible.
var cAccentSoft = lipgloss.Color("#E8DFFF")

// cAccentFaint is a washed-out lavender that sits close to the modal fill
// so it reads as "faded ghost text" — used for input placeholders.
var cAccentFaint = lipgloss.Color("#564a77ff")

// Inner text styles — every style carries modalBox's cAccent fill explicitly
// so inner ANSI resets never punch holes in the modal surface.
var (
	sDialogTitle = lipgloss.NewStyle().Background(cAccent).Foreground(cBg).Bold(true)
	// Blurred label uses a softer lavender so focused labels clearly stand
	// out in bold black. No underline, no arrow prefix — alignment between
	// label and input must be preserved.
	sDialogLabel = lipgloss.NewStyle().Background(cAccent).Foreground(cAccentSoft)
	sDialogFocus = lipgloss.NewStyle().Background(cAccent).Foreground(cBg).Bold(true)
	sDialogErr   = lipgloss.NewStyle().Background(cAccent).Foreground(cYellow).Bold(true)
	sDialogHelp  = lipgloss.NewStyle().Background(cAccent).Foreground(cAccentSoft)
	sDialogDim   = lipgloss.NewStyle().Background(cAccent).Foreground(cBg)

	// Styles used by the manual-render path for blurred inputs. Inputs now
	// sit flush on the modal's purple surface — no dark well. Value text is
	// black for contrast; placeholder is soft lavender (italic) so it reads
	// as a hint. A blurred textinput.View() always appends its virtualCursor
	// whose TextStyle is zero-valued → 1-cell bg reset; we bypass View()
	// when blurred and render the value ourselves. See docs/Styling.md §A3.
	sDialogInputValue  = lipgloss.NewStyle().Background(cAccent).Foreground(cBg)
	sDialogInputPlace  = lipgloss.NewStyle().Background(cAccent).Foreground(cAccentFaint).Italic(true)
	sDialogInputFiller = lipgloss.NewStyle().Background(cAccent)
	// sDialogGap fills bare separator cells between styled spans inside
	// modalBox so the cAccent fill stays continuous across the row.
	sDialogGap = lipgloss.NewStyle().Background(cAccent)
)

// renderFormModal assembles a standard labeled-input dialog:
//
//	Title                                         (black bold on purple)
//
//	label                                         (black bold underlined when focused)
//	<input>                                       (dark well with light text)
//
//	...
//	error (if any)                                (yellow bold)
//	help                                          (soft lavender)
func renderFormModal(title string, fields []formField, focus int, errMsg, help string) string {
	const inputWidth = 44

	var rows []string
	rows = append(rows, sDialogTitle.Render(title))
	rows = append(rows, sDialogGap.Render(""))

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
		switch f.kind {
		case fieldToggle:
			rows = append(rows, toggleRow(f))
		default:
			rows = append(rows, inputRow(f, i == focus, inputWidth))
		}

		if i < len(fields)-1 {
			rows = append(rows, sDialogGap.Render(""))
		}
	}

	if errMsg != "" {
		rows = append(rows, sDialogGap.Render(""))
		rows = append(rows, sDialogErr.Render(errMsg))
	}
	if help != "" {
		rows = append(rows, sDialogGap.Render(""))
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
	rows = append(rows, sDialogGap.Render(""))
	for _, d := range details {
		rows = append(rows, sDialogDim.Render(d))
	}
	rows = append(rows, sDialogGap.Render(""))

	btns := renderPill("Cancel", focus == 0) + sDialogGap.Render("  ") + renderPill(confirmLabel, focus == 1)
	rows = append(rows, btns)
	rows = append(rows, sDialogGap.Render(""))
	rows = append(rows, sDialogHelp.Render("←/→ select  ·  enter confirm  ·  esc cancel"))

	return modalBox.Render(strings.Join(rows, "\n"))
}

// renderSpinnerModal renders a tiny centred "operation in progress" dialog:
// one line with the bubbles spinner and a bold black label on the purple
// surface.
func renderSpinnerModal(spinner, title string) string {
	body := spinner + sDialogGap.Render("  ") + sDialogTitle.Render(title)
	return modalBox.Render(body)
}

// inputRow renders one form input as a single seamless dark well on the
// purple modal surface.
//
//   - Focused: the bubbles textinput renders itself so typing and the live
//     cursor work. Its styles are configured for the dark well.
//   - Blurred: the value is rendered manually (bypassing View()) to avoid
//     v2's virtualCursor bg-leak bug. The well is padded to the full input
//     width so every blurred input is the same rectangle as a focused one.
//
// See docs/Styling.md §A3 — this mirrors the settings.valueRow pattern.
func inputRow(f formField, focused bool, width int) string {
	if focused {
		ti := f.input
		ti.SetWidth(width)
		st := ti.Styles()
		st.Focused.Text = sDialogInputValue
		st.Focused.Placeholder = sDialogInputPlace
		st.Blurred.Text = sDialogInputValue
		st.Blurred.Placeholder = sDialogInputPlace
		st.Cursor.Color = cTeal
		ti.SetStyles(st)
		return ti.View()
	}

	// Blurred — manual render. The well width includes the value/placeholder
	// plus trailing filler so every row is the same rectangle.
	value := f.input.Value()
	var head string
	if value == "" {
		head = sDialogInputPlace.Render(f.placeholder)
	} else {
		head = sDialogInputValue.Render(value)
	}
	headW := lipgloss.Width(head)
	if headW >= width {
		return head
	}
	return head + sDialogInputFiller.Render(strings.Repeat(" ", width-headW))
}

// toggleRow renders a two-pill on/off switch for a fieldToggle. The "on"
// side highlights when the field's boolean is true, the "off" side when
// false. The gap between pills carries the modal fill so the row stays
// seamless (see docs/Styling.md §A1).
func toggleRow(f formField) string {
	var onPill, offPill string
	if f.boolValue {
		onPill = renderPill(f.onLabel, true)
		offPill = renderPill(f.offLabel, false)
	} else {
		onPill = renderPill(f.onLabel, false)
		offPill = renderPill(f.offLabel, true)
	}
	return onPill + sDialogGap.Render(" ") + offPill
}

// renderPill renders one button in the confirm dialog.
//
//   - Focused: inverted — black fill with purple bold text, so the active
//     button pops out of the modal's purple surface like a pressed button.
//   - Idle: flat on the modal (same cAccent bg), black text. No visible
//     border — affordance comes purely from the padding + focused state.
func renderPill(label string, focused bool) string {
	if focused {
		return lipgloss.NewStyle().
			Background(cBg).
			Foreground(cAccent).
			Bold(true).
			Padding(0, 2).
			Render(label)
	}
	return lipgloss.NewStyle().
		Background(cAccent).
		Foreground(cBg).
		Padding(0, 2).
		Render(label)
}
