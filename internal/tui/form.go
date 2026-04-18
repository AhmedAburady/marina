package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// fieldKind is the shape a form field renders as — either a labeled text
// input or a two-state pill toggle.
type fieldKind int

const (
	fieldText fieldKind = iota
	fieldToggle
)

// formField is one row in an inline form. Most fields are text inputs; the
// fieldToggle kind adds a two-pill on/off switch (used for boolean config
// like Disabled).
type formField struct {
	kind        fieldKind
	label       string
	placeholder string
	required    bool
	input       textinput.Model // kind == fieldText
	boolValue   bool            // kind == fieldToggle
	onLabel     string          // kind == fieldToggle (e.g. "Enabled")
	offLabel    string          // kind == fieldToggle (e.g. "Disabled")
}

// newFormField builds a text-input field with a configured placeholder +
// required flag. Caller focuses it via form.focusNext / focusPrev; input
// widths are set at render time in renderFormModal so fields size with the
// modal box.
func newFormField(label, placeholder string, required bool) formField {
	ti := textinput.New()
	ti.Placeholder = placeholder
	// No prompt — the label above each input is the only affordance we use.
	// Leaving the default "> " would both shift the input's x-origin
	// (breaking alignment with the label) and render in a colour that
	// doesn't match the dark input well.
	ti.Prompt = ""
	return formField{
		kind:        fieldText,
		label:       label,
		placeholder: placeholder,
		required:    required,
		input:       ti,
	}
}

// newToggleField builds a two-state pill toggle (on/off). `onLabel` is what
// the pill reads when the value is true (e.g. "Enabled"), `offLabel` when
// it is false (e.g. "Disabled"). `initial` is the starting value.
func newToggleField(label, onLabel, offLabel string, initial bool) formField {
	return formField{
		kind:      fieldToggle,
		label:     label,
		boolValue: initial,
		onLabel:   onLabel,
		offLabel:  offLabel,
	}
}

// newTextFieldWithValue is a convenience for edit-mode forms: builds a text
// field pre-populated with `value`. Useful when the form is opened on an
// existing record (hosts edit, etc.).
func newTextFieldWithValue(label, placeholder, value string, required bool) formField {
	f := newFormField(label, placeholder, required)
	f.input.SetValue(value)
	return f
}

// inlineForm is a modal form. Screens embed this, feed tea.Msg through Update,
// and render via renderFormModal + overlayModal.
type inlineForm struct {
	title  string
	fields []formField
	focus  int
	err    string
}

// newInlineForm builds a blank form with the given title and fields. The
// first text field is focused automatically so typing works immediately.
// If the first field is a toggle, nothing is focused (toggles don't take
// text input).
func newInlineForm(title string, fields []formField) *inlineForm {
	f := &inlineForm{title: title, fields: fields}
	if len(fields) > 0 && fields[0].kind == fieldText {
		f.fields[0].input.Focus()
	}
	return f
}

// Value returns the trimmed value of the nth text field. Returns "" for
// toggle fields or out-of-range indexes.
func (f *inlineForm) Value(i int) string {
	if i < 0 || i >= len(f.fields) || f.fields[i].kind != fieldText {
		return ""
	}
	return strings.TrimSpace(f.fields[i].input.Value())
}

// BoolValue returns the boolean value of the nth toggle field. Returns
// false for text fields or out-of-range indexes.
func (f *inlineForm) BoolValue(i int) bool {
	if i < 0 || i >= len(f.fields) || f.fields[i].kind != fieldToggle {
		return false
	}
	return f.fields[i].boolValue
}

// Update processes a bubbletea message. Returns (submit, cancel, cmd):
//   - submit == true: user pressed Enter with all required fields filled
//   - cancel == true: user pressed Esc
//   - cmd: forwarded to Bubble Tea for cursor blinks
func (f *inlineForm) Update(msg tea.Msg) (submit, cancel bool, cmd tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			return false, true, nil
		case "enter":
			if err := f.validate(); err != "" {
				f.err = err
				return false, false, nil
			}
			return true, false, nil
		case "tab", "down":
			f.focusNext()
			return false, false, nil
		case "shift+tab", "up":
			f.focusPrev()
			return false, false, nil
		}

		// Toggle field keys: space toggles; ←/→ jump to a specific side.
		// These keys never flow into a text input, so they cannot clash
		// with typing while the toggle is focused.
		if f.focus >= 0 && f.focus < len(f.fields) && f.fields[f.focus].kind == fieldToggle {
			switch k.String() {
			case "space":
				f.fields[f.focus].boolValue = !f.fields[f.focus].boolValue
				return false, false, nil
			case "left":
				f.fields[f.focus].boolValue = true
				return false, false, nil
			case "right":
				f.fields[f.focus].boolValue = false
				return false, false, nil
			}
			// Swallow any other keystrokes on a toggle — we don't want
			// them to reach the (unused) text input path below.
			return false, false, nil
		}
	}
	// Delegate character input to the focused text field only.
	if f.focus >= 0 && f.focus < len(f.fields) && f.fields[f.focus].kind == fieldText {
		newInput, c := f.fields[f.focus].input.Update(msg)
		f.fields[f.focus].input = newInput
		cmd = c
	}
	return false, false, cmd
}

// Modal returns the fully rendered modal string. Screens pass this straight
// to the dashboard via the ModalProvider interface.
func (f *inlineForm) Modal() string {
	const help = "tab move  ·  space toggle  ·  enter save  ·  esc cancel"
	return renderFormModal(f.title, f.fields, f.focus, f.err, help)
}

// ── Internals ───────────────────────────────────────────────────────────────

func (f *inlineForm) validate() string {
	for _, field := range f.fields {
		if field.kind != fieldText {
			continue
		}
		if field.required && strings.TrimSpace(field.input.Value()) == "" {
			return field.label + " is required"
		}
	}
	return ""
}

func (f *inlineForm) focusNext() {
	if len(f.fields) == 0 {
		return
	}
	if f.fields[f.focus].kind == fieldText {
		f.fields[f.focus].input.Blur()
	}
	f.focus = (f.focus + 1) % len(f.fields)
	if f.fields[f.focus].kind == fieldText {
		f.fields[f.focus].input.Focus()
	}
}

func (f *inlineForm) focusPrev() {
	if len(f.fields) == 0 {
		return
	}
	if f.fields[f.focus].kind == fieldText {
		f.fields[f.focus].input.Blur()
	}
	f.focus = (f.focus - 1 + len(f.fields)) % len(f.fields)
	if f.fields[f.focus].kind == fieldText {
		f.fields[f.focus].input.Focus()
	}
}
