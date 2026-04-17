package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// filterBar is the shared substring-filter input used by every list screen
// (Hosts, Containers, Stacks, Updates). Activation keybinding is `/`; the
// screen keeps the bar alive between activations so a query persists until
// it's cleared with `esc`.
//
// Lifecycle:
//
//	idle              no query, no bar rendered
//	editing   /  →    bar rendered, keys typed live
//	applied enter →   bar rendered dim, rows still filtered
//	          esc →   query cleared, back to idle
//
// Screens ask the bar whether a row should be shown via Match, so the
// keying/filter logic never duplicates across screens.
type filterBar struct {
	input  textinput.Model
	active bool
}

func newFilterBar() filterBar {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = "filter…"
	ti.CharLimit = 64
	ti.SetVirtualCursor(false) // no stray cursor block against the black bar
	return filterBar{input: ti}
}

// Active reports whether the user is currently editing the query. When
// Active is true the screen must route every KeyPressMsg through
// Update(msg) before checking its own bindings — otherwise typing would
// trigger list-level shortcuts.
func (f *filterBar) Active() bool { return f.active }

// Query returns the current filter string with surrounding whitespace
// stripped. An empty string means "no filter" (Match returns true for
// every row).
func (f *filterBar) Query() string { return strings.TrimSpace(f.input.Value()) }

// HasQuery is true when there's a non-empty filter in effect — regardless
// of whether the input is focused. Screens use this to know if they should
// still render the bar even when editing is done.
func (f *filterBar) HasQuery() bool { return f.Query() != "" }

// Activate opens the input for editing. Idempotent.
func (f *filterBar) Activate() tea.Cmd {
	f.active = true
	return f.input.Focus()
}

// Commit exits editing but keeps the query applied to rows. Use this for
// enter — "I'm done typing, but the filter stays."
func (f *filterBar) Commit() {
	f.active = false
	f.input.Blur()
}

// Clear blanks the query and exits editing. Use this for esc — full
// bail-out back to the unfiltered list.
func (f *filterBar) Clear() {
	f.active = false
	f.input.Blur()
	f.input.SetValue("")
}

// Update delegates a message to the underlying textinput while editing.
// Returns (handled, cmd):
//
//   - handled=true  → the message was consumed by the filter; the caller
//     must NOT process it as a list shortcut
//   - cmd           → cursor tick or re-render command from the input
//
// Caller still receives (false, nil) when the bar is inactive — that's the
// signal to fall through to normal list handling.
func (f *filterBar) Update(msg tea.Msg) (bool, tea.Cmd) {
	if !f.active {
		return false, nil
	}
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			f.Clear()
			return true, nil
		case "enter":
			f.Commit()
			return true, nil
		}
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return true, cmd
}

// Match reports whether any of the passed cells contains the (lower-cased)
// query as a substring. Empty query always matches — callers can drop the
// conditional and just call Match unconditionally.
func (f *filterBar) Match(cells ...string) bool {
	q := strings.ToLower(f.Query())
	if q == "" {
		return true
	}
	for _, c := range cells {
		if strings.Contains(strings.ToLower(c), q) {
			return true
		}
	}
	return false
}

// View returns the filter bar strip or an empty string when the filter is
// idle. Screens prepend this to their `top` rows so the bar lives in the
// same fixed chrome that spacers/notices already use.
//
// Two render states:
//
//	editing  →  `/ <input>                    enter apply · esc clear`
//	applied  →  `/ <query>  (N matches)       / edit · esc clear`
//
// Everything renders on the black panel bg so there's no colour tear
// against the body below.
func (f *filterBar) View(width int, matched int) string {
	if !f.active && !f.HasQuery() {
		return ""
	}

	slash := lipgloss.NewStyle().Background(cBg).Foreground(cAccent).Bold(true).Render("/ ")

	if f.active {
		ti := f.input
		ti.SetWidth(max0(width - 40))
		ti.SetStyles(filterInputStyles)
		help := lipgloss.NewStyle().Background(cBg).Foreground(cDim).Render("  enter apply · esc clear")
		return panelLine(sPanel, width, "  "+slash+ti.View()+help)
	}

	q := lipgloss.NewStyle().Background(cBg).Foreground(cFg).Render(f.Query())
	count := lipgloss.NewStyle().Background(cBg).Foreground(cDim).
		Render("  (" + itoa(matched) + " matches)")
	help := lipgloss.NewStyle().Background(cBg).Foreground(cDim).
		Render("  / edit · esc clear")
	return panelLine(sPanel, width, "  "+slash+q+count+help)
}

// Filter input styles — mirror the modal input palette but on the black
// body bg so the bar blends with the rest of the screen chrome.
var filterInputStyles = textinput.Styles{
	Focused: textinput.StyleState{
		Prompt:      lipgloss.NewStyle().Foreground(cAccent).Background(cBg),
		Text:        lipgloss.NewStyle().Foreground(cFg).Background(cBg),
		Placeholder: lipgloss.NewStyle().Foreground(cMuted).Background(cBg),
		Suggestion:  lipgloss.NewStyle().Foreground(cMuted).Background(cBg),
	},
	Blurred: textinput.StyleState{
		Prompt:      lipgloss.NewStyle().Foreground(cMuted).Background(cBg),
		Text:        lipgloss.NewStyle().Foreground(cDim).Background(cBg),
		Placeholder: lipgloss.NewStyle().Foreground(cMuted).Background(cBg),
		Suggestion:  lipgloss.NewStyle().Foreground(cMuted).Background(cBg),
	},
}

// itoa is inlined here to avoid importing strconv in a file that otherwise
// needs only strings.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
